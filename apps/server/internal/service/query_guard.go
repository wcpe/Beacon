package service

import (
	"strconv"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
)

// 连接明细与消息元数据管理面查询的共享防护与游标分页参数（spec §4.3）。
const (
	// maxConnMsgRangeMs 条件查询时间范围上限（168h / 8 张日表，spec §3.1/§4.3）。
	maxConnMsgRangeMs = 168 * int64(time.Hour/time.Millisecond)
	// defaultQueryLimit / maxQueryLimit 游标分页大小默认与上限（spec §4.3：默认 50 / 上限 200）。
	defaultQueryLimit = 50
	maxQueryLimit     = 200
)

// validateRangeFilter 校验条件查询防护（无精确 ID 时）：须显式有序时间范围且跨度 ≤168h。
// 选择性过滤（serverId/playerUuid）可选——无 selector 时允许「时间窗内全局近期」列表（管理台默认进页）。
// 仍禁止无时间窗的全表扫；超 168h 或倒置范围一律 ErrQueryGuardViolation（400）。
func validateRangeFilter(_ bool, fromMs, toMs int64) error {
	if fromMs <= 0 || toMs <= 0 || fromMs > toMs {
		return apperr.ErrQueryGuardViolation
	}
	if toMs-fromMs > maxConnMsgRangeMs {
		return apperr.ErrQueryGuardViolation
	}
	return nil
}

// validateColdSelector 校验冷查询（includeArchived）的选择性防护：须至少一个选择性过滤
// （serverId/playerUuid）+ 有序时间范围（from/to 必填），任一不满足即 400（spec §4.4）。
// 与 validateRangeFilter 的区别：冷查询时间跨度上限为 archive.cold-query-max-days（可达 31 天 >168h），
// 故跨度上限由 handler 按设置校验，此处只守选择性与有序范围、不套 168h 上限。
func validateColdSelector(hasSelector bool, fromMs, toMs int64) error {
	if !hasSelector {
		return apperr.ErrQueryGuardViolation
	}
	if fromMs <= 0 || toMs <= 0 || fromMs > toMs {
		return apperr.ErrQueryGuardViolation
	}
	return nil
}

// clampLimit 规整游标分页大小：≤0 取默认 50，>200 收敛到 200（spec §4.3 分页强制）。
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultQueryLimit
	}
	if limit > maxQueryLimit {
		return maxQueryLimit
	}
	return limit
}

// clampOffset 规整游标偏移：负值归 0。
func clampOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}

// nextCursorOf 由本页 offset/limit 与是否还有下一页算下一游标（数值 offset 字符串）；无更多返回空串（上层映射 null）。
func nextCursorOf(offset, limit int, hasMore bool) string {
	if !hasMore {
		return ""
	}
	return strconv.Itoa(offset + limit)
}
