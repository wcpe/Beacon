package store

import (
	"crypto/rand"
	"strconv"
)

// hexDigitsLower 是十六进制小写字母表（生成 UUID 文本用，不引 fmt 以减少分配）。
const hexDigitsLower = "0123456789abcdef"

// TimeMsFromUUIDv7 从 UUIDv7 文本的高 48 位解析 Unix 毫秒时间戳（RFC 9562：前 6 字节大端即毫秒）。
//
// 全域日表以「行主键 UUIDv7 内嵌时间所在 UTC 日」路由物理表（见 v2-connection-message-storage.md §3.1）：
// 连接明细 conn_id、消息 message_id 均由 agent 生成 UUIDv7，控制面据此免日期提示按 ID 直定日表。
// 手写最小解析、不引第三方 uuid 依赖：跳过连字符取前 12 个十六进制字符（= 6 字节 = 48 位）按大端解析。
// 返回 (毫秒, true)；字符不足 12 位或含非十六进制字符返回 (0, false)。
func TimeMsFromUUIDv7(id string) (int64, bool) {
	hexDigits := uuidHexDigits(id, 12)
	if len(hexDigits) < 12 {
		return 0, false
	}
	ms, err := strconv.ParseInt(string(hexDigits), 16, 64)
	if err != nil {
		return 0, false
	}
	return ms, true
}

// TrustedTimeMsFromUUIDv7 解析 UUIDv7 文本并**校验 RFC 9562 结构**（版本号 = 第 7 字节高 4 位、
// 变体 = 第 9 字节高 2 位），返回 (毫秒, true)；结构不符（如 agent 误用 UUIDv4 随机 ID、
// 非 UUID 文本、长度不足）返回 (0, false)。
//
// 为什么要与 TimeMsFromUUIDv7 分开：后者只取前 48 位当时间戳，对随机 UUID（v4）也会「解析成功」，
// 于是随机位被当成 数千年后的毫秒——终态行会被写进永不进入查询窗口的垃圾日表而彻底不可见
// （真机实测：带业务 payload 的消息曾落进「由随机位推出的荒诞日期」日表）。凡「据 ID 时间定日表」的
// 写入 / 读取路由都必须先过本函数，确认时间可信；不可信时改由控制面接收时刻定日表（见
// message_repo.resolveMsgDay），保证任何一条消息的终态行都落在可查询的日表里。
func TrustedTimeMsFromUUIDv7(id string) (int64, bool) {
	hexDigits := uuidHexDigits(id, 32)
	if len(hexDigits) < 32 {
		return 0, false
	}
	// 结构校验（对齐 NewUUIDv7 的位写入）：版本号 7 落在跳过连字符后的第 13 个十六进制字符
	// （hexDigits[12]），变体 10xx 落在第 17 个十六进制字符（hexDigits[16]）。
	if hexDigits[12] != '7' {
		return 0, false
	}
	switch hexDigits[16] {
	case '8', '9', 'a', 'b', 'A', 'B':
	default:
		return 0, false
	}
	ms, err := strconv.ParseInt(string(hexDigits[:12]), 16, 64)
	if err != nil {
		return 0, false
	}
	return ms, true
}

// uuidHexDigits 提取 id 的前 n 个十六进制字符（跳过连字符）；不足 n 个返回已有长度。
func uuidHexDigits(id string, n int) []byte {
	out := make([]byte, 0, n)
	for i := 0; i < len(id) && len(out) < n; i++ {
		c := id[i]
		if c == '-' {
			continue
		}
		out = append(out, c)
	}
	return out
}

// NewUUIDv7 按给定 Unix 毫秒生成一个 UUIDv7 文本（RFC 9562：前 6 字节大端为毫秒，
// 第 7 字节高 4 位为版本号 7，第 9 字节高 2 位为变体 10，其余位随机）。
//
// 与 TimeMsFromUUIDv7 配套：控制面自造 ID 的日表（MCP 工具调用流水，FR-240 §3.2）用
// 「完成时刻的毫秒」生成主键，查询侧再由同一内嵌毫秒直定日表，免时间参数按 ID 直查。
// 手写最小实现、不引第三方 uuid 依赖（与解析侧同口径）；随机源不可用时退化为全零随机段
// （仍保证时间可解析与主键唯一性由毫秒 + 进程内计数无关——极端场景下由日表主键冲突兜底）。
func NewUUIDv7(ms int64) string {
	var b [16]byte
	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)
	// 随机段：crypto/rand 读失败时不改写（保持零值），仍返回合法形态的 ID。
	_, _ = rand.Read(b[6:])
	b[6] = (b[6] & 0x0f) | 0x70 // 版本 7
	b[8] = (b[8] & 0x3f) | 0x80 // 变体 10xx

	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexDigitsLower[v>>4], hexDigitsLower[v&0x0f])
	}
	return string(out)
}
