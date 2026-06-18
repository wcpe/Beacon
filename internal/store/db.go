// Package store 是基础设施层：GORM 连接、连接池与表结构迁移。
package store

import (
	"fmt"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"beacon/internal/config"
	"beacon/internal/model"
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
		&model.FileObject{},
		&model.FileRevision{},
		&model.FileOverrideSet{},
		&model.FileOverrideSetRevision{},
		&model.ZoneAssignment{},
		&model.ServerDrain{},
		&model.AuditLog{},
	); err != nil {
		return nil, fmt.Errorf("自动迁移表结构失败: %w", err)
	}
	return db, nil
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
