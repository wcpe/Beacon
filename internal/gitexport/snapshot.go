// Package gitexport 实现 git 单向导出镜像（FR-47，见 ADR-0030）的纯逻辑：
// 把配置 / 文件树的源层组装为 git 仓内的文件快照、按覆盖链坐标布局路径、敏感排除、渲染 commit message。
// 全为无副作用纯函数，便于穷举单测；真正读写 git 仓的副作用经 GitRepo 端口隔离（实现见 repo_*.go）。
//
// 导出的是「源层」（global / 大区 / 小区 / 单服 各层原始内容）而非某服合并后的有效配置，
// 让 git 树直观映射覆盖链结构。git 仓是单向派生镜像、不作第二真源（守架构不变量 #3）。
package gitexport

import (
	"strings"

	"github.com/wcpe/Beacon/internal/model"
)

// 顶层目录名：配置源层落 configs/、文件树源层落 files/。
const (
	configsDir = "configs"
	filesDir   = "files"
)

// globalDirSegment 是 global 层在 git 路径里的目录名。
// 库内 global 层 group_code 为占位保留字 __GLOBAL__；为避免双下划线保留字直接出现在路径、
// 也避免与真实 group 名碰撞（真实 group 名禁用 __GLOBAL__，见 normalizeScope），渲染为 _global_。
const globalDirSegment = "_global_"

// groupDirSegment 是 group 层「自身」那一格的目录名（区别于其下的 zone/server 子目录）。
const groupDirSegment = "_group_"

// SourceLayer 是一条待导出的源层记录（配置项或文件对象的某一覆盖层）。
// 它脱离 GORM 实体，只携导出所需的最小信息，便于纯函数组装与单测。
type SourceLayer struct {
	// Kind 区分配置（configs/）还是文件树（files/）源层。
	Kind LayerKind
	// Namespace 环境编码。
	Namespace string
	// Group 大区编码；global 层为占位 __GLOBAL__。
	Group string
	// ScopeLevel 覆盖层：global/group/zone/server。
	ScopeLevel string
	// ScopeTarget 该层目标键：zone=zone 编码、server=serverId；global/group 为空。
	ScopeTarget string
	// Name 配置层取 dataId，文件层取相对 path。决定 git 路径的末段（path 可含子目录）。
	Name string
	// Content 该层原始内容：敏感配置项为 enc:v1: 密文（不解密），其余为明文 / 整文件原文。
	Content string
	// Excluded 为真则该源层不导出到 git（文件树 path 级敏感排除，见 ADR-0030 决策4）。
	// 配置项敏感走「导密文」（Content 已是密文），不走 Excluded。
	Excluded bool
}

// LayerKind 区分配置源层与文件树源层。
type LayerKind int

const (
	// KindConfig 配置中心（通道A）源层，落 configs/。
	KindConfig LayerKind = iota
	// KindFile 文件树托管（通道B）源层，落 files/。
	KindFile
)

// Snapshot 是一次导出的全量文件集：git 仓内相对路径 → 文件内容。
// 由源层记录纯函数组装；每次导出以它全量覆盖工作区（简单、天然自愈漂移，见 ADR-0030 决策3）。
type Snapshot struct {
	// Files git 仓相对路径 → 文件内容。
	Files map[string]string
}

// BuildSnapshot 把配置与文件树源层组装为全量快照。
// 敏感排除（Excluded）的源层不进快照（git 里看不到该文件）；敏感配置项 Content 已是密文、照常导出。
// 同一路径重复（不应发生，唯一键保证）时后者覆盖前者，保证结果确定。
func BuildSnapshot(layers []SourceLayer) Snapshot {
	files := make(map[string]string, len(layers))
	for _, l := range layers {
		if l.Excluded {
			continue // path 级敏感排除：整体不导出
		}
		files[BuildPath(l)] = l.Content
	}
	return Snapshot{Files: files}
}

// BuildPath 把一条源层映射到 git 仓内的相对路径（确定性、可单测）。
// 布局（见 ADR-0030 决策3 / spec §3.3）：
//
//	configs|files / <ns> / {_global_ | <group>/_group_ | <group>/zone/<zone> | <group>/server/<serverId>} / <name>
//
// global 层不嵌 group 段（本就跨大区）。各路径段做最小安全清洗（去 .. / 绝对前缀），
// 入库时 name 已过 normalizePath / scope 校验，这里再兜一道防御性 join。
func BuildPath(l SourceLayer) string {
	top := filesDir
	if l.Kind == KindConfig {
		top = configsDir
	}
	segs := []string{top, sanitizeSegment(l.Namespace)}
	segs = append(segs, scopeSegments(l)...)
	// name 可能含子目录（文件相对 path），逐段清洗后拼回
	segs = append(segs, sanitizeRelPath(l.Name))
	return strings.Join(segs, "/")
}

// scopeSegments 返回覆盖层对应的中间目录段（不含顶层 / namespace / name）。
func scopeSegments(l SourceLayer) []string {
	switch l.ScopeLevel {
	case model.ScopeGlobal:
		return []string{globalDirSegment}
	case model.ScopeGroup:
		return []string{sanitizeSegment(l.Group), groupDirSegment}
	case model.ScopeZone:
		return []string{sanitizeSegment(l.Group), model.ScopeZone, sanitizeSegment(l.ScopeTarget)}
	case model.ScopeServer:
		return []string{sanitizeSegment(l.Group), model.ScopeServer, sanitizeSegment(l.ScopeTarget)}
	default:
		// 未知层兜底归入 _unknown_，避免源层因脏数据丢失（不应发生，scope 入库已校验）
		return []string{"_unknown_"}
	}
}

// sanitizeSegment 清洗单个路径段：去首尾空白、空与危险值替换为占位，禁止路径分隔与穿越。
func sanitizeSegment(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || s == "." || s == ".." {
		return "_"
	}
	// 单段不应含分隔符；出现即替换为下划线（防越级）
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	return s
}

// sanitizeRelPath 清洗可含子目录的相对路径（文件 path）：逐段清洗、丢弃空与 . / .. 段，
// 防绝对路径与向上穿越逃出导出仓。结果不以 / 开头。
func sanitizeRelPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.ReplaceAll(p, "\\", "/")
	parts := strings.Split(p, "/")
	clean := make([]string, 0, len(parts))
	for _, seg := range parts {
		seg = strings.TrimSpace(seg)
		if seg == "" || seg == "." || seg == ".." {
			continue // 丢弃空 / 当前 / 向上段
		}
		clean = append(clean, seg)
	}
	if len(clean) == 0 {
		return "_" // 整段都被清掉时给个占位，避免落到上级目录
	}
	return strings.Join(clean, "/")
}
