package service

import (
	"encoding/json"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/configschema"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 本文件是配置中心 V2 的对外响应视图（camelCase，逐字对齐 packages/contracts/src/config-center.ts
// 与 devmock 形状——前端已按此消费，不可漂移）。handler 直出不再二次映射。

// ConfigFileView 对齐 contracts ConfigFileRow。
type ConfigFileView struct {
	ID             uint       `json:"id"`
	NamespaceID    uint       `json:"namespaceId"`
	Name           string     `json:"name"`
	Format         string     `json:"format"`
	Description    string     `json:"description"`
	SchemaJSON     *string    `json:"schemaJson"`
	SensitivePaths []string   `json:"sensitivePaths"`
	DeletedAt      *time.Time `json:"deletedAt"`
	DeletedBy      *string    `json:"deletedBy"`
	CreatedBy      string     `json:"createdBy"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

// configFileView 把实体映射为对外视图（schema / 删除人空串 → null，敏感路径 json 文本 → 数组）。
func configFileView(f *model.ConfigFile) *ConfigFileView {
	view := &ConfigFileView{
		ID: f.ID, NamespaceID: f.NamespaceID, Name: f.Name, Format: f.Format,
		Description: f.Description, SensitivePaths: decodeSensitivePaths(f.SensitivePaths),
		DeletedAt: f.DeletedAt, CreatedBy: f.CreatedBy, CreatedAt: f.CreatedAt, UpdatedAt: f.UpdatedAt,
	}
	if f.SchemaJSON != "" {
		s := f.SchemaJSON
		view.SchemaJSON = &s
	}
	if f.DeletedBy != "" {
		b := f.DeletedBy
		view.DeletedBy = &b
	}
	return view
}

// decodeSensitivePaths 解码敏感路径 json 文本；空串 / 解码失败均得空数组（响应恒为数组不为 null）。
func decodeSensitivePaths(raw string) []string {
	if raw == "" {
		return []string{}
	}
	var paths []string
	if err := json.Unmarshal([]byte(raw), &paths); err != nil || paths == nil {
		return []string{}
	}
	return paths
}

// ConfigFileScopeOverview 是文件详情内嵌的单链概览（对齐前端 ConfigFileScopeOverview）。
type ConfigFileScopeOverview struct {
	ScopeLevel    string `json:"scopeLevel"`
	ScopeRefID    uint   `json:"scopeRefId"`
	ScopeName     string `json:"scopeName"`
	HeadVersionNo int    `json:"headVersionNo"`
	IsRemoval     bool   `json:"isRemoval"`
}

// ConfigFileDetailView 是文件详情：元数据 + 各层覆盖概览（对齐前端 ConfigFileDetail）。
type ConfigFileDetailView struct {
	ConfigFileView
	Scopes []ConfigFileScopeOverview `json:"scopes"`
}

// ConfigFileItemView 是列表项（对齐 contracts ConfigFileItem）。
type ConfigFileItemView struct {
	ID                     uint      `json:"id"`
	NamespaceID            uint      `json:"namespaceId"`
	Name                   string    `json:"name"`
	Format                 string    `json:"format"`
	Description            string    `json:"description"`
	ContributingLayerCount int       `json:"contributingLayerCount"`
	UpdatedAt              time.Time `json:"updatedAt"`
	EffectiveHash          string    `json:"effectiveHash,omitempty"`
}

// ConfigFileListView 是文件分页列表响应（对齐 contracts Paged<ConfigFileItem>）。
type ConfigFileListView struct {
	Items []ConfigFileItemView `json:"items"`
	Total int64                `json:"total"`
}

// ConfigTrashItemView 是回收站列表项（对齐前端 TrashItem）。
type ConfigTrashItemView struct {
	ID          uint       `json:"id"`
	NamespaceID uint       `json:"namespaceId"`
	Name        string     `json:"name"`
	Format      string     `json:"format"`
	DeletedBy   *string    `json:"deletedBy"`
	DeletedAt   *time.Time `json:"deletedAt"`
}

// ConfigTrashListView 是回收站分页列表响应。
type ConfigTrashListView struct {
	Items []ConfigTrashItemView `json:"items"`
	Total int64                 `json:"total"`
}

// ConfigScopeSummaryView 是各贡献链概览项（对齐 contracts ConfigScopeSummary）。
type ConfigScopeSummaryView struct {
	ScopeLevel    string    `json:"scopeLevel"`
	ScopeRefID    uint      `json:"scopeRefId"`
	ScopeName     string    `json:"scopeName"`
	HeadVersionNo int       `json:"headVersionNo"`
	HeadHash      string    `json:"headHash"`
	IsRemoval     bool      `json:"isRemoval"`
	UpdatedBy     string    `json:"updatedBy"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ConfigScopesView 是贡献链概览响应 {scopes}。
type ConfigScopesView struct {
	Scopes []ConfigScopeSummaryView `json:"scopes"`
}

// ConfigVersionListItemView 是链内版本列表项（对齐前端 ConfigVersionListItem，主键对外名 versionId）。
type ConfigVersionListItemView struct {
	VersionID        uint      `json:"versionId"`
	VersionNo        int       `json:"versionNo"`
	ContentHash      string    `json:"contentHash"`
	IsRemoval        bool      `json:"isRemoval"`
	BasedOnVersionID *uint     `json:"basedOnVersionId"`
	Remark           string    `json:"remark"`
	CreatedBy        string    `json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
}

// ConfigVersionListView 是链内版本分页列表响应。
type ConfigVersionListView struct {
	Items []ConfigVersionListItemView `json:"items"`
	Total int64                       `json:"total"`
}

// ConfigVersionDetailView 是版本详情（对齐 contracts ConfigVersionRow；content 已脱敏）。
type ConfigVersionDetailView struct {
	ID               uint      `json:"id"`
	ConfigFileID     uint      `json:"configFileId"`
	ScopeLevel       string    `json:"scopeLevel"`
	ScopeRefID       uint      `json:"scopeRefId"`
	VersionNo        int       `json:"versionNo"`
	Content          string    `json:"content"`
	ContentHash      string    `json:"contentHash"`
	IsRemoval        bool      `json:"isRemoval"`
	BasedOnVersionID *uint     `json:"basedOnVersionId"`
	Remark           string    `json:"remark"`
	CreatedBy        string    `json:"createdBy"`
	CreatedAt        time.Time `json:"createdAt"`
}

// ConfigSaveResultView 是保存 / 回退结果（对齐前端 SaveVersionResult）。
type ConfigSaveResultView struct {
	VersionID   uint   `json:"versionId"`
	VersionNo   int    `json:"versionNo"`
	ContentHash string `json:"contentHash"`
}

// ConfigRevokeResultView 是撤销层贡献结果（对齐前端 RevokeResult）。
type ConfigRevokeResultView struct {
	VersionID uint `json:"versionId"`
	VersionNo int  `json:"versionNo"`
	IsRemoval bool `json:"isRemoval"`
}

// ConfigValidateView 是实时校验响应（对齐 contracts ConfigValidateResponse）。
type ConfigValidateView struct {
	Valid  bool                     `json:"valid"`
	Errors []configschema.Violation `json:"errors"`
}

// ConfigProvenanceEntryView 是逐键来源（对齐 contracts ConfigProvenanceEntry）。
type ConfigProvenanceEntryView struct {
	Path       string `json:"path"`
	ScopeLevel string `json:"scopeLevel"`
	ScopeRefID uint   `json:"scopeRefId"`
	ScopeName  string `json:"scopeName"`
	VersionNo  int    `json:"versionNo"`
}

// ConfigDeletedKeyView 是被高层 null 删除的键（contracts ConfigDeletedKey；scopeName 为 P7c 契约收敛补充字段）。
type ConfigDeletedKeyView struct {
	Path       string `json:"path"`
	ScopeLevel string `json:"scopeLevel"`
	ScopeRefID uint   `json:"scopeRefId"`
	ScopeName  string `json:"scopeName"`
	VersionNo  int    `json:"versionNo"`
}

// ConfigEffectiveLayerView 是参与合并的一层摘要（对齐 contracts ConfigEffectiveResponse.layers 元素）。
type ConfigEffectiveLayerView struct {
	ScopeLevel    string  `json:"scopeLevel"`
	ScopeRefID    *uint   `json:"scopeRefId"`
	ScopeName     *string `json:"scopeName"`
	Contributing  bool    `json:"contributing"`
	HeadVersionNo *int    `json:"headVersionNo"`
	HeadHash      *string `json:"headHash"`
}

// ConfigEffectiveView 是有效配置预览响应（对齐 contracts ConfigEffectiveResponse；content 已脱敏、hash 基于明文）。
type ConfigEffectiveView struct {
	EffectiveContent string                      `json:"effectiveContent"`
	EffectiveHash    string                      `json:"effectiveHash"`
	Provenance       []ConfigProvenanceEntryView `json:"provenance"`
	DeletedKeys      []ConfigDeletedKeyView      `json:"deletedKeys"`
	Layers           []ConfigEffectiveLayerView  `json:"layers"`
}

// ConfigDiffEntryAdded / Removed / Changed 是键级差异项（对齐 contracts ConfigDiffResponse）。
type ConfigDiffEntryAdded struct {
	Path  string `json:"path"`
	Right string `json:"right"`
}

// ConfigDiffEntryRemoved 是右侧缺失的键。
type ConfigDiffEntryRemoved struct {
	Path string `json:"path"`
	Left string `json:"left"`
}

// ConfigDiffEntryChanged 是两侧同键不同值。
type ConfigDiffEntryChanged struct {
	Path  string `json:"path"`
	Left  string `json:"left"`
	Right string `json:"right"`
}

// ConfigDiffView 是键级 diff 响应（对齐 contracts ConfigDiffResponse；值已脱敏）。
type ConfigDiffView struct {
	Added       []ConfigDiffEntryAdded   `json:"added"`
	Removed     []ConfigDiffEntryRemoved `json:"removed"`
	Changed     []ConfigDiffEntryChanged `json:"changed"`
	UnifiedDiff string                   `json:"unifiedDiff"`
}
