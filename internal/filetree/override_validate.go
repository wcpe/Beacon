package filetree

import (
	"path"
	"strings"

	"beacon/internal/apperr"
)

// pluginsPrefix 是覆盖集目标根必须落在的固定前缀（限定 plugins/<plugin>/ 内，见 ADR-0011 决策 4）。
const pluginsPrefix = "plugins/"

// commandMetaChars 是重载命令禁止出现的注入元字符集合（含管道 / 重定向 / 变量 / 反引号等，见 ADR-0011 决策 3）。
const commandMetaChars = ";&|><$`"

// winReservedNames 是 Windows 保留设备名（不区分大小写，规范化后逐段比对，见 ADR-0011 决策 4）。
var winReservedNames = map[string]struct{}{
	"con": {}, "prn": {}, "aux": {}, "nul": {},
	"com1": {}, "com2": {}, "com3": {}, "com4": {}, "com5": {},
	"com6": {}, "com7": {}, "com8": {}, "com9": {},
	"lpt1": {}, "lpt2": {}, "lpt3": {}, "lpt4": {}, "lpt5": {},
	"lpt6": {}, "lpt7": {}, "lpt8": {}, "lpt9": {},
}

// ValidateTargetRoot 校验覆盖集目标根目录并返回归一化值（去尾部斜杠）。
// 规则（控制面早校验，agent 为最终权威，二者同口径）：
//   - 必须以 plugins/ 开头且限定到具体插件子目录（不止于 plugins 根本身）；
//   - 禁绝对路径 / 盘符 / UNC / 反斜杠 / 冒号；禁 `..` 穿越（用 path.Clean 后逐段查）；
//   - 任一段不得为 Windows 保留设备名（不区分大小写）。
func ValidateTargetRoot(root string) (string, error) {
	if root == "" {
		return "", apperr.ErrInvalidTargetRoot
	}
	if !isCleanRelativeSafe(root) {
		return "", apperr.ErrInvalidTargetRoot
	}
	clean := path.Clean(strings.TrimSuffix(root, "/"))
	// 必须严格落在 plugins/<plugin> 之下（plugins 根本身不算，需至少一级插件目录）。
	if clean == "plugins" || !strings.HasPrefix(clean+"/", pluginsPrefix) {
		return "", apperr.ErrInvalidTargetRoot
	}
	if len(strings.Split(clean, "/")) < 2 {
		return "", apperr.ErrInvalidTargetRoot
	}
	return clean, nil
}

// ValidateMemberPath 校验覆盖集成员文件相对 path：
//   - 同 normalizePath 的相对安全口径（非空 / 无反斜杠 / 无穿越 / 非绝对）；
//   - 额外禁冒号、禁段为 Windows 保留设备名、禁 .jar 后缀（防越界进 P3 发布编排）。
// path 以目标根为基准的相对路径，落盘逃逸由 agent RelativePathGuard 最终把关。
func ValidateMemberPath(targetRoot, p string) error {
	if p == "" {
		return apperr.ErrInvalidPath
	}
	if !isCleanRelativeSafe(p) {
		return apperr.ErrInvalidPath
	}
	if strings.HasSuffix(strings.ToLower(p), ".jar") {
		return apperr.ErrInvalidPath
	}
	return nil
}

// ValidateReloadCommand 校验单条重载命令并返回去首尾空白后的归一化命令。
// 拒绝：空 / 全空白 / 含元字符（`;&|><$\`+反引号）/ 含换行回车或任何控制字符（防注入、防多条）。
// 白名单首 token 校验由 agent 本地承担（控制面不下发白名单，见 ADR-0011 决策 3），此处只做语法安全。
func ValidateReloadCommand(cmd string) (string, error) {
	trimmed := strings.TrimSpace(cmd)
	if trimmed == "" {
		return "", apperr.ErrInvalidReloadCommand
	}
	if strings.ContainsAny(trimmed, commandMetaChars) {
		return "", apperr.ErrInvalidReloadCommand
	}
	for _, r := range trimmed {
		// 拒绝换行 / 回车 / 制表符等一切控制字符（ASCII < 0x20 与 0x7F）。
		if r < 0x20 || r == 0x7F {
			return "", apperr.ErrInvalidReloadCommand
		}
	}
	return trimmed, nil
}

// FirstToken 取命令首 token（按空白切分的第一段），供 agent 本地白名单比对。
func FirstToken(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// isCleanRelativeSafe 判定相对路径是否安全：非空、无反斜杠、无冒号、非绝对、清理后无穿越，
// 且任一段非 Windows 保留设备名。控制面与 agent 同口径（agent RelativePathGuard 平行实现）。
func isCleanRelativeSafe(p string) bool {
	if p == "" {
		return false
	}
	if strings.ContainsAny(p, `\:`) {
		return false // 反斜杠 / 冒号（盘符 / ADS）一律拒
	}
	if strings.HasPrefix(p, "/") {
		return false // 绝对路径
	}
	clean := path.Clean(p)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return false
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return false
		}
		// 规范化后不区分大小写比对 Windows 保留设备名（取点号前主名）。
		base := seg
		if dot := strings.IndexByte(seg, '.'); dot >= 0 {
			base = seg[:dot]
		}
		if _, bad := winReservedNames[strings.ToLower(base)]; bad {
			return false
		}
	}
	return true
}
