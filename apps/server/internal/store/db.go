// Package store 是基础设施层：GORM 连接、连接池与表结构迁移。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// bootstrapTimeout 是启动期建表 / 存量回填的期限（分钟级）。
//
// 为什么引导路径要单独放宽：连接等待防护里的 call 预算是给**在线业务语句**定值的（秒级），
// 而 `AutoMigrate` 在 MySQL 大表上是一条 `ALTER TABLE`。实测（探针）：单条语句预算是硬约束，
// 到点即被 ctx 掐断且无法续跑。若沿用秒级预算，升级时一次慢 DDL 就会让控制面**再也起不来**
// （AutoMigrate 失败即 fail-fast）——这正是「加防护反而引入新故障」的典型。
// 故给引导路径 30 分钟：足够覆盖大表 DDL，又仍是有界值——真卡死时不会像修复前那样无限挂住启动。
const bootstrapTimeout = 30 * time.Minute

// Open 按配置建立 GORM 连接、设置连接池并对表结构做 AutoMigrate。
// 连接或 Ping 失败时返回错误，由上层 fail-fast 退出（控制面无库不可启动）。
func Open(cfg config.DatabaseConfig) (*gorm.DB, error) {
	dialector, err := newDialector(cfg)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger: newGormLogger(),
		// 把方言专有的约束冲突错误翻译为可移植的 gorm.ErrDuplicatedKey 等
		TranslateError: true,
		// 全表自动时间戳（CreatedAt/UpdatedAt）统一用 UTC：与注册/健康等内存侧时间一致，
		// 否则非 UTC 时区机器上时间戳带本地偏移，会让 FR-73 服务分析按 UTC 窗口漏掉最近活动。
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("获取底层连接池失败: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("数据库 Ping 失败: %w", err)
	}

	// 引导期（建表 + 存量回填）统一挂宽松期限，见 bootstrapTimeout。
	//
	// 必须用**独立变量**承载：WithContext 返回持有该 ctx 的克隆，若赋回 db 再返回，
	// 随后的 `defer bootCancel()` 会让调用方拿到的连接上下文立即取消、全库查询当场失效。
	bootCtx, bootCancel := context.WithTimeout(context.Background(), bootstrapTimeout)
	defer bootCancel()
	bootDB := db.WithContext(bootCtx)

	// AutoMigrate 仅用于建表/补字段；DDL 由 GORM 按方言生成，业务零方言绑定。
	// instance 镜像表 MVP 不建（注册/健康运行态以内存为准）。
	if err := bootDB.AutoMigrate(
		&model.Namespace{},
		&model.ConfigItem{},
		&model.ConfigRevision{},
		&model.ConfigPendingChange{},
		&model.ConfigGray{},
		&model.FileObject{},
		&model.FileRevision{},
		&model.FilePendingChange{},
		&model.FileOverrideSet{},
		&model.FileOverrideSetRevision{},
		&model.ZoneAssignment{},
		&model.ServerDrain{},
		&model.ServerOffline{},
		&model.AuditLog{},
		&model.AlertEvent{},
		&model.MetricSample{},
		&model.APIKey{},
		&model.ApprovalRequest{},
		&model.ApprovalExecutionReceipt{},
		&model.ApprovalCredentialSecret{},
		&model.SystemExecution{},
		&model.SensitiveAccessGrant{},
		// MCP OAuth（FR-219）：客户端、待审批凭据变更与短期 token 均为控制面持久事实。
		&model.MCPOAuthClient{},
		&model.MCPOAuthClientChange{},
		&model.MCPAccessToken{},
		&model.AgentCommand{},
		&model.ReverseFetchTask{},
		&model.ReverseFetchIgnoreRule{},
		&model.Setting{},
		&model.ReversibleOperation{},
		&model.FileSyncTask{},
		&model.FileSyncBatch{},
		&model.FileSyncTarget{},
		&model.FileSyncLog{},
		&model.NamespaceTrust{},
		&model.Env{},
		&model.EnvNamespace{},
		&model.BCCluster{},
		&model.Region{},
		&model.Zone{},
		&model.LobbyCluster{},
		&model.Server{},
		&model.AgentIdentity{},
		&model.ServerTag{},
		&model.AgentEndpoint{},
		&model.HealthWeightsRev{},
		// 热冷归档任务表（FR-151，见 ADR-0066）：落热库、控制面事实，不随数据归档
		&model.ArchiveJob{},
		&model.ArchiveJobItem{},
		// 配置中心 V2（FR-160/161）：文件 + 层版本不可变链，低频小表不分日表、不进归档（spec §3.4）
		&model.ConfigFile{},
		&model.ConfigLayerVersion{},
		// 文件资产 V2（FR-163/164）：每服最新清单 + 扫描概要，只存最新快照，不分日表（spec §3.1/§3.2）
		&model.FileAsset{},
		&model.FileAssetScan{},
		// 交付编排 V2（FR-162/165/166/167/168/171，spec v2-delivery-orchestration.md §3）：
		// 变更单统一发布——单 / 项 / 批 / 目标四层编排事实 + 内容寻址中转 blob 元数据，全落 MySQL 可恢复
		&model.ChangeOrder{},
		&model.ChangeOrderItem{},
		&model.ChangeBatch{},
		&model.ChangeTarget{},
		// 回滚动作记录（FR-270 / FR-271）：动作头 + 逐台结果快照，回答「发生过哪些回滚动作」。
		&model.ChangeRollbackRecord{},
		&model.ChangeRollbackRecordTarget{},
		&model.DeliveryBlob{},
		// 配置灰度冻结渲染工件（FR-171，见 ADR-0071）：config_change 项 per-(项, 目标) 渲染 sha 无法落
		// change_order_item，单独冻结持久化，供 manifest / 下载授权 / 清理护栏读取，不再重渲染
		&model.DeliveryConfigArtifact{},
	); err != nil {
		return nil, fmt.Errorf("自动迁移表结构失败: %w", err)
	}
	if err := backfillStableBusinessNames(bootDB); err != nil {
		return nil, err
	}
	if err := dropLegacyDisplayNameUniqueIndexes(bootDB); err != nil {
		return nil, err
	}
	if err := backfillLobbyClusters(bootDB); err != nil {
		return nil, err
	}
	if err := backfillLegacyIdentityBindingSources(bootDB); err != nil {
		return nil, err
	}
	if err := backfillMachineRegisteredIdentitySources(bootDB); err != nil {
		return nil, err
	}

	// 告警处理状态存量回填（FR-157，见 ADR-0064）：加列前的 append-only 历史行属过去已闭事件，
	// 回填为终态 resolved，避免把当前健康 activeAlerts 撑爆。幂等一次性——只命中空串 / NULL 的旧行，
	// 新行由应用层显式写 open、不会被回填。
	if err := backfillLegacyAlertStatus(bootDB); err != nil {
		return nil, err
	}
	// 告警收敛索引换代（FR-232 恶化链合并）：旧索引把 to_status 当方向维度，新收敛键不再含它。
	if err := dropLegacyAlertDedupIndex(bootDB); err != nil {
		return nil, err
	}
	return db, nil
}

