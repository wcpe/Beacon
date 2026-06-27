// Package update 实现控制面在线自更新核心（FR-97/FR-119，见 ADR-0044/ADR-0053）：按渠道查 GitHub Release、
// 下载本平台资产、SHA256 校验、原子落位 pending 路径，再由主进程单进程自替换（rename 让位三步）+ 自动回滚重启。
// 仅控制面单二进制（含 go:embed 前端整体替换），不涉 agent jar。
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// devVersion 是未经打包构建（直接 go run）时的版本哨兵，视为未知、不参与更新比较 / 不提示更新。
const devVersion = "dev"

// semver 是解析后的语义版本：X.Y.Z 基线 + 可选预发布标识（如 dev.<短sha>）。
// 正式版 prerelease 为空；滚动预发布为 dev.<短sha>（FR-117/ADR-0055：每次 push sha 变、便于反复验证在线更新）。
type semver struct {
	major, minor, patch int
	prerelease          string // '-' 后的预发布标识（如 "dev.715989a"）；空=正式版
}

// parseSemver 解析 X.Y.Z 或 X.Y.Z-<prerelease>（容忍可选前导 "v"）。
// 支持滚动预发布的 -dev.<sha> 预发布段（ADR-0055，取代 ADR-0052「不支持任何后缀」）；不支持 build 元数据 '+'。
func parseSemver(s string) (semver, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(s), "v")
	if raw == "" {
		return semver{}, fmt.Errorf("版本号为空")
	}
	if strings.ContainsRune(raw, '+') {
		return semver{}, fmt.Errorf("不支持 build 元数据 '+'，实际 %q", s)
	}
	// 拆出预发布段（首个 '-' 后）：X.Y.Z[-prerelease]。
	core := raw
	var pre string
	if i := strings.IndexByte(raw, '-'); i >= 0 {
		core = raw[:i]
		pre = raw[i+1:]
		if pre == "" {
			return semver{}, fmt.Errorf("预发布段为空，实际 %q", s)
		}
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
	v.prerelease = pre
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

// compareBase 比较两版本的 X.Y.Z 基线（不含预发布标识）：a<b 返回 -1、a==b 返回 0、a>b 返回 1。
func compareBase(a, b semver) int {
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

// IsNewer 判断远端 remote 是否应提示为「有更新」相对当前 current（ADR-0055）。
// current 为 dev 哨兵或任一方解析失败 → 无更新（不提示、不误判），并返回解析错误供日志。
// 判定：先比 X.Y.Z 基线——远端基线高即更新、低即否；基线相同时**预发布标识不同即视为更新**，
// 使滚动预发布每次 push（dev.<sha> 变）都能被检测到、便于反复验证在线更新（dev→正式也算更新）。
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
	switch compareBase(rem, cur) {
	case 1:
		return true, nil
	case -1:
		return false, nil
	default:
		// 同 X.Y.Z 基线：预发布标识不同即更新（dev.<sha> 变可重复触发）。
		return rem.prerelease != cur.prerelease, nil
	}
}
