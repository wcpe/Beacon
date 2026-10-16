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

// semver 是用于更新比较的版本：三段数字 + 可选候选项，排序遵循 SemVer 2.0.0 第 11 条。
type semver struct {
	major int
	minor int
	patch int
	// prerelease 是候选后缀（如 "rc.2"）；空表示正式版本。
	// 按 SemVer：带候选后缀者低于同三段数字的正式版本（1.5.0-rc.2 < 1.5.0）。
	prerelease string
}

// parseRuntimeVersion 解析运行时版本 "X.Y.Z" 或 "X.Y.Z-<候选>"，可容忍前导 v。
//
// 运行时版本必须接受候选后缀：候选构建注入的就是 "1.5.0-rc.2"（ADR-0092），
// 若拒绝则更新判新会解析失败并静默退化为「无更新」。
//
// 候选段同样做字符集校验：只允许点分隔的非空字母数字段（SemVer 2.0.0 第 9 条），
// 禁止构建元数据（+）、空段与前导零。不校验会让 "1.5.0-rc.+1" 这类串被当作合法候选，
// 且其中含 `+` 的部分会被传进比较器（`+` 在核心三段被拒、在候选段却放行是不一致的）。
//
// 不接受缺段、空白或数字段前导零。
func parseRuntimeVersion(value string) (semver, error) {
	raw := strings.TrimPrefix(value, "v")
	core := raw
	prerelease := ""
	if index := strings.IndexByte(raw, '-'); index >= 0 {
		core = raw[:index]
		prerelease = raw[index+1:]
		if prerelease == "" {
			return semver{}, fmt.Errorf("候选后缀为空: %q", value)
		}
		if err := validatePrerelease(prerelease); err != nil {
			return semver{}, fmt.Errorf("候选后缀非法: %w", err)
		}
	}
	parts := strings.Split(core, ".")
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
	return semver{major: major, minor: minor, patch: patch, prerelease: prerelease}, nil
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

// validatePrerelease 校验候选后缀的字符集：点分隔的若干非空段，每段只含 ASCII 字母数字与连字符。
// 禁止 `+`（构建元数据）、空段（"rc..1"）与数字段前导零（SemVer 2.0.0 第 9 条），
// 与核心三段解析的严格度保持一致。
func validatePrerelease(value string) error {
	for _, part := range strings.Split(value, ".") {
		if part == "" {
			return fmt.Errorf("候选段为空")
		}
		if len(part) > 1 && part[0] == '0' && isAllDigits(part) {
			return fmt.Errorf("候选数字段含前导零: %q", part)
		}
		for _, char := range part {
			isDigit := char >= '0' && char <= '9'
			isAlpha := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z'
			if !isDigit && !isAlpha && char != '-' {
				return fmt.Errorf("候选段含非法字符 %q", string(char))
			}
		}
	}
	return nil
}

// isAllDigits 判断是否全为 ASCII 数字且非空（空串与含非数字字符均返回 false）。
func isAllDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

// compareSemver 比较两个版本：a<b 返回 -1、a==b 返回 0、a>b 返回 1。
// 先比三段数字；数字相等时按 SemVer 2.0.0 第 11 条比候选后缀：
// 无后缀者高于有后缀者（1.5.0 > 1.5.0-rc.2），都有则逐段比较。
func compareSemver(a, b semver) int {
	if result := cmpInt(a.major, b.major); result != 0 {
		return result
	}
	if result := cmpInt(a.minor, b.minor); result != 0 {
		return result
	}
	if result := cmpInt(a.patch, b.patch); result != 0 {
		return result
	}
	return comparePrerelease(a.prerelease, b.prerelease)
}

// comparePrerelease 比较候选后缀，返回 -1/0/1。
// 规则（SemVer 2.0.0 第 11 条）：无后缀 > 有后缀；都有则按点分段比较，
// 数字段按数值比且低于字母段，字母段按字典序比，前缀相同时段数少者低（rc.1 < rc.2 < rc.10）。
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	// 正式版本高于任何候选版本。
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for index := 0; index < len(aParts) && index < len(bParts); index++ {
		if result := comparePrereleasePart(aParts[index], bParts[index]); result != 0 {
			return result
		}
	}
	return cmpInt(len(aParts), len(bParts))
}

