// Package store 是基础设施层：GORM 连接、连接池与表结构迁移。
package store

import (
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
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSec) * time.Second)

	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("数据库 Ping 失败: %w", err)
	}

	// AutoMigrate 仅用于建表/补字段；DDL 由 GORM 按方言生成，业务零方言绑定。
	// instance 镜像表 MVP 不建（注册/健康运行态以内存为准）。
	if err := db.AutoMigrate(
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
		&model.DeliveryBlob{},
		// 配置灰度冻结渲染工件（FR-171，见 ADR-0071）：config_change 项 per-(项, 目标) 渲染 sha 无法落
		// change_order_item，单独冻结持久化，供 manifest / 下载授权 / 清理护栏读取，不再重渲染
		&model.DeliveryConfigArtifact{},
	); err != nil {
		return nil, fmt.Errorf("自动迁移表结构失败: %w", err)
	}
	if err := backfillStableBusinessNames(db); err != nil {
		return nil, err
	}
	if err := dropLegacyDisplayNameUniqueIndexes(db); err != nil {
		return nil, err
	}
	if err := backfillLobbyClusters(db); err != nil {
		return nil, err
	}
	if err := backfillLegacyIdentityBindingSources(db); err != nil {
		return nil, err
	}
	if err := backfillMachineRegisteredIdentitySources(db); err != nil {
		return nil, err
	}

	// 告警处理状态存量回填（FR-157，见 ADR-0064）：加列前的 append-only 历史行属过去已闭事件，
	// 回填为终态 resolved，避免把当前健康 activeAlerts 撑爆。幂等一次性——只命中空串 / NULL 的旧行，
	// 新行由应用层显式写 open、不会被回填。
	if err := backfillLegacyAlertStatus(db); err != nil {
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
func newDialector(cfg config.DatabaseConfig) (gorm.Dialector, error) {
	switch cfg.Driver {
	case "mysql":
		return mysql.Open(cfg.DSN), nil
	case "sqlite":
		return sqlite.Open(applySQLitePragmas(cfg.DSN)), nil
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q（支持 mysql / sqlite）", cfg.Driver)
	}
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
// busy_timeout(5000)：多连接并发时写操作遇锁等待最多 5s 而非立即返回 SQLITE_BUSY 失败，
// 配合 WAL 的并发读 + 单写模型，使 MaxOpenConns>1 时写操作不会因瞬态锁竞争失败。
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
