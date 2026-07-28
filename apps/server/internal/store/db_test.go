package store

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// TestOpenStoresTimestampsInUTC 守护「控制面所有 GORM 自动时间戳必须为 UTC」。
//
// 缺陷背景：GORM 默认 NowFunc 用 time.Now()（本地时区）。在非 UTC 时区机器上，
// autoCreateTime 会把 CreatedAt 写成本地时间（如 +08:00）。而注册/健康等内存侧
// 时间一律 UTC、FR-73 服务分析按 UTC 时间窗过滤审计——本地时间戳会让默认「近 N 天」
// 窗口把最近「时区偏移」小时内的活动错误排除在外（+08:00 下默认视图丢最近约 8 小时）。
// 修复：store.Open 的 gorm.Config 设 NowFunc 恒返回 UTC，全表时间戳统一 UTC。
func TestOpenStoresTimestampsInUTC(t *testing.T) {
	// 强制本地时区为 +08:00，使 time.Now() 带非零偏移，从而在任何机器上复现该缺陷。
	orig := time.Local
	time.Local = time.FixedZone("UTC+8", 8*3600)
	t.Cleanup(func() { time.Local = orig })

	db, err := Open(config.DatabaseConfig{
		Driver: "sqlite", DSN: "file:utc_ts_test?mode=memory&cache=shared",
		MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })

	// 不显式设 CreatedAt，交由 GORM autoCreateTime（经 NowFunc）填充。
	row := &model.AuditLog{
		NamespaceCode: "prod", Operator: "admin", Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "ref", Result: model.ResultOK,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
	if _, offset := row.CreatedAt.Zone(); offset != 0 {
		t.Fatalf("CreatedAt 应为 UTC（offset 0），实际 offset=%d 秒——时间戳存了本地时间，会让 FR-73 服务分析按 UTC 窗口漏掉最近活动", offset)
	}
}

// TestOpenBackfillsLegacyLobbyClusters 锁定升级数据库的大厅集群回填与重复启动幂等性。
func TestOpenBackfillsLegacyLobbyClusters(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开升级前 sqlite 失败: %v", err)
	}
	if err := legacy.AutoMigrate(&model.Namespace{}); err != nil {
		t.Fatalf("创建升级前 namespace 表失败: %v", err)
	}
	legacyNamespace := &model.Namespace{Code: "legacy", Name: "历史环境"}
	if err := legacy.Create(legacyNamespace).Error; err != nil {
		t.Fatalf("写入升级前 namespace 失败: %v", err)
	}
	legacySQL, err := legacy.DB()
	if err != nil {
		t.Fatalf("获取升级前 sqlite 连接失败: %v", err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatalf("关闭升级前 sqlite 连接失败: %v", err)
	}

	cfg := config.DatabaseConfig{Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("第一次升级打开数据库失败: %v", err)
	}
	if !db.Migrator().HasColumn(&model.Server{}, "lobby_cluster_id") {
		t.Fatal("升级后 server 必须具有可空 lobby_cluster_id 列")
	}
	assertLobbyClusterCount(t, db, legacyNamespace.ID, 1)
	Close(db)

	db, err = Open(cfg)
	if err != nil {
		t.Fatalf("重复升级打开数据库失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })
	assertLobbyClusterCount(t, db, legacyNamespace.ID, 1)
}

// legacyAgentIdentity 是 FR-203 前的 agent_identity 表结构，用于验证新增来源字段的升级回填。
type legacyAgentIdentity struct {
	ID               uint       `gorm:"primaryKey;autoIncrement"`
	IdentityID       string     `gorm:"column:identity_id;size:64;not null;uniqueIndex"`
	NamespaceID      uint       `gorm:"column:namespace_id;not null;index:idx_agent_identity_ns_server,priority:1;index"`
	ServerID         string     `gorm:"column:server_id;size:64;not null;index:idx_agent_identity_ns_server,priority:2"`
	Kind             string     `gorm:"column:kind;size:16;not null"`
	Status           string     `gorm:"column:status;size:16;not null;index"`
	BootID           string     `gorm:"column:boot_id;size:36"`
	LastAddr         string     `gorm:"column:last_addr;size:64"`
	AgentVersion     string     `gorm:"column:agent_version;size:32"`
	PendingExpiresAt *time.Time `gorm:"column:pending_expires_at"`
	BoundAt          *time.Time `gorm:"column:bound_at"`
	StatusChangedAt  time.Time  `gorm:"column:status_changed_at;not null"`
	ConflictReason   string     `gorm:"column:conflict_reason;size:255"`
	ConflictPeers    string     `gorm:"column:conflict_peers;type:text"`
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func (legacyAgentIdentity) TableName() string { return "agent_identity" }

// TestOpenBackfillsLegacyIdentityBindingSources 锁定老库升级后的来源回填与幂等性。
func TestOpenBackfillsLegacyIdentityBindingSources(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-identity.db")
	legacy, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开升级前 sqlite 失败: %v", err)
	}
	if err := legacy.AutoMigrate(&model.Namespace{}, &legacyAgentIdentity{}); err != nil {
		t.Fatalf("创建升级前身份表失败: %v", err)
	}
	ns := &model.Namespace{Code: "legacy", Name: "历史环境"}
	if err := legacy.Create(ns).Error; err != nil {
		t.Fatalf("写入升级前 namespace 失败: %v", err)
	}
	legacyIdentity := &legacyAgentIdentity{
		IdentityID: "20300000-0000-4000-8000-000000000004", NamespaceID: ns.ID, ServerID: "legacy-203",
		Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive, LastAddr: "legacy.example:25565", StatusChangedAt: time.Now().UTC(),
	}
	if err := legacy.Create(legacyIdentity).Error; err != nil {
		t.Fatalf("写入升级前身份失败: %v", err)
	}
	legacySQL, err := legacy.DB()
	if err != nil {
		t.Fatalf("获取升级前 sqlite 连接失败: %v", err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatalf("关闭升级前 sqlite 连接失败: %v", err)
	}

	cfg := config.DatabaseConfig{Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("升级打开数据库失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })
	if !db.Migrator().HasColumn(&model.AgentIdentity{}, "binding_source") {
		t.Fatal("升级后 agent_identity 必须具有 binding_source 列")
	}
	if !db.Migrator().HasTable(&model.AgentEndpoint{}) {
		t.Fatal("升级后必须创建 agent_endpoint 表保存多 listener 权威事实")
	}
	assertLegacyBindingSource(t, db, legacyIdentity.IdentityID)
	var upgraded model.AgentIdentity
	if err := db.Where("identity_id = ?", legacyIdentity.IdentityID).First(&upgraded).Error; err != nil {
		t.Fatalf("读取升级后身份失败: %v", err)
	}
	if upgraded.LastAddr != legacyIdentity.LastAddr {
		t.Fatalf("地址列扩容迁移不得丢失旧兼容地址，实际 %q", upgraded.LastAddr)
	}

	if err := db.Exec("UPDATE agent_identity SET binding_source = '' WHERE identity_id = ?", legacyIdentity.IdentityID).Error; err != nil {
		t.Fatalf("构造空来源存量行失败: %v", err)
	}
	if err := backfillLegacyIdentityBindingSources(db); err != nil {
		t.Fatalf("回填空来源失败: %v", err)
	}
	if err := backfillLegacyIdentityBindingSources(db); err != nil {
		t.Fatalf("重复回填空来源失败: %v", err)
	}
	assertLegacyBindingSource(t, db, legacyIdentity.IdentityID)
}

// TestOpenMigratesLegacyRequiredIdentityServerID 验证老库的 server_id 非空约束升级后可保存待审批身份。
func TestOpenMigratesLegacyRequiredIdentityServerID(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-required-server-id.db")
	legacy, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开升级前 sqlite 失败: %v", err)
	}
	if err := legacy.AutoMigrate(&model.Namespace{}, &legacyAgentIdentity{}); err != nil {
		t.Fatalf("创建升级前身份表失败: %v", err)
	}
	namespace := &model.Namespace{Code: "legacy-required", Name: "历史必填身份环境"}
	if err := legacy.Create(namespace).Error; err != nil {
		t.Fatalf("写入升级前 namespace 失败: %v", err)
	}
	legacyIdentity := &legacyAgentIdentity{
		IdentityID: "20300000-0000-4000-8000-000000000005", NamespaceID: namespace.ID, ServerID: "legacy-203",
		Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusActive, StatusChangedAt: time.Now().UTC(),
	}
	if err := legacy.Create(legacyIdentity).Error; err != nil {
		t.Fatalf("写入升级前身份失败: %v", err)
	}
	legacySQL, err := legacy.DB()
	if err != nil {
		t.Fatalf("获取升级前 sqlite 连接失败: %v", err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatalf("关闭升级前 sqlite 连接失败: %v", err)
	}

	cfg := config.DatabaseConfig{Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("升级打开数据库失败: %v", err)
	}
	pending := &model.AgentIdentity{
		IdentityID: "20300000-0000-4000-8000-000000000006", NamespaceID: namespace.ID,
		Kind: model.ServerKindBackend, Status: model.AgentIdentityStatusPending, StatusChangedAt: time.Now().UTC(),
		BindingSource: model.AgentIdentityBindingSourceAdminAssigned,
	}
	if err := db.Create(pending).Error; err != nil {
		t.Fatalf("升级后必须能写入 serverId 为 NULL 的待审批身份: %v", err)
	}
	assertLegacyBindingSource(t, db, legacyIdentity.IdentityID)
	assertIdentityServerIDIsNull(t, db, pending.IdentityID)
	Close(db)

	db, err = Open(cfg)
	if err != nil {
		t.Fatalf("重复升级打开数据库失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })
	assertLegacyBindingSource(t, db, legacyIdentity.IdentityID)
	assertIdentityServerIDIsNull(t, db, pending.IdentityID)
}

func assertLegacyBindingSource(t *testing.T, db *gorm.DB, identityID string) {
	t.Helper()
	var identity model.AgentIdentity
	if err := db.Where("identity_id = ?", identityID).First(&identity).Error; err != nil {
		t.Fatalf("读取身份 %s 失败: %v", identityID, err)
	}
	if identity.BindingSource != model.AgentIdentityBindingSourceLegacyLocal {
		t.Fatalf("存量身份来源应为 legacy_local，实际 %q", identity.BindingSource)
	}
	if !identity.ServerID.Assigned() || string(identity.ServerID) != "legacy-203" {
		t.Fatalf("升级不得改变既有 serverId，实际 %q", identity.ServerID)
	}
}

func assertIdentityServerIDIsNull(t *testing.T, db *gorm.DB, identityID string) {
	t.Helper()
	var isNull int
	if err := db.Raw("SELECT CASE WHEN server_id IS NULL THEN 1 ELSE 0 END FROM agent_identity WHERE identity_id = ?", identityID).Scan(&isNull).Error; err != nil {
		t.Fatalf("读取身份 %s 的 serverId NULL 状态失败: %v", identityID, err)
	}
	if isNull != 1 {
		t.Fatalf("待审批身份 %s 的 serverId 应为 SQL NULL，实际不是 NULL", identityID)
	}
}

func assertLobbyClusterCount(t *testing.T, db *gorm.DB, namespaceID uint, want int64) {
	t.Helper()
	var got int64
	if err := db.Model(&model.LobbyCluster{}).Where("namespace_id = ?", namespaceID).Count(&got).Error; err != nil {
		t.Fatalf("统计 namespace %d 的大厅集群失败: %v", namespaceID, err)
	}
	if got != want {
		t.Fatalf("namespace %d 的大厅集群数应为 %d，实际 %d", namespaceID, want, got)
	}
}

// legacyEnv 是 FR-205 前的 env 表结构，用于验证 code 回填。
type legacyEnv struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex"`
	Description string `gorm:"column:description;size:255"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (legacyEnv) TableName() string { return "env" }

type legacyBCCluster struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	NamespaceID uint   `gorm:"column:namespace_id;not null;uniqueIndex:uk_bc_cluster_name,priority:1;index"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex:uk_bc_cluster_name,priority:2"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (legacyBCCluster) TableName() string { return "bc_cluster" }

type legacyRegion struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	BCClusterID uint   `gorm:"column:bc_cluster_id;not null;uniqueIndex:uk_region_name,priority:1;index"`
	Name        string `gorm:"column:name;size:64;not null;uniqueIndex:uk_region_name,priority:2"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (legacyRegion) TableName() string { return "region" }

type legacyZone struct {
	ID        uint   `gorm:"primaryKey;autoIncrement"`
	RegionID  uint   `gorm:"column:region_id;not null;uniqueIndex:uk_zone_name,priority:1;index"`
	Name      string `gorm:"column:name;size:64;not null;uniqueIndex:uk_zone_name,priority:2"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (legacyZone) TableName() string { return "zone" }

type legacyServer struct {
	ID          uint   `gorm:"primaryKey;autoIncrement"`
	NamespaceID uint   `gorm:"column:namespace_id;not null;uniqueIndex:uk_server_id,priority:1;index"`
	ServerID    string `gorm:"column:server_id;size:64;not null;uniqueIndex:uk_server_id,priority:2"`
	Kind        string `gorm:"column:kind;size:16;not null"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

func (legacyServer) TableName() string { return "server" }

// TestOpenBackfillsStableBusinessNames 锁定 FR-205 新列回填、幂等以及 displayName 可重复。
func TestOpenBackfillsStableBusinessNames(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "legacy-stable-names.db")
	legacy, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开升级前 sqlite 失败: %v", err)
	}
	if err := legacy.AutoMigrate(&model.Namespace{}, &legacyEnv{}, &legacyBCCluster{}, &legacyRegion{}, &legacyZone{}, &legacyServer{}); err != nil {
		t.Fatalf("创建升级前稳定名称表失败: %v", err)
	}
	ns := &model.Namespace{Code: "legacy", Name: "历史环境"}
	if err := legacy.Create(ns).Error; err != nil {
		t.Fatalf("写入升级前 namespace 失败: %v", err)
	}
	env := &legacyEnv{Name: "历史Env"}
	cluster := &legacyBCCluster{NamespaceID: ns.ID, Name: "历史BC"}
	if err := legacy.Create(env).Error; err != nil {
		t.Fatalf("写入升级前 env 失败: %v", err)
	}
	if err := legacy.Create(cluster).Error; err != nil {
		t.Fatalf("写入升级前 BC 集群失败: %v", err)
	}
	region := &legacyRegion{BCClusterID: cluster.ID, Name: "历史大区"}
	if err := legacy.Create(region).Error; err != nil {
		t.Fatalf("写入升级前大区失败: %v", err)
	}
	zone := &legacyZone{RegionID: region.ID, Name: "历史小区"}
	if err := legacy.Create(zone).Error; err != nil {
		t.Fatalf("写入升级前小区失败: %v", err)
	}
	server := &legacyServer{NamespaceID: ns.ID, ServerID: "lobby-1", Kind: model.ServerKindBackend}
	if err := legacy.Create(server).Error; err != nil {
		t.Fatalf("写入升级前 server 失败: %v", err)
	}
	legacySQL, err := legacy.DB()
	if err != nil {
		t.Fatalf("获取升级前 sqlite 连接失败: %v", err)
	}
	if err := legacySQL.Close(); err != nil {
		t.Fatalf("关闭升级前 sqlite 连接失败: %v", err)
	}

	cfg := config.DatabaseConfig{Driver: "sqlite", DSN: dsn, MaxOpenConns: 1, MaxIdleConns: 1, ConnMaxLifetimeSec: 60}
	db, err := Open(cfg)
	if err != nil {
		t.Fatalf("升级打开数据库失败: %v", err)
	}
	assertStableNameBackfill(t, db)
	if err := db.Create(&model.BCCluster{NamespaceID: ns.ID, Code: "bc-2", Name: "历史BC"}).Error; err != nil {
		t.Fatalf("旧 name 唯一索引应移除以允许 displayName 重复: %v", err)
	}
	Close(db)

	db, err = Open(cfg)
	if err != nil {
		t.Fatalf("重复升级打开数据库失败: %v", err)
	}
	t.Cleanup(func() { Close(db) })
	assertStableNameBackfill(t, db)
}

func assertStableNameBackfill(t *testing.T, db *gorm.DB) {
	t.Helper()
	var env model.Env
	if err := db.First(&env, "name = ?", "历史Env").Error; err != nil {
		t.Fatalf("读取升级后 env 失败: %v", err)
	}
	if env.Code != "历史Env" {
		t.Fatalf("env code 应由 name 回填，实际 %+v", env)
	}
	var zone model.Zone
	if err := db.First(&zone, "name = ?", "历史小区").Error; err != nil {
		t.Fatalf("读取升级后小区失败: %v", err)
	}
	if zone.Code != "历史小区" {
		t.Fatalf("zone code 应由 name 回填，实际 %+v", zone)
	}
	var server model.Server
	if err := db.First(&server, "server_id = ?", "lobby-1").Error; err != nil {
		t.Fatalf("读取升级后 server 失败: %v", err)
	}
	if server.DisplayName != "lobby-1" {
		t.Fatalf("server displayName 应由 serverId 回填，实际 %+v", server)
	}
}
