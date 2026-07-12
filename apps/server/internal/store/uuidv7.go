package store

import "strconv"

// TimeMsFromUUIDv7 从 UUIDv7 文本的高 48 位解析 Unix 毫秒时间戳（RFC 9562：前 6 字节大端即毫秒）。
//
// 全域日表以「行主键 UUIDv7 内嵌时间所在 UTC 日」路由物理表（见 v2-connection-message-storage.md §3.1）：
// 连接明细 conn_id、消息 message_id 均由 agent 生成 UUIDv7，控制面据此免日期提示按 ID 直定日表。
// 手写最小解析、不引第三方 uuid 依赖：跳过连字符取前 12 个十六进制字符（= 6 字节 = 48 位）按大端解析。
// 返回 (毫秒, true)；字符不足 12 位或含非十六进制字符返回 (0, false)。
func TimeMsFromUUIDv7(id string) (int64, bool) {
	hexDigits := make([]byte, 0, 12)
	for i := 0; i < len(id) && len(hexDigits) < 12; i++ {
		c := id[i]
		if c == '-' {
			continue
		}
		hexDigits = append(hexDigits, c)
	}
	if len(hexDigits) < 12 {
		return 0, false
	}
	ms, err := strconv.ParseInt(string(hexDigits), 16, 64)
	if err != nil {
		return 0, false
	}
	return ms, true
}
