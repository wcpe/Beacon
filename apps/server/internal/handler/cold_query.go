package handler

import (
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/service"
)

// 冷查询（includeArchived）跨域参数契约（FR-152，spec §4.4，见 ADR-0066 决策 5）：
// 默认只查热库、行为不变；includeArchived=true 时跨热 / 冷并表，强制携带时间范围且跨度 ≤
// archive.cold-query-max-days（默认 31，违反 400 且文案明示边界，ADR-0057 脱敏——仅含天数无凭据）。

// coldQueryRequested 解析 includeArchived query 参数（仅字面 "true" 视为开启，其余按默认热查）。
func coldQueryRequested(q url.Values) bool {
	return q.Get("includeArchived") == "true"
}

// coldQueryMaxDays 读取冷查询单次最大时间跨度（天，运维设置热更、默认 31）。
func coldQueryMaxDays(settings *service.SettingsService) int {
	return settings.GetInt(service.SettingArchiveColdQueryMaxDays)
}

// validateColdQueryRange 校验冷查询强制时间范围（spec §4.4）：from/to 必填（>0）且有序、
// 跨度 ≤ maxDays 天；违反返回明示边界的 400。
func validateColdQueryRange(fromMs, toMs int64, maxDays int) error {
	if fromMs <= 0 || toMs <= 0 || fromMs > toMs {
		return apperr.New(http.StatusBadRequest, "COLD_QUERY_RANGE_REQUIRED",
			"启用 includeArchived 冷查询必须携带有效时间范围 from/to")
	}
	maxMs := int64(maxDays) * 24 * int64(time.Hour/time.Millisecond)
	if toMs-fromMs > maxMs {
		return apperr.New(http.StatusBadRequest, "COLD_QUERY_RANGE_TOO_WIDE",
			fmt.Sprintf("冷查询时间跨度不得超过 %d 天（archive.cold-query-max-days）", maxDays))
	}
	return nil
}
