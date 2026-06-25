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

// semver 是解析后的最小语义版本（仅支持 vX.Y.Z 与可选 -rc.N 预发布段，FR-97 不引第三方 semver 库）。
type semver struct {
	major, minor, patch int
	// isPrerelease 标记是否带 -rc.N 预发布段；prerelease 版本低于同主次补丁的正式版。
	isPrerelease bool
	// rc 为预发布序号（-rc.N 的 N）；非预发布时无意义。
	rc int
}

// parseSemver 解析 vX.Y.Z 或 X.Y.Z，可带 -rc.N 预发布段。
// 容忍可选前导 "v"；格式非法返回错误（调用方据此当未知处理、不误判更新）。
func parseSemver(s string) (semver, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(s), "v")
	if raw == "" {
		return semver{}, fmt.Errorf("版本号为空")
	}

	// 切出预发布段：core[-pre]。仅支持 -rc.N 形式的预发布。
	core := raw
	var pre string
	if idx := strings.IndexByte(raw, '-'); idx >= 0 {
		core = raw[:idx]
		pre = raw[idx+1:]
	}

	parts := strings.Split(core, ".")
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

	if pre != "" {
		// 仅支持 rc.N（FR-97 渠道只产 stable=vX.Y.Z 与 rc=vX.Y.Z-rc.N，见 ADR-0044/0046）。
		rcStr, ok := strings.CutPrefix(pre, "rc.")
		if !ok {
			return semver{}, fmt.Errorf("预发布段仅支持 rc.N，实际 %q", s)
		}
		if v.rc, err = parseNonNegInt(rcStr); err != nil {
			return semver{}, fmt.Errorf("预发布序号非法: %q", s)
		}
		v.isPrerelease = true
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
// 序：先比主、次、补丁数字；主次补丁相同时，预发布 < 正式（1.2.0-rc.1 < 1.2.0）；
// 两者都预发布则按 rc 序号比（rc.1 < rc.2）。
func compareSemver(a, b semver) int {
	if c := cmpInt(a.major, b.major); c != 0 {
		return c
	}
	if c := cmpInt(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmpInt(a.patch, b.patch); c != 0 {
		return c
	}
	// 主次补丁相同：正式版高于预发布版。
	switch {
	case a.isPrerelease && !b.isPrerelease:
		return -1
	case !a.isPrerelease && b.isPrerelease:
		return 1
	case a.isPrerelease && b.isPrerelease:
		return cmpInt(a.rc, b.rc)
	default:
		return 0
	}
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
