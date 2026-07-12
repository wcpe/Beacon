// Package store 是基础设施层：GORM 连接、连接池与表结构迁移。
package store

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
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
		&model.ConfigGray{},
		&model.FileObject{},
		&model.FileRevision{},
		&model.FileOverrideSet{},
		&model.FileOverrideSetRevision{},
		&model.ZoneAssignment{},
		&model.ZoneDefaultEntry{},
		&model.ServerDrain{},
		&model.ServerOffline{},
		&model.AuditLog{},
		&model.AlertEvent{},
		&model.MetricSample{},
		&model.APIKey{},
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
		&model.Server{},
		&model.AgentIdentity{},
		&model.HealthWeightsRev{},
	); err != nil {
		return nil, fmt.Errorf("自动迁移表结构失败: %w", err)
	}

	// 告警处理状态存量回填（FR-157，见 ADR-0064）：加列前的 append-only 历史行属过去已闭事件，
	// 回填为终态 resolved，避免把当前健康 activeAlerts 撑爆。幂等一次性——只命中空串 / NULL 的旧行，
	// 新行由应用层显式写 open、不会被回填。
	if err := backfillLegacyAlertStatus(db); err != nil {
		return nil, err
	}
	return db, nil
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

// Close 关闭底层连接池。
func Close(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// newDialector 根据配置中的 driver 字段返回对应的 GORM Dialector。
func newDialector(cfg config.DatabaseConfig) (gorm.Dialector, error) {
	switch cfg.Driver {
	case "mysql":
		return mysql.Open(cfg.DSN), nil
	case "sqlite":
		return sqlite.Open(cfg.DSN), nil
	default:
		return nil, fmt.Errorf("不支持的数据库驱动 %q（支持 mysql / sqlite）", cfg.Driver)
	}
}