// backfillStableBusinessNames 为新增稳定标识 / 展示名字段做可重入回填。
func backfillStableBusinessNames(db *gorm.DB) error {
	updates := []struct {
		model  any
		field  string
		source string
		errMsg string
	}{
		{&model.Env{}, "code", "name", "回填 env code 失败"},
		{&model.BCCluster{}, "code", "name", "回填 BC 集群 code 失败"},
		{&model.Region{}, "code", "name", "回填大区 code 失败"},
		{&model.Zone{}, "code", "name", "回填小区 code 失败"},
		{&model.Server{}, "display_name", "server_id", "回填 server 展示名失败"},
		{&model.Namespace{}, "name", "code", "回填 namespace 展示名失败"},
	}
	for _, item := range updates {
		res := db.Model(item.model).Where(item.field+" = ? OR "+item.field+" IS NULL", "").Update(item.field, gorm.Expr(item.source))
		if res.Error != nil {
			return fmt.Errorf("%s: %w", item.errMsg, res.Error)
		}
	}
	return nil
}

// dropLegacyDisplayNameUniqueIndexes 移除旧 name 唯一索引，让 displayName 可重复。
func dropLegacyDisplayNameUniqueIndexes(db *gorm.DB) error {
	indexes := []struct {
		model any
		name  string
	}{
		{&model.Env{}, "idx_env_name"},
		{&model.BCCluster{}, "uk_bc_cluster_name"},
		{&model.Region{}, "uk_region_name"},
		{&model.Zone{}, "uk_zone_name"},
	}
	for _, idx := range indexes {
		if db.Migrator().HasIndex(idx.model, idx.name) {
			if err := db.Migrator().DropIndex(idx.model, idx.name); err != nil {
				return fmt.Errorf("移除旧展示名唯一索引 %s 失败: %w", idx.name, err)
			}
		}
	}
	return nil
}

