package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ConfigScopePin 是「作用域 → 版本 id」灰度覆盖参数（P9 变更单渲染接缝，spec §6；本期无调用方）：
// 被指派链用指定版本参与合并，其余链按各自 head。
type ConfigScopePin struct {
	ScopeLevel string
	ScopeRefID uint
	VersionID  uint
}

// resolvedEffective 是一次有效解析的完整产物（明文，脱敏由读出口另行处理）。
type resolvedEffective struct {
	content     string
	hash        string
	sources     []merge.KeyProvenance
	deletions   []merge.KeyProvenance
	layers      []ConfigEffectiveLayerView
	scopeNames  map[configScopeRef]string
	layerScopes map[string]configScopeRef // provenance 编码 → 层定位
}

// Effective 有效配置预览（spec §4.3）：脱敏后的合并内容 + 明文 hash + 逐键来源 + 删键列表 + 层摘要。
func (s *ConfigCenterService) Effective(fileID uint, target ConfigEffectiveTarget) (*ConfigEffectiveView, error) {
	file, err := s.findActiveFile(fileID)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveEffective(file, target, nil)
	if err != nil {
		return nil, err
	}
	masked, err := maskContentText(file.Format, resolved.content, decodeSensitivePaths(file.SensitivePaths))
	if err != nil {
		return nil, err
	}
	return &ConfigEffectiveView{
		EffectiveContent: masked,
		EffectiveHash:    resolved.hash,
		Provenance:       provenanceViews(resolved),
		DeletedKeys:      deletedKeyViews(resolved),
		Layers:           resolved.layers,
	}, nil
}

// EffectivePlaintext 进程内明文有效渲染（P9 变更单载荷快照接缝，spec §6）：
// 明文只经本进程内接口流向交付渲染，不经任何 HTTP 端点；回收站内文件同样拒绝（spec §4.9）。
func (s *ConfigCenterService) EffectivePlaintext(fileID uint, target ConfigEffectiveTarget, pins []ConfigScopePin) (string, string, error) {
	file, err := s.findActiveFile(fileID)
	if err != nil {
		return "", "", err
	}
	resolved, err := s.resolveEffective(file, target, pins)
	if err != nil {
		return "", "", err
	}
	return resolved.content, resolved.hash, nil
}

// resolveEffective 解析目标链、逐层取参与版本（head 或 pin 指定版本）、深合并并富化来源。
// 合并与 provenance 全部委托 internal/merge 的纯函数（平行实现交叉测试防漂移，spec §4.3）。
func (s *ConfigCenterService) resolveEffective(file *model.ConfigFile, target ConfigEffectiveTarget, pins []ConfigScopePin) (*resolvedEffective, error) {
	chain, err := resolveTargetChain(s.db, file.NamespaceID, target)
	if err != nil {
		return nil, err
	}
	versions, err := s.chainVersions(file, chain, pins)
	if err != nil {
		return nil, err
	}
	names, err := resolveScopeNames(s.db, chain)
	if err != nil {
		return nil, err
	}
	out := &resolvedEffective{scopeNames: names, layerScopes: map[string]configScopeRef{}}
	provLayers := make([]merge.ProvLayer, 0, len(chain))
	for _, ref := range chain {
		version := versions[ref]
		out.layers = append(out.layers, effectiveLayerView(ref, names[ref], version))
		if version == nil || version.IsRemoval {
			continue
		}
		encoded := encodeProvScope(ref, version.VersionNo)
		out.layerScopes[encoded] = ref
		provLayers = append(provLayers, merge.ProvLayer{Scope: encoded, Content: version.Content})
	}
	content, sources, deletions, err := merge.MergeDataIDWithProvenance(file.Format, provLayers)
	if err != nil {
		return nil, fmt.Errorf("合并有效配置失败: %w", err)
	}
	out.content = content
	out.hash = merge.Sha256Hex(content)
	out.sources = sources
	out.deletions = deletions
	return out, nil
}

