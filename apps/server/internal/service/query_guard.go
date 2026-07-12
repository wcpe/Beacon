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

// validateRangeFilter 校验条件查询防护（无精确 ID 时）：须至少一个选择性过滤（serverId/playerUuid）
// + 显式有序时间范围且跨度 ≤168h，任一不满足即 ErrQueryGuardViolation（400，spec §4.3）。
func validateRangeFilter(hasSelector bool, fromMs, toMs int64) error {
	if !hasSelector {
		return apperr.ErrQueryGuardViolation
	}
	if fromMs <= 0 || toMs <= 0 || fromMs > toMs {
		return apperr.ErrQueryGuardViolation
	}
	if toMs-fromMs > maxConnMsgRangeMs {
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
