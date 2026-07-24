// Package update 实现控制面在线自更新核心（FR-97/FR-119，见 ADR-0044/ADR-0053）：按稳定渠道查 GitHub Release、
// 下载本平台资产、SHA256 校验、原子落位 pending 路径，再由主进程单进程自替换（rename 让位三步）+ 自动回滚重启。
// 仅控制面单二进制（含 go:embed 前端整体替换），不涉 agent jar。
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// devVersion 是未经打包构建（直接 go run）时的版本哨兵，视为未知、不参与更新比较或提示。
const devVersion = "dev"

// semver 是用于稳定更新比较的正式版本 X.Y.Z。
type semver struct {
	major int
	minor int
	patch int
}

// parseSemver 解析正式版本 X.Y.Z，可容忍运行时版本带前导 v。
// 不接受预发布、构建元数据、缺段、空白或数字段前导零。
func parseSemver(value string) (semver, error) {
	raw := strings.TrimPrefix(value, "v")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return semver{}, fmt.Errorf("版本号须为 X.Y.Z 三段，实际 %q", value)
	}

	major, err := parseVersionNumber(parts[0])
	if err != nil {
		return semver{}, fmt.Errorf("主版本号非法: %q", value)
	}
	minor, err := parseVersionNumber(parts[1])
	if err != nil {
		return semver{}, fmt.Errorf("次版本号非法: %q", value)
	}
	patch, err := parseVersionNumber(parts[2])
	if err != nil {
		return semver{}, fmt.Errorf("补丁版本号非法: %q", value)
	}
	return semver{major: major, minor: minor, patch: patch}, nil
}

// parseVersionNumber 解析无前导零的非负十进制版本段；数字 0 本身合法。
func parseVersionNumber(value string) (int, error) {
	if value == "" || len(value) > 1 && value[0] == '0' {
		return 0, fmt.Errorf("版本段为空或含前导零")
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, fmt.Errorf("版本段包含非数字字符")
		}
	}
	number, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("版本段超出整数范围: %w", err)
	}
	return number, nil
}

// compareSemver 比较两个正式版本：a<b 返回 -1、a==b 返回 0、a>b 返回 1。
func compareSemver(a, b semver) int {
	if result := cmpInt(a.major, b.major); result != 0 {
		return result
	}
	if result := cmpInt(a.minor, b.minor); result != 0 {
		return result
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

// IsNewer 判断远端正式版本是否严格高于当前正式版本。
// 当前为 dev 哨兵时版本未知，不提示更新；任一正式版本非法时返回解析错误。
func IsNewer(current, remote string) (bool, error) {
	if current == devVersion {
		return false, nil
	}
	currentVersion, err := parseSemver(current)
	if err != nil {
		return false, fmt.Errorf("当前版本解析失败: %w", err)
	}
	remoteVersion, err := parseSemver(remote)
	if err != nil {
		return false, fmt.Errorf("远端版本解析失败: %w", err)
	}
	return compareSemver(remoteVersion, currentVersion) > 0, nil
}