// chainVersions 取链上各层参与合并的版本：默认各链 head；pin 命中的链用指定版本（须属于该文件该链）。
func (s *ConfigCenterService) chainVersions(file *model.ConfigFile, chain []configScopeRef, pins []ConfigScopePin) (map[configScopeRef]*model.ConfigLayerVersion, error) {
	heads, err := s.versions.HeadsByFile(file.ID)
	if err != nil {
		return nil, err
	}
	byRef := make(map[configScopeRef]*model.ConfigLayerVersion, len(heads))
	for i := range heads {
		byRef[configScopeRef{Level: heads[i].ScopeLevel, RefID: heads[i].ScopeRefID}] = &heads[i]
	}
	for _, pin := range pins {
		ref := configScopeRef{Level: pin.ScopeLevel, RefID: pin.ScopeRefID}
		pinned, err := s.versions.FindByID(pin.VersionID)
		if err != nil {
			return nil, err
		}
		if pinned == nil || pinned.ConfigFileID != file.ID || pinned.ScopeLevel != ref.Level || pinned.ScopeRefID != ref.RefID {
			return nil, apperr.New(http.StatusBadRequest, "INVALID_PARAM",
				fmt.Sprintf("版本指派 %d 不属于链 %s/%d", pin.VersionID, pin.ScopeLevel, pin.ScopeRefID))
		}
		byRef[ref] = pinned
	}
	// 只保留链上的层
	out := make(map[configScopeRef]*model.ConfigLayerVersion, len(chain))
	for _, ref := range chain {
		if v, ok := byRef[ref]; ok {
			out[ref] = v
		}
	}
	return out, nil
}

// effectiveLayerView 组装单层摘要（无链 / head 为撤销 → contributing=false）。
func effectiveLayerView(ref configScopeRef, name string, version *model.ConfigLayerVersion) ConfigEffectiveLayerView {
	refID := ref.RefID
	view := ConfigEffectiveLayerView{ScopeLevel: ref.Level, ScopeRefID: &refID, ScopeName: &name}
	if version == nil {
		return view
	}
	no := version.VersionNo
	hash := version.ContentHash
	view.HeadVersionNo = &no
	view.HeadHash = &hash
	view.Contributing = !version.IsRemoval
	return view
}

// encodeProvScope 把层定位编码进 merge provenance 的 scope 标签（level:refId:versionNo）。
func encodeProvScope(ref configScopeRef, versionNo int) string {
	return ref.Level + ":" + strconv.FormatUint(uint64(ref.RefID), 10) + ":" + strconv.Itoa(versionNo)
}

// decodeProvScope 解码 provenance scope 标签为层定位与版本号。
func decodeProvScope(encoded string) (configScopeRef, int) {
	parts := strings.SplitN(encoded, ":", 3)
	if len(parts) != 3 {
		return configScopeRef{Level: encoded}, 0
	}
	refID, _ := strconv.ParseUint(parts[1], 10, 64)
	versionNo, _ := strconv.Atoi(parts[2])
	return configScopeRef{Level: parts[0], RefID: uint(refID)}, versionNo
}

// provenanceViews 把 merge 来源富化为 {path, scopeLevel, scopeRefId, scopeName, versionNo}（spec §4.3）。
func provenanceViews(resolved *resolvedEffective) []ConfigProvenanceEntryView {
	out := make([]ConfigProvenanceEntryView, 0, len(resolved.sources))
	for _, src := range resolved.sources {
		ref, versionNo := decodeProvScope(src.Scope)
		out = append(out, ConfigProvenanceEntryView{
			Path: strings.Join(src.Path, "."), ScopeLevel: ref.Level, ScopeRefID: ref.RefID,
			ScopeName: resolved.scopeNames[ref], VersionNo: versionNo,
		})
	}
	return out
}

// deletedKeyViews 把 merge 删键轨迹富化为 {path, scopeLevel, scopeRefId, scopeName, versionNo}。
func deletedKeyViews(resolved *resolvedEffective) []ConfigDeletedKeyView {
	out := make([]ConfigDeletedKeyView, 0, len(resolved.deletions))
	for _, del := range resolved.deletions {
		ref, versionNo := decodeProvScope(del.Scope)
		out = append(out, ConfigDeletedKeyView{
			Path: strings.Join(del.Path, "."), ScopeLevel: ref.Level, ScopeRefID: ref.RefID,
			ScopeName: resolved.scopeNames[ref], VersionNo: versionNo,
		})
	}
	return out
}

