package merge

import (
	"crypto/md5"
	"encoding/hex"
	"sort"
	"strings"
)

// MD5Hex 返回字符串内容的小写十六进制 md5。
func MD5Hex(content string) string {
	sum := md5.Sum([]byte(content))
	return hex.EncodeToString(sum[:])
}

// OverallMD5 由各 dataId 的单 md5 计算有效配置整体 md5。
// 公式：md5( 按 dataId 字典序拼接 (dataId + ":" + 单md5 + "\n") )。
// 把 dataId 名纳入哈希，避免集合 {A:x} 与 {B:x} 碰撞误判（见 ADR-0008）。
func OverallMD5(dataIDToMD5 map[string]string) string {
	ids := make([]string, 0, len(dataIDToMD5))
	for id := range dataIDToMD5 {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var b strings.Builder
	for _, id := range ids {
		b.WriteString(id)
		b.WriteString(":")
		b.WriteString(dataIDToMD5[id])
		b.WriteString("\n")
	}
	return MD5Hex(b.String())
}