// backfillLobbyClusters 为升级前已有的 namespace 补建唯一空大厅集群。
// 不推断成员；重复启动仅命中已有行，保持幂等。
func backfillLobbyClusters(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var namespaceIDs []uint
		if err := tx.Model(&model.Namespace{}).Pluck("id", &namespaceIDs).Error; err != nil {
			return fmt.Errorf("查询 namespace 以回填大厅集群失败: %w", err)
		}
		for _, namespaceID := range namespaceIDs {
			cluster := model.LobbyCluster{NamespaceID: namespaceID}
			if err := tx.Where("namespace_id = ?", namespaceID).FirstOrCreate(&cluster).Error; err != nil {
				return fmt.Errorf("回填 namespace %d 的大厅集群失败: %w", namespaceID, err)
			}
		}
		return nil
	})
}

// backfillLegacyIdentityBindingSources 为升级前没有来源字段的身份绑定补上 legacy_local。
// 只命中空值，重复启动不覆盖新身份的 admin_assigned 来源。
func backfillLegacyIdentityBindingSources(db *gorm.DB) error {
	res := db.Model(&model.AgentIdentity{}).
		Where("binding_source = ? OR binding_source IS NULL", "").
		Update("binding_source", model.AgentIdentityBindingSourceLegacyLocal)
	if res.Error != nil {
		return fmt.Errorf("回填存量身份绑定来源失败: %w", res.Error)
	}
	return nil
}

// backfillMachineRegisteredIdentitySources 把 FR-235 之前由控制面机器注册预置的身份行标记出来。
//
// 判据：来源为 admin_assigned **且** boot_id 为空。FR-235 之前机器注册写入的正是这个组合
// （该路径从不写 bootId，来源取 admin_assigned），而真 agent 身份在注册时强制带 bootId，
// 故该组合唯一指向「控制面预置的占位空壳」。迁移后这类行可在审批时自动让位给真 agent。
//
// 只命中上述组合，不触碰其他来源或带 bootId 的行；幂等（回填后来源已变，重复启动不再命中）。
func backfillMachineRegisteredIdentitySources(db *gorm.DB) error {
	res := db.Model(&model.AgentIdentity{}).
		Where("binding_source = ? AND (boot_id = ? OR boot_id IS NULL)", model.AgentIdentityBindingSourceAdminAssigned, "").
		Update("binding_source", model.AgentIdentityBindingSourceMachineRegistered)
	if res.Error != nil {
		return fmt.Errorf("回填机器注册预置身份来源失败: %w", res.Error)
	}
	// 该回填是**语义不可逆**的（改写来源、无反向回填），故记录命中行数：
	// 运维升级后可从日志确认影响了多少行、是否与预期（CP 预置壳数）一致。
	if res.RowsAffected > 0 {
		slog.Info("回填控制面预置身份来源为 machine_registered", "行数", res.RowsAffected)
	}
	return nil
}

// dropLegacyAlertDedupIndex 清理告警收敛的旧复合索引 idx_alert_event_dedup（FR-232 恶化链合并）。
//
// 背景：该索引原为 (server_id, namespace, to_status, type)，把方向 to_status 当收敛维度；恶化链合并后
// 收敛键改为 (server_id, namespace, type)，新索引名为 idx_alert_event_dedup_v2。GORM AutoMigrate 对索引
// 只增不删、且**同名索引不做列比对与重建**（实测：表中已有同名索引时直接跳过），故旧索引必须在此显式移除——
// 否则它会作为无用索引长期拖慢 alert_event 写入（每次合并都要回写 to_status）。
//
// 安全：只 DROP INDEX，不删列、不动任何行（to_status 列保留，历史行仍可读）。HasIndex 判存保证幂等：
// 新库 / 已升级库上该索引不存在时静默跳过；万一旧版本二进制回退运行，AutoMigrate 会按旧模型重建它，无数据风险。
func dropLegacyAlertDedupIndex(db *gorm.DB) error {
	const legacyIndex = "idx_alert_event_dedup"
	if !db.Migrator().HasIndex(&model.AlertEvent{}, legacyIndex) {
		return nil
	}
	if err := db.Migrator().DropIndex(&model.AlertEvent{}, legacyIndex); err != nil {
		return fmt.Errorf("移除旧告警收敛索引 %s 失败: %w", legacyIndex, err)
	}
	slog.Info("已移除旧告警收敛索引（收敛键不再含 to_status）", "索引", legacyIndex)
	return nil
}

