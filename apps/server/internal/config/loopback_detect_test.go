package config

import "testing"

// TestIsLoopbackListenAddrCoversExpandedForms 固化回环判定的边界行为（实测自本仓实现）。
//
// **为什么这些少见写法被刻意判为非回环、且不应"修好"**：本判定的唯一用途是
// 「非回环 + 无凭据 → 记一条 WARN」，即它误导的两个方向后果严重不对称：
//
//   - **把真实回环误判成非回环**（本用例覆盖的那些写法）→ 只是多打一条 WARN，
//     运维看一眼就知道是误报，无实际危害；
//   - **把实际对外的地址误判成回环** → 本该出现的 WARN 被静默吞掉，部署者以为
//     "默认安全"而实际已对外暴露组织拓扑与容量画像——这正是本次改动要消除的形态。
//
// 故实现只认三种**最常见且无歧义**的写法（127.0.0.1 / localhost / ::1），
// 其余一律保守视为非回环。若将来有人想"完善"成 `strings.HasPrefix(host, "127.")`
// 或补上 IPv6 展开形式，请先想清楚：那是在把判定推向上面第二个方向，
// 收益（少一条 WARN）远小于风险（可能漏报对外暴露）。
func TestIsLoopbackListenAddrCoversExpandedForms(t *testing.T) {
	// 被接受为回环：常见写法，含大小写与首尾空白（TrimSpace + ToLower 归一）。
	loopback := []string{
		"127.0.0.1:8849",
		"localhost:8849",
		"[::1]:8849",
		"LOCALHOST:8849",   // 大小写不敏感
		" 127.0.0.1:8849 ", // 首尾空白被归一
		"127.0.0.1:1",      // 端口无关
	}
	// 保守判为非回环：语义上确实指向本机，但写法少见；判错的代价只是一条多余 WARN。
	conservativelyNonLoopback := []string{
		"127.0.0.2:8849",          // 127.0.0.0/8 全段都是回环，但只用不上
		"127.1:8849",              // IPv4 简写形式
		"[0:0:0:0:0:0:0:1]:8849",  // ::1 的展开形式
		"[::ffff:127.0.0.1]:8849", // IPv4-mapped IPv6
		"localhost.:8849",         // 带根点的 FQDN 形式
	}
	for _, addr := range loopback {
		if !isLoopbackListenAddr(addr) {
			t.Errorf("isLoopbackListenAddr(%q) 应为回环——它是常见无歧义写法，误判会多打无害 WARN 但不符合预期", addr)
		}
	}
	for _, addr := range conservativelyNonLoopback {
		if isLoopbackListenAddr(addr) {
			t.Errorf("isLoopbackListenAddr(%q) 应为非回环（保守）——若实现改成接受这类写法，"+
				"任何以 127. 开头或形似 IPv6 的**对外**地址也可能被放宽判为回环，导致告警静默失效", addr)
		}
	}
}
