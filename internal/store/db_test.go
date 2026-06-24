package store

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/internal/config"
	"github.com/wcpe/Beacon/internal/model"
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
