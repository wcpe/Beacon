package model

import "time"

// SoftDeleteSentinel 是软删字段 deleted_at 的"未删"哨兵值（固定 1970-01-01 UTC，非 NULL）。
// 纳入唯一键后：未删记录哨兵相同 → 唯一约束真正生效；软删填真实时间 → 允许同标识重建。
// 用固定哨兵而非 NULL，是因为 MySQL/Postgres 唯一索引都不把多个 NULL 视为冲突（见 ADR-0008）。
var SoftDeleteSentinel = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)

// IsDeleted 判断 deleted_at 是否表示已软删（非哨兵值即已删）。
func IsDeleted(deletedAt time.Time) bool {
	return !deletedAt.Equal(SoftDeleteSentinel)
}