// backfillLegacyAlertStatus 把加列前的存量告警历史行（status 为空串 / NULL）回填为终态 resolved。
// 仅标准 SQL（无方言函数），保 Postgres 可移植；命中 0 行时静默返回（幂等）。
func backfillLegacyAlertStatus(db *gorm.DB) error {
	res := db.Model(&model.AlertEvent{}).
		Where("status = ? OR status IS NULL", "").
		Update("status", model.AlertEventStatusResolved)
	if res.Error != nil {
		return fmt.Errorf("回填存量告警状态失败: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		slog.Info("回填存量告警历史为终态 resolved", "行数", res.RowsAffected)
	}
	return nil
}

// Close 关闭底层连接池；db 为 nil 时安全略过（归档库不可达降级时连接为 nil，FR-151）。
func Close(db *gorm.DB) {
	if db == nil {
		return
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// newDialector 根据配置中的 driver 字段返回对应的 GORM Dialector。
// sqlite 使用纯 Go 实现（glebarez/modernc），无需 CGO。
//
// 这里也是「连接等待防护」的装配点：包装层经 dialector 的 Conn 字段注入，而**不是**在 gorm.Open
// 之后直接赋 `db.ConnPool`。原因（实测，见 connpool_timeout_test.go 的 TestConnPoolCoversAllGORMPaths）：
// gorm.Open 末尾会把 db.ConnPool 固化进 db.Statement.ConnPool，而后续 getInstance() 取的是**语句级**
// 那个字段；事后只赋 db.ConnPool 会让业务语句仍走原池（实测包装层命中 0 次），防护形同虚设且无迹象。
// 经 dialector.Conn 注入则由 gorm 自己在 Initialize 里写入两个字段，全路径（含 Session 派生、
// 嵌套 savepoint、AutoMigrate）都被覆盖。
func newDialector(cfg config.DatabaseConfig) (gorm.Dialector, error) {
	switch cfg.Driver {
	case "mysql":
		// 保留 mysql dialector 的版本探测（Initialize 内 SELECT VERSION()）：它决定 DDL 生成策略
		// （RENAME COLUMN / RETURNING / datetime 精度等），跳过会悄悄改变 AutoMigrate 行为。
		conn, err := openPool(cfg.Driver, cfg.DSN, cfg)
		if err != nil {
			return nil, err
		}
		return mysql.New(mysql.Config{DSN: cfg.DSN, Conn: conn}), nil
	case "sqlite":
		dsn := applySQLiteTxLock(applySQLitePragmas(cfg.DSN))
		conn, err := openPool(cfg.Driver, dsn, cfg)
		if err != nil {
			return nil, err
		}
		return &sqlite.Dialector{DriverName: sqlite.DriverName, DSN: dsn, Conn: conn}, nil
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q（支持 mysql / sqlite）", cfg.Driver)
	}
}

// openPool 打开原生连接池、按配置设好池参数，再包上连接等待防护。
//
// 池参数在这里设（而不是 gorm.Open 之后经 db.DB()）：此时池只由本函数持有，
// 设完再交给 gorm 装配，不存在「漏设」窗口。
func openPool(driver, dsn string, cfg config.DatabaseConfig) (gorm.ConnPool, error) {
	sqlDB, err := sql.Open(sqlDriverName(driver), dsn)
	if err != nil {
		return nil, fmt.Errorf("打开数据库连接失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSec) * time.Second)
	return newTimeoutConnPool(sqlDB, cfg.CallTimeoutMs, cfg.TxTimeoutMs), nil
}

// sqlDriverName 把内部 driver 名映射为 database/sql 注册的驱动名。
// sqlite 走 glebarez 纯 Go 实现（底层 modernc，无 CGO），其注册名为 sqlite.DriverName。
func sqlDriverName(driver string) string {
	if driver == "mysql" {
		return "mysql"
	}
	return sqlite.DriverName
}

// applySQLitePragmas 为 sqlite DSN 追加崩溃韧性与并发相关的 pragma。
//
// 缺陷背景：sqlite 默认回滚日志（DELETE）模式下，进程被强杀（如 Ctrl+C）会在写入事务
// 中途留下热 beacon.db-journal；下次启动 sqlite 检测到热日志后必须先写主库做回滚恢复，
// 一旦该写因文件暂不可写（杀软占用、句柄未释放等）失败，即返回 SQLITE_READONLY(8)
// 直接卡死控制面启动——AutoMigrate 仅在读已有 schema 时不写库，回填 UPDATE 作为首个
// 写入触发该只读恢复失败。
//
// 修复：切到 WAL 后主库始终一致，强杀只留 -wal/-shm 旁车，下次启动自动重放、不再触发
// 只读恢复。WAL 为持久化设置（写入库头），仅对本地 sqlite 生效；mysql 路径不受影响。
// 若 DSN 已显式指定 journal_mode（含 file: URI 形式），尊重用户配置不再覆盖。
//
// busy_timeout(5000)：多连接并发时，遇到**写锁竞争**的操作等待最多 5s 而非立即返回 SQLITE_BUSY。
//
// 取值与 `call-timeout-ms` 的关系（实测，勿让二者再回到同值）：两者取同值时，一次写锁竞争的
// 归属是**竞态**——谁先到点取决于计时抖动，同一条语句这次报 SQLITE_BUSY、下次报 DB_WAIT_TIMEOUT，
// 故障指纹不稳定、无法据报错分流。实测：预算 300ms + busy_timeout 5000ms 下，写冲突稳定等满
// SQLite 的 5s 才以 SQLITE_BUSY 返回（见 TestBusyLockNotMisattributedToPoolExhaustion）。
// 故 busy_timeout 应**显著小于** `call-timeout-ms`，让「等文件锁」总能在本层预算之前自行了结，
// 从而两条超时语义各归其位。默认 DSN 的 busy_timeout(5000) 与默认预算 5000ms 恰好同值，
// 是这一竞态的临界形态——归因侧已由 wrapTimeout 的 errors.Is(DeadlineExceeded) 判据兜住，
// 但取值本身仍应在部署时拉开（运维真源在 config.yml / 环境）。
//
// 注意其能力边界（实测，见 applySQLiteTxLock 的说明）：busy_timeout 只覆盖「等待写锁释放」这一类竞争，
// **无法**覆盖 DEFERRED 事务把读锁升级为写锁时的失败——那属于快照已陈旧、重试也无意义的情形，
// SQLite 会立即返回 SQLITE_BUSY(5) / BUSY_SNAPSHOT(517) 而不等待。故多连接部署必须同时注入
// _txlock=immediate（见 applySQLiteTxLock），二者缺一不可。
func applySQLitePragmas(dsn string) string {
	if strings.Contains(strings.ToLower(dsn), "journal_mode") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
}

// applySQLiteTxLock 为 sqlite DSN 追加 `_txlock=immediate`，把「读后写」事务的写锁竞争前移到
// BEGIN 处等待，而不是在第一条写语句上直接失败。
//
// 缺陷背景（实测，sqlite 3.41.2 / glebarez v1.11.0）：sqlite 默认的 DEFERRED 事务在 BEGIN 时
// 不取任何锁，读语句只拿读锁，直到第一条写语句才需要把读锁升级为写锁。**WAL 模式下该升级不可等待**：
// 若另一事务在本事务读之后已修改过该页，升级立即返回 SQLITE_BUSY(5) / BUSY_SNAPSHOT(517)，
// **busy_timeout 完全不生效**（SQLite 不会为「升级失败」重试，因为重试也无法让它看到更新的快照）。
//
// 实测（MaxOpenConns=4，8 并发各跑「先读后写」事务共 120 个）：
//   - 默认 DEFERRED：失败 104/120，全部为 SQLITE_BUSY；
//   - 加 _txlock=immediate：失败 0/120。
//
// 代价（也已实测）：immediate 让**每个**事务在 BEGIN 处即取写者位，故并发的只读事务不再互相并行——
// 长只读事务（2s）期间普通写事务从 4ms 变为 ~1.95s（等待而非失败）。控制面只读事务普遍短小，
// 故该代价可接受；这也是 MaxOpenConns > 1 能够安全放开的前提。
//
// 若 DSN 已显式指定 _txlock（含 file: URI 形式），尊重用户配置不再覆盖。
func applySQLiteTxLock(dsn string) string {
	if strings.Contains(strings.ToLower(dsn), "_txlock") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "_txlock=immediate"
}
