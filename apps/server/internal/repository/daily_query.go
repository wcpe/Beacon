package repository

import "time"

// utcDaysBetween 返回 [fromMs, toMs] 毫秒区间覆盖的 UTC 日零点序列（含端点日，升序），
// 供日表查询侧按日期枚举候选表名（跨日并表）。from > to 返回空。
func utcDaysBetween(fromMs, toMs int64) []time.Time {
	if fromMs > toMs {
		return nil
	}
	days := make([]time.Time, 0, 4)
	for day := utcDayStart(fromMs); !day.After(utcDayStart(toMs)); day = day.AddDate(0, 0, 1) {
		days = append(days, day)
	}
	return days
}
