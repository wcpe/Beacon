// Package update 实现控制面在线自更新核心（FR-97，见 ADR-0044）：按渠道查 GitHub Release、
// 下载本平台资产、SHA256 校验、原子落位 launcher 约定的 pending 路径、以退出码交还 launcher 换二进制重启。
// 仅控制面单二进制（含 go:embed 前端整体替换），不涉 agent jar。
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// devVersion 是未经打包构建（直接 go run）时的版本哨兵，视为未知、不参与更新比较 / 不提示更新。
const devVersion = "dev"

// semver 是解析后的最小语义版本（仅支持 vX.Y.Z 三段数字，FR-117/ADR-0052：去 rc 预发布段，按版本号判新）。
type semver struct {
	major, minor, patch int
}

// parseSemver 解析 vX.Y.Z 或 X.Y.Z（容忍可选前导 "v"）。
// ADR-0052：渠道收敛为正式 / 预发布、按 X.Y.Z 判新，**不再支持任何预发布后缀**（含 -rc.N）；
// 带后缀一律视为非法 → 解析失败，调用方据此当未知处理、不误判更新。
func parseSemver(s string) (semver, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(s), "v")
	if raw == "" {
		return semver{}, fmt.Errorf("版本号为空")
	}
	// 任何 '-' 后缀（预发布 / build 元数据）均拒：滚动预发布与正式版都用纯 X.Y.Z。
	if strings.ContainsAny(raw, "-+") {
		return semver{}, fmt.Errorf("版本号不支持后缀，须为纯 X.Y.Z，实际 %q", s)
	}

	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("版本号须为 X.Y.Z 三段，实际 %q", s)
	}
	var v semver
	var err error
	if v.major, err = parseNonNegInt(parts[0]); err != nil {
		return semver{}, fmt.Errorf("主版本号非法: %q", s)
	}
	if v.minor, err = parseNonNegInt(parts[1]); err != nil {
		return semver{}, fmt.Errorf("次版本号非法: %q", s)
	}
	if v.patch, err = parseNonNegInt(parts[2]); err != nil {
		return semver{}, fmt.Errorf("补丁版本号非法: %q", s)
	}
	return v, nil
}

// parseNonNegInt 解析非负十进制整数（拒绝符号 / 空串 / 非数字），用于版本各段。
func parseNonNegInt(s string) (int, error) {
	if s == "" {
		return 0, fmt.Errorf("空段")
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("非负整数解析失败")
	}
	return n, nil
}

// compareSemver 比较两个版本：a<b 返回 -1，a==b 返回 0，a>b 返回 1。
// 序：依次比主、次、补丁数字；三段全等即相等（同 X.Y.Z，ADR-0052 不判更新）。
func compareSemver(a, b semver) int {
	if c := cmpInt(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpInt(a.minor, b.minor); c != 0 {
		return c
	}
	return cmpInt(a.patch, b.patch)
}

// cmpInt 返回 a 与 b 的符号比较（-1/0/1）。
func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// IsNewer 判断远端版本 remote 是否严格高于当前版本 current（据此决定是否提示更新）。
// current 为 dev 哨兵或任一方解析失败 → 视为「无可用更新」（不提示、不误判），并返回解析错误供日志。
func IsNewer(current, remote string) (bool, error) {
	if strings.TrimSpace(current) == devVersion {
		// dev 构建：版本未知，不参与比较、不提示更新。
		return false, nil
	}
	cur, err := parseSemver(current)
	if err != nil {
		return false, fmt.Errorf("当前版本解析失败: %w", err)
	}
	rem, err := parseSemver(remote)
	if err != nil {
		return false, fmt.Errorf("远端版本解析失败: %w", err)
	}
	return compareSemver(rem, cur) > 0, nil
}
