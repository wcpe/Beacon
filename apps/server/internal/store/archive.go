package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	sqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/config"
)

// 归档目标形态（FR-151，见 ADR-0066）：同实例第二库 / 独立库。
const (
	// ArchiveModeSameInstance 同实例模式：复用主库连接参数、仅替换库名（archive.dsn 留空）。
	ArchiveModeSameInstance = "same-instance"
	// ArchiveModeExternal 独立库模式：归档写入与冷查询全部路由 archive.dsn。
	ArchiveModeExternal = "external"
)

// ArchiveInfo 是归档目标的可展示元信息（供 overview，DSN 一律脱敏）。
// 即便连通性检查失败也会返回（供降级态展示 target + reachable=false）。
type ArchiveInfo struct {
	// same-instance / external
	Mode string
	// 归档库名（同实例=archive.database；独立库=从 DSN 解析，解析不出为空）
	Database string
	// 脱敏后的 DSN（去凭据，仅供展示）
	DSNMasked string
}

// OpenArchive 建立归档库连接（FR-151，见 ADR-0066）：复用 newDialector 建第二个独立 *gorm.DB。
//
// 同实例模式（archive.dsn 留空）：复用主库连接参数——mysql 仅把 database 替换为 archive.database、
// sqlite 用同目录第二个 .db 文件；独立库模式（archive.dsn 非空）：直接用该 DSN。
//
// 启动做连通性检查（Ping）：不可达时返回非 nil 错误但 ArchiveInfo 仍完整——由上层 WARN + 归档能力降级不可用、
// 绝不阻断控制面启动（ADR-0066 决策 4）。连接成功时 db 非 nil。
func OpenArchive(main config.DatabaseConfig, arc config.ArchiveConfig) (*gorm.DB, ArchiveInfo, error) {
	archiveDB := resolveArchiveDatabaseConfig(main, arc)
	info := ArchiveInfo{
		Mode:      archiveTargetMode(arc),
		Database:  archiveDatabaseName(main.Driver, arc, archiveDB.DSN),
		DSNMasked: maskDSN(archiveDB.Driver, archiveDB.DSN),
	}

	dialector, err := newDialector(archiveDB)
	if err != nil {
		return nil, info, err
	}
	db, err := gorm.Open(dialector, &gorm.Config{
		Logger:         newGormLogger(),
		TranslateError: true,
		NowFunc:        func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		return nil, info, fmt.Errorf("连接归档库失败: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, info, fmt.Errorf("获取归档库底层连接池失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(main.MaxOpenConns)
	sqlDB.SetMaxIdleConns(main.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(main.ConnMaxLifetimeSec) * time.Second)
	if err := sqlDB.Ping(); err != nil {
		return nil, info, fmt.Errorf("归档库 Ping 失败: %w", err)
	}
	// 归档表按同一套 model 由归档器写入前经 GORM 迁移建表（日表经 EnsureDailyTable、单表按需建），
	// 故此处不做 AutoMigrate；任务表 archive_job/archive_job_item 落热库（见 store.Open）。
	return db, info, nil
}

// archiveTargetMode 判定归档目标形态。
func archiveTargetMode(arc config.ArchiveConfig) string {
	if strings.TrimSpace(arc.DSN) != "" {
		return ArchiveModeExternal
	}
	return ArchiveModeSameInstance
}

// resolveArchiveDatabaseConfig 把主库配置与归档配置解析为归档连接用的 DatabaseConfig。
// 独立库模式直接用 archive.dsn；同实例模式按驱动派生第二库 DSN（连接池参数沿用主库）。
func resolveArchiveDatabaseConfig(main config.DatabaseConfig, arc config.ArchiveConfig) config.DatabaseConfig {
	out := main // 复用主库连接池参数（MaxOpenConns 等）
	if strings.TrimSpace(arc.DSN) != "" {
		out.DSN = arc.DSN
		return out
	}
	database := archiveDatabaseOrDefault(arc)
	switch main.Driver {
	case "mysql":
		out.DSN = replaceMySQLDBName(main.DSN, database)
	case "sqlite":
		out.DSN = deriveSQLiteArchiveDSN(main.DSN, database)
	}
	return out
}

// archiveDatabaseOrDefault 取归档库名，留空回退默认 beacon_archive。
func archiveDatabaseOrDefault(arc config.ArchiveConfig) string {
	if strings.TrimSpace(arc.Database) == "" {
		return "beacon_archive"
	}
	return arc.Database
}

// replaceMySQLDBName 复用主库 mysql DSN 的全部连接参数、仅替换 database 名（同实例第二库）。
// 解析失败时回退为「原 DSN 尾部库名整体替换」的保守处理（极少见，DSN 由运维配置且主库已连通）。
func replaceMySQLDBName(mainDSN, database string) string {
	cfg, err := sqldriver.ParseDSN(mainDSN)
	if err != nil {
		return mainDSN
	}
	cfg.DBName = database
	return cfg.FormatDSN()
}

// deriveSQLiteArchiveDSN 同实例 sqlite 归档 = 同目录下以库名命名的第二个 .db 文件。
// 主库 DSN 支持 "beacon.db" 或 "file:beacon.db?params" 两种形态：取其文件路径所在目录，拼 <database>.db。
func deriveSQLiteArchiveDSN(mainDSN, database string) string {
	path := strings.TrimPrefix(mainDSN, "file:")
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	dir := filepath.Dir(path)
	return filepath.Join(dir, database+".db")
}

// archiveDatabaseName 取归档库展示名：同实例=archive.database；独立库 mysql=从 DSN 解析 DBName，解析不出为空。
func archiveDatabaseName(driver string, arc config.ArchiveConfig, resolvedDSN string) string {
	if strings.TrimSpace(arc.DSN) == "" {
		return archiveDatabaseOrDefault(arc)
	}
	if driver == "mysql" {
		if cfg, err := sqldriver.ParseDSN(resolvedDSN); err == nil {
			return cfg.DBName
		}
	}
	return ""
}

// maskDSN 脱敏 DSN 供展示（去凭据）：mysql 走 ParseDSN 打码口令；sqlite 为文件路径无凭据、原样返回。
func maskDSN(driver, dsn string) string {
	if driver == "mysql" {
		if cfg, err := sqldriver.ParseDSN(dsn); err == nil {
			if cfg.Passwd != "" {
				cfg.Passwd = "***"
			}
			return cfg.FormatDSN()
		}
	}
	return dsn
}