// Diff 统一键级 diff（spec §4.5）：left/right 各接受 version:<id> / scope:<level>:<refId> /
// effective:<targetType>:<targetId> 描述符，任意组合；敏感值脱敏后再进入 diff 输出。
func (s *ConfigCenterService) Diff(fileID uint, left, right string) (*ConfigDiffView, error) {
	file, err := s.findActiveFile(fileID)
	if err != nil {
		return nil, err
	}
	paths := decodeSensitivePaths(file.SensitivePaths)
	leftLeaves, err := s.diffSideLeaves(file, left, paths)
	if err != nil {
		return nil, err
	}
	rightLeaves, err := s.diffSideLeaves(file, right, paths)
	if err != nil {
		return nil, err
	}
	return buildDiffView(left, right, leftLeaves, rightLeaves), nil
}

// diffSideLeaves 解析一侧描述符为脱敏后的叶子映射（path → 值字符串）。
func (s *ConfigCenterService) diffSideLeaves(file *model.ConfigFile, spec string, sensitivePaths []string) (map[string]string, error) {
	parsed, err := s.resolveDiffSide(file, spec)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return map[string]string{}, nil
	}
	return flattenLeafValues(maskSensitiveContent(parsed, sensitivePaths)), nil
}

// resolveDiffSide 解析描述符为解析后的内容结构（无贡献 / 空侧得 nil）。
func (s *ConfigCenterService) resolveDiffSide(file *model.ConfigFile, spec string) (any, error) {
	parts := strings.Split(spec, ":")
	switch parts[0] {
	case "version":
		if len(parts) != 2 {
			return nil, diffSpecInvalid(spec)
		}
		return s.diffVersionSide(file, parts[1])
	case "scope":
		if len(parts) != 3 {
			return nil, diffSpecInvalid(spec)
		}
		return s.diffScopeSide(file, parts[1], parts[2])
	case "effective":
		if len(parts) != 3 {
			return nil, diffSpecInvalid(spec)
		}
		return s.diffEffectiveSide(file, parts[1], parts[2])
	default:
		return nil, diffSpecInvalid(spec)
	}
}

// diffSpecInvalid 构造描述符不合法错误。
func diffSpecInvalid(spec string) error {
	return apperr.New(http.StatusBadRequest, "INVALID_PARAM", "无法识别的 diff 侧描述符: "+spec)
}

// diffVersionSide 解析 version:<id> 描述符（版本须属于本文件）。
func (s *ConfigCenterService) diffVersionSide(file *model.ConfigFile, rawID string) (any, error) {
	id, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		return nil, diffSpecInvalid("version:" + rawID)
	}
	version, err := s.versions.FindByID(uint(id))
	if err != nil {
		return nil, err
	}
	if version == nil || version.ConfigFileID != file.ID {
		return nil, apperr.ErrConfigVersionNotFound
	}
	if version.IsRemoval {
		return nil, nil
	}
	return merge.Parse(file.Format, version.Content)
}

// diffScopeSide 解析 scope:<level>:<refId> 描述符（取该链当前 head，无贡献得空侧）。
func (s *ConfigCenterService) diffScopeSide(file *model.ConfigFile, level, rawRefID string) (any, error) {
	refID, err := strconv.ParseUint(rawRefID, 10, 64)
	if err != nil || !model.IsValidConfigScopeLevel(level) {
		return nil, diffSpecInvalid("scope:" + level + ":" + rawRefID)
	}
	head, err := s.versions.Head(file.ID, level, uint(refID))
	if err != nil {
		return nil, err
	}
	if head == nil || head.IsRemoval {
		return nil, nil
	}
	return merge.Parse(file.Format, head.Content)
}

// diffEffectiveSide 解析 effective:<targetType>:<targetId> 描述符（目标的有效合并结果）。
func (s *ConfigCenterService) diffEffectiveSide(file *model.ConfigFile, targetType, targetID string) (any, error) {
	target, err := effectiveTargetOf(file, targetType, targetID)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveEffective(file, target, nil)
	if err != nil {
		return nil, err
	}
	return merge.Parse(file.Format, resolved.content)
}