// comparePrereleasePart 比较单个候选段：全数字段按数值比较，否则按字典序；数字段低于非数字段。
//
// 「是否数字段」用 isAllDigits 判定而非 strconv.Atoi：Atoi 会把 "+1"/"-1" 当数字段，
// 且超 int 范围时报错、让超大数字段静默降级为字典序比较（"999…" < "5"）。候选段经
// validatePrerelease 后本不含符号，但这里的判据应是自足的，不依赖上游校验。
func comparePrereleasePart(a, b string) int {
	aNumeric := isAllDigits(a)
	bNumeric := isAllDigits(b)
	switch {
	case aNumeric && bNumeric:
		aNumber, aErr := strconv.Atoi(a)
		bNumber, bErr := strconv.Atoi(b)
		if aErr != nil || bErr != nil {
			// 超出 int 范围：按长度再按字典序比较等价于按数值比较（无前导零时位数多者更大）。
			if len(a) != len(b) {
				return cmpInt(len(a), len(b))
			}
			return strings.Compare(a, b)
		}
		return cmpInt(aNumber, bNumber)
	case aNumeric:
		return -1
	case bNumeric:
		return 1
	default:
		return strings.Compare(a, b)
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

// IsNewer 判断远端版本是否严格高于当前运行版本。
//
// 两侧校验强度刻意不同：
//   - 当前版本按运行时口径解析，允许候选后缀（"1.5.0-rc.2" 是合法的运行版本，ADR-0092）；
//   - 远端版本按 GA 口径解析，严格拒绝候选与开发后缀——在线更新只消费 GA（ADR-0074 第 4 条），
//     若这里放宽，vX.Y.Z-rc.N 会被判为「有更新」而把候选装进生产实例。
//
// 候选相对 GA 的语义：1.5.0-rc.2 < 1.5.0，故候选实例会被同号 GA 判为有更新，可正常晋升。
// 当前为 dev 哨兵时版本未知，不提示更新。
//
// 注意：comparePrerelease 实现的候选序号排序（rc.2 < rc.3）在生产路径上不会被走到——
// 远端侧只接受 GA，故候选实例能升级到的目标永远是正式版 GA，不存在候选→候选的在线升级；
// 需要逐号验收候选时必须显式安装。该排序规则仍被单测锁定，供比较器自身正确性使用。
func IsNewer(current, remote string) (bool, error) {
	if current == devVersion {
		return false, nil
	}
	currentVersion, err := parseRuntimeVersion(current)
	if err != nil {
		return false, fmt.Errorf("当前版本解析失败: %w", err)
	}
	remoteVersion, err := parseGAVersion(remote)
	if err != nil {
		return false, fmt.Errorf("远端版本解析失败: %w", err)
	}
	return compareSemver(remoteVersion, currentVersion) > 0, nil
}

// parseGAVersion 解析远端 GA 版本，可容忍前导 v，但拒绝候选与开发后缀。
// 与 parseGATag 共用同一口径：远端只可能是正式版本。
func parseGAVersion(value string) (semver, error) {
	parsed, err := parseRuntimeVersion(value)
	if err != nil {
		return semver{}, err
	}
	if parsed.prerelease != "" {
		return semver{}, fmt.Errorf("远端版本不得带候选或开发后缀: %q", value)
	}
	return parsed, nil
}

// SameReleaseVersion 判断两个版本串是否指向同一个正式版本，容忍前导 v 与候选后缀。
//
// 用途是跨来源的版本比对：换版对账一侧是 GitHub tag（"v1.5.0"，带 v、永不带候选后缀，
// 因 parseGATag 强制），另一侧是注入的运行时版本（"1.5.0" 或候选构建的 "1.5.0-rc.2"，ADR-0092）。
// 三者指向同一版本时应判为相同。字符串相等做不到这点。
//
// 任一侧无法解析时退回字符串相等：比对失败不得被误判成"版本相同"，
// 对账的默认方向必须是保守的（不确定即视为未达到目标）。
func SameReleaseVersion(a, b string) bool {
	aVersion, aErr := parseRuntimeVersion(a)
	bVersion, bErr := parseRuntimeVersion(b)
	if aErr != nil || bErr != nil {
		return a == b
	}
	return aVersion.major == bVersion.major &&
		aVersion.minor == bVersion.minor &&
		aVersion.patch == bVersion.patch
}