// effectiveTargetOf 把 diff 描述符目标映射为有效解析目标（namespace 目标须等于文件 namespace）。
func effectiveTargetOf(file *model.ConfigFile, targetType, targetID string) (ConfigEffectiveTarget, error) {
	id, err := strconv.ParseUint(targetID, 10, 64)
	if err != nil && targetType != "server" {
		return ConfigEffectiveTarget{}, diffSpecInvalid("effective:" + targetType + ":" + targetID)
	}
	switch targetType {
	case "server":
		return ConfigEffectiveTarget{ServerRef: targetID}, nil
	case "zone":
		return ConfigEffectiveTarget{ZoneID: uint(id)}, nil
	case "region":
		return ConfigEffectiveTarget{RegionID: uint(id)}, nil
	case "bc_cluster":
		return ConfigEffectiveTarget{BCClusterID: uint(id)}, nil
	case "namespace":
		if uint(id) != file.NamespaceID {
			return ConfigEffectiveTarget{}, scopeMismatch("目标 namespace 与文件不一致")
		}
		return ConfigEffectiveTarget{}, nil
	default:
		return ConfigEffectiveTarget{}, diffSpecInvalid("effective:" + targetType + ":" + targetID)
	}
}

// buildDiffView 由两侧叶子映射产出键级差异与 unified 文本（形状对齐 devmock）。
func buildDiffView(leftSpec, rightSpec string, left, right map[string]string) *ConfigDiffView {
	view := &ConfigDiffView{
		Added:   []ConfigDiffEntryAdded{},
		Removed: []ConfigDiffEntryRemoved{},
		Changed: []ConfigDiffEntryChanged{},
	}
	for _, path := range sortedKeys(right) {
		leftValue, ok := left[path]
		if !ok {
			view.Added = append(view.Added, ConfigDiffEntryAdded{Path: path, Right: right[path]})
		} else if leftValue != right[path] {
			view.Changed = append(view.Changed, ConfigDiffEntryChanged{Path: path, Left: leftValue, Right: right[path]})
		}
	}
	for _, path := range sortedKeys(left) {
		if _, ok := right[path]; !ok {
			view.Removed = append(view.Removed, ConfigDiffEntryRemoved{Path: path, Left: left[path]})
		}
	}
	view.UnifiedDiff = buildUnifiedDiff(leftSpec, rightSpec, view)
	return view
}

// buildUnifiedDiff 组装键级 unified 文本（--- / +++ 头 + 逐键 -/+ 行，对齐 devmock 形状）。
func buildUnifiedDiff(leftSpec, rightSpec string, view *ConfigDiffView) string {
	lines := []string{"--- " + leftSpec, "+++ " + rightSpec}
	for _, entry := range view.Removed {
		lines = append(lines, "- "+entry.Path+": "+entry.Left)
	}
	for _, entry := range view.Changed {
		lines = append(lines, "- "+entry.Path+": "+entry.Left, "+ "+entry.Path+": "+entry.Right)
	}
	for _, entry := range view.Added {
		lines = append(lines, "+ "+entry.Path+": "+entry.Right)
	}
	return strings.Join(lines, "\n")
}

// sortedKeys 返回映射键的字典序列表。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// flattenLeafValues 把解析结构拍平为「点分叶子路径 → 值字符串」映射（map 递归到底，非 map 即叶子）。
// 整体为 nil（空侧 / 链空）得空映射。
func flattenLeafValues(v any) map[string]string {
	out := map[string]string{}
	if v == nil {
		return out
	}
	flattenInto(v, "", out)
	return out
}

// flattenInto 递归展开；空 map 视为叶子（与 merge provenance 口径一致）。
func flattenInto(v any, prefix string, out map[string]string) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		if prefix != "" || !ok {
			out[prefix] = leafValueString(v)
		}
		return
	}
	for key, child := range m {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		flattenInto(child, path, out)
	}
}

// leafValueString 把叶子值转为展示字符串：字符串原样、null 显式、其余 JSON 紧凑编码。
func leafValueString(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case string:
		return t
	case map[string]any:
		return "{}"
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(raw)
	}
}
