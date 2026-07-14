package service

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/configschema"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// ConfigSchemaViolationError 携带逐条违例的 schema 校验失败（对外 400 CONFIG_SCHEMA_VIOLATION +
// errors:[{path,message}]，spec §4.4；由 handler 识别后写出扩展错误体）。
type ConfigSchemaViolationError struct {
	Violations []configschema.Violation
}

// Error 实现 error 接口（日志用摘要，不含内容明文）。
func (e *ConfigSchemaViolationError) Error() string {
	return fmt.Sprintf("配置内容未通过 schema 校验（%d 条违例）", len(e.Violations))
}

// SaveVersionRequest 是保存新版本请求（spec §4.2）。
type SaveVersionRequest struct {
	ScopeLevel       string
	ScopeRefID       uint
	Content          string
	Remark           string
	BasedOnVersionID *uint
}

// SaveVersion 保存即定稿新的不可变版本（spec §4.2 七步）：
// scope 校验 → 大小 → 语法 → 敏感回填 → schema → 乐观并发 → 归一化判无变化 → 事务内 max+1 插入 + 审计。
func (s *ConfigCenterService) SaveVersion(fileID uint, req SaveVersionRequest, operator, clientIP string) (*ConfigSaveResultView, error) {
	file, err := s.findActiveFile(fileID)
	if err != nil {
		return nil, err
	}
	if err := validateScopeBelongs(s.db, file, req.ScopeLevel, req.ScopeRefID); err != nil {
		return nil, err
	}
	if len(req.Content) > ConfigMaxContentBytes {
		return nil, apperr.ErrConfigContentTooLarge
	}
	parsed, err := parseConfigContent(file.Format, req.Content)
	if err != nil {
		return nil, err
	}
	head, err := s.versions.Head(fileID, req.ScopeLevel, req.ScopeRefID)
	if err != nil {
		return nil, err
	}
	headParsed, err := parseHeadContent(file.Format, head)
	if err != nil {
		return nil, err
	}
	if err := backfillSensitivePlaceholders(parsed, headParsed, decodeSensitivePaths(file.SensitivePaths)); err != nil {
		return nil, err
	}
	if err := validateAgainstSchema(file, parsed, req.ScopeLevel); err != nil {
		return nil, err
	}
	if err := checkBasedOnVersion(head, req.BasedOnVersionID); err != nil {
		return nil, err
	}
	normalized, hash, err := normalizeContent(file.Format, parsed)
	if err != nil {
		return nil, err
	}
	if head != nil && !head.IsRemoval && head.ContentHash == hash {
		return nil, apperr.ErrConfigNoChange
	}
	version := &model.ConfigLayerVersion{
		ConfigFileID: fileID, ScopeLevel: req.ScopeLevel, ScopeRefID: req.ScopeRefID,
		Content: normalized, ContentHash: hash, Remark: req.Remark, CreatedBy: operator,
	}
	if head != nil {
		id := head.ID
		version.BasedOnVersionID = &id
	}
	detail := saveAuditDetail(headParsed, parsed, req.ScopeLevel, req.ScopeRefID)
	if err := s.appendVersion(file, version, model.ActionConfigVersionSave, operator, clientIP, detail); err != nil {
		return nil, err
	}
	return &ConfigSaveResultView{VersionID: version.ID, VersionNo: version.VersionNo, ContentHash: version.ContentHash}, nil
}

// RollbackVersion 回退 = 基于历史版本生成新版本（spec §4.6）：内容取历史版本、based_on 指向它；
// 同样过 schema（可能已收紧则阻断）与无变化校验。
func (s *ConfigCenterService) RollbackVersion(versionID uint, remark, operator, clientIP string) (*ConfigSaveResultView, error) {
	source, err := s.versions.FindByID(versionID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, apperr.ErrConfigVersionNotFound
	}
	file, err := s.findActiveFile(source.ConfigFileID)
	if err != nil {
		return nil, err
	}
	if source.IsRemoval {
		return nil, apperr.New(http.StatusBadRequest, "INVALID_PARAM", "撤销版本不可作为回退来源")
	}
	parsed, err := parseConfigContent(file.Format, source.Content)
	if err != nil {
		return nil, err
	}
	if err := validateAgainstSchema(file, parsed, source.ScopeLevel); err != nil {
		return nil, err
	}
	head, err := s.versions.Head(source.ConfigFileID, source.ScopeLevel, source.ScopeRefID)
	if err != nil {
		return nil, err
	}
	if head != nil && !head.IsRemoval && head.ContentHash == source.ContentHash {
		return nil, apperr.ErrConfigNoChange
	}
	if strings.TrimSpace(remark) == "" {
		remark = fmt.Sprintf("回退自 v%d", source.VersionNo)
	}
	sourceID := source.ID
	version := &model.ConfigLayerVersion{
		ConfigFileID: source.ConfigFileID, ScopeLevel: source.ScopeLevel, ScopeRefID: source.ScopeRefID,
		Content: source.Content, ContentHash: source.ContentHash,
		BasedOnVersionID: &sourceID, Remark: remark, CreatedBy: operator,
	}
	detail := map[string]any{"scopeLevel": source.ScopeLevel, "scopeRefId": source.ScopeRefID, "fromVersionNo": source.VersionNo}
	if err := s.appendVersion(file, version, model.ActionConfigVersionRollback, operator, clientIP, detail); err != nil {
		return nil, err
	}
	return &ConfigSaveResultView{VersionID: version.ID, VersionNo: version.VersionNo, ContentHash: version.ContentHash}, nil
}

// RemoveScopeContribution 撤销某层贡献（spec §4.6）：追加 is_removal=true 的空内容版本；
// head 已是撤销或链不存在则 400；原因必填并入审计。
func (s *ConfigCenterService) RemoveScopeContribution(fileID uint, scopeLevel string, scopeRefID uint, reason, operator, clientIP string) (*ConfigRevokeResultView, error) {
	file, err := s.findActiveFile(fileID)
	if err != nil {
		return nil, err
	}
	if err := validateScopeBelongs(s.db, file, scopeLevel, scopeRefID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(reason) == "" {
		return nil, apperr.New(http.StatusBadRequest, "missing_reason", "撤销层贡献必须填写原因")
	}
	head, err := s.versions.Head(fileID, scopeLevel, scopeRefID)
	if err != nil {
		return nil, err
	}
	if head == nil || head.IsRemoval {
		return nil, apperr.New(http.StatusBadRequest, "INVALID_PARAM", "该层无可撤销的贡献")
	}
	headID := head.ID
	version := &model.ConfigLayerVersion{
		ConfigFileID: fileID, ScopeLevel: scopeLevel, ScopeRefID: scopeRefID,
		Content: "", ContentHash: merge.Sha256Hex(""), IsRemoval: true,
		BasedOnVersionID: &headID, Remark: "撤销层贡献：" + reason, CreatedBy: operator,
	}
	detail := map[string]any{"scopeLevel": scopeLevel, "scopeRefId": scopeRefID, "reason": reason}
	if err := s.appendVersion(file, version, model.ActionConfigScopeRemove, operator, clientIP, detail); err != nil {
		return nil, err
	}
	return &ConfigRevokeResultView{VersionID: version.ID, VersionNo: version.VersionNo, IsRemoval: true}, nil
}

// appendVersion 事务内取链 max(version_no)+1 追加版本并自记审计；
// 唯一索引兜底并发插入，冲突（gorm.ErrDuplicatedKey）映射为 409 CONFIG_VERSION_CONFLICT。
func (s *ConfigCenterService) appendVersion(file *model.ConfigFile, version *model.ConfigLayerVersion,
	action, operator, clientIP string, detail map[string]any) error {
	err := s.db.Transaction(func(tx *gorm.DB) error {
		maxNo, e := maxVersionNo(tx, version.ConfigFileID, version.ScopeLevel, version.ScopeRefID)
		if e != nil {
			return e
		}
		version.VersionNo = maxNo + 1
		if e := s.versions.WithTx(tx).Insert(version); e != nil {
			return e
		}
		// 触达文件更新时间：列表「更新时间」以最近一次链变更为准
		if e := tx.Model(&model.ConfigFile{}).Where("id = ?", file.ID).
			Update("updated_at", nowUTC(tx)).Error; e != nil {
			return e
		}
		if detail == nil {
			detail = map[string]any{}
		}
		detail["versionNo"] = version.VersionNo
		return s.auditConfigFile(tx, file, action, operator, clientIP, detail)
	})
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return apperr.ErrConfigVersionConflict
	}
	return err
}

// nowUTC 取事务同款 UTC 当前时间（store.Open 已把 NowFunc 固定为 UTC）。
func nowUTC(tx *gorm.DB) any {
	return tx.NowFunc()
}

// maxVersionNo 事务内取链当前最大版本号（链空得 0）。
func maxVersionNo(tx *gorm.DB, fileID uint, scopeLevel string, scopeRefID uint) (int, error) {
	var maxNo int
	err := tx.Model(&model.ConfigLayerVersion{}).
		Where("config_file_id = ? AND scope_level = ? AND scope_ref_id = ?", fileID, scopeLevel, scopeRefID).
		Select("COALESCE(MAX(version_no), 0)").Scan(&maxNo).Error
	return maxNo, err
}

// parseConfigContent 语法解析门（spec §4.2 步骤 2）：按格式 parse，失败 / 空内容 / 顶层非键值映射 /
// 存在非字符串键均报 CONFIG_SYNTAX_INVALID（含原因）。
func parseConfigContent(format, content string) (map[string]any, error) {
	parsed, err := merge.Parse(format, content)
	if err != nil {
		return nil, apperr.New(http.StatusBadRequest, "CONFIG_SYNTAX_INVALID", err.Error())
	}
	if parsed == nil {
		return nil, apperr.New(http.StatusBadRequest, "CONFIG_SYNTAX_INVALID", "配置内容为空；撤销该层贡献请用撤销操作")
	}
	root, ok := parsed.(map[string]any)
	if !ok {
		return nil, apperr.New(http.StatusBadRequest, "CONFIG_SYNTAX_INVALID", "配置顶层必须是键值映射，不能是标量或列表")
	}
	if err := rejectInvalidKeys(root); err != nil {
		return nil, err
	}
	return root, nil
}

// rejectInvalidKeys 递归拒绝空键与非字符串键（merge 全链路只处理字符串键，序列化亦无法承载）。
func rejectInvalidKeys(m map[string]any) error {
	for key, value := range m {
		if strings.TrimSpace(key) == "" {
			return apperr.New(http.StatusBadRequest, "CONFIG_SYNTAX_INVALID", "配置存在空键或仅空白的键")
		}
		switch child := value.(type) {
		case map[string]any:
			if err := rejectInvalidKeys(child); err != nil {
				return err
			}
		case map[any]any:
			return apperr.New(http.StatusBadRequest, "CONFIG_SYNTAX_INVALID",
				fmt.Sprintf("配置键必须是字符串：%s 下存在非字符串键", key))
		}
	}
	return nil
}

// parseHeadContent 解析链 head 内容供敏感回填 / 审计摘要（链空或 head 为撤销版本得 nil）。
func parseHeadContent(format string, head *model.ConfigLayerVersion) (any, error) {
	if head == nil || head.IsRemoval {
		return nil, nil
	}
	parsed, err := merge.Parse(format, head.Content)
	if err != nil {
		return nil, fmt.Errorf("解析链 head 内容失败: %w", err)
	}
	return parsed, nil
}

// validateAgainstSchema 按文件 schema 校验内容（无 schema 只做语法校验；namespace 层为基线强制 required）。
func validateAgainstSchema(file *model.ConfigFile, parsed any, scopeLevel string) error {
	if file.SchemaJSON == "" {
		return nil
	}
	validator, err := configschema.Compile(file.Format, file.SchemaJSON)
	if err != nil {
		// 已落库的 schema 理应可编译；防御性上抛为内部错误（经 render 脱敏出口展示真因）
		return fmt.Errorf("编译文件 schema 失败: %w", err)
	}
	violations := validator.Validate(parsed, scopeLevel == model.ConfigScopeNamespace)
	if len(violations) > 0 {
		return &ConfigSchemaViolationError{Violations: violations}
	}
	return nil
}

// checkBasedOnVersion 乐观并发校验（spec §4.2 步骤 5）：basedOnVersionId 必须等于链当前 head id（链空传 null）。
func checkBasedOnVersion(head *model.ConfigLayerVersion, basedOn *uint) error {
	switch {
	case head == nil && basedOn == nil:
		return nil
	case head != nil && basedOn != nil && head.ID == *basedOn:
		return nil
	default:
		return apperr.ErrConfigVersionConflict
	}
}

// normalizeContent 归一化（spec §4.2 步骤 6）：parse 后按固定键序规范序列化并算 sha256。
func normalizeContent(format string, parsed map[string]any) (string, string, error) {
	normalized, err := merge.Serialize(format, parsed)
	if err != nil {
		return "", "", apperr.New(http.StatusBadRequest, "CONFIG_SYNTAX_INVALID", err.Error())
	}
	return normalized, merge.Sha256Hex(normalized), nil
}

// saveAuditDetail 组装保存审计的键路径级摘要（新增 / 修改 / 删除了哪些路径；不含任何值，spec §4.7）。
func saveAuditDetail(headParsed any, newParsed map[string]any, scopeLevel string, scopeRefID uint) map[string]any {
	oldLeaves := flattenLeafValues(headParsed)
	newLeaves := flattenLeafValues(newParsed)
	added, changed, removed := diffLeafPaths(oldLeaves, newLeaves)
	return map[string]any{
		"scopeLevel": scopeLevel, "scopeRefId": scopeRefID,
		"addedPaths": added, "changedPaths": changed, "removedPaths": removed,
	}
}

// diffLeafPaths 比较两侧叶子映射，产出有序的新增 / 变更 / 删除路径列表（只含路径不含值）。
func diffLeafPaths(oldLeaves, newLeaves map[string]string) (added, changed, removed []string) {
	added, changed, removed = []string{}, []string{}, []string{}
	for path, value := range newLeaves {
		oldValue, ok := oldLeaves[path]
		if !ok {
			added = append(added, path)
		} else if oldValue != value {
			changed = append(changed, path)
		}
	}
	for path := range oldLeaves {
		if _, ok := newLeaves[path]; !ok {
			removed = append(removed, path)
		}
	}
	sort.Strings(added)
	sort.Strings(changed)
	sort.Strings(removed)
	return added, changed, removed
}

// ValidateContent 编辑器实时校验（spec §5 validate）：与保存用同一引擎，只回结果、不落库不审计。
// 无 scopeRefId 可回填敏感占位符，占位符按字面字符串参与校验。
func (s *ConfigCenterService) ValidateContent(fileID uint, scopeLevel, content string) (*ConfigValidateView, error) {
	file, err := s.findActiveFile(fileID)
	if err != nil {
		return nil, err
	}
	if len(content) > ConfigMaxContentBytes {
		return invalidView(rootViolation("配置内容超出 1 MiB 上限")), nil
	}
	parsed, err := parseConfigContent(file.Format, content)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return invalidView(rootViolation(ae.Message)), nil
		}
		return nil, err
	}
	if err := validateAgainstSchema(file, parsed, scopeLevel); err != nil {
		var sve *ConfigSchemaViolationError
		if errors.As(err, &sve) {
			return invalidView(sve.Violations), nil
		}
		return nil, err
	}
	return &ConfigValidateView{Valid: true, Errors: []configschema.Violation{}}, nil
}

// rootViolation 构造单条根级违例。
func rootViolation(message string) []configschema.Violation {
	return []configschema.Violation{{Path: "(root)", Message: message}}
}

// invalidView 组装校验不通过响应。
func invalidView(violations []configschema.Violation) *ConfigValidateView {
	return &ConfigValidateView{Valid: false, Errors: violations}
}

// ListVersions 分页列出某链版本（新 → 旧，spec §5）。
func (s *ConfigCenterService) ListVersions(fileID uint, scopeLevel string, scopeRefID uint, page, pageSize int) (*ConfigVersionListView, error) {
	if _, err := s.findActiveFile(fileID); err != nil {
		return nil, err
	}
	if !model.IsValidConfigScopeLevel(scopeLevel) || scopeRefID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	versions, total, err := s.versions.ListChain(fileID, scopeLevel, scopeRefID, page, pageSize)
	if err != nil {
		return nil, err
	}
	items := make([]ConfigVersionListItemView, 0, len(versions))
	for i := range versions {
		v := &versions[i]
		items = append(items, ConfigVersionListItemView{
			VersionID: v.ID, VersionNo: v.VersionNo, ContentHash: v.ContentHash, IsRemoval: v.IsRemoval,
			BasedOnVersionID: v.BasedOnVersionID, Remark: v.Remark, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt,
		})
	}
	return &ConfigVersionListView{Items: items, Total: total}, nil
}

// GetVersion 取版本详情（content 经敏感脱敏，spec §5）。
func (s *ConfigCenterService) GetVersion(versionID uint) (*ConfigVersionDetailView, error) {
	version, err := s.versions.FindByID(versionID)
	if err != nil {
		return nil, err
	}
	if version == nil {
		return nil, apperr.ErrConfigVersionNotFound
	}
	file, err := s.findActiveFile(version.ConfigFileID)
	if err != nil {
		return nil, err
	}
	masked, err := maskContentText(file.Format, version.Content, decodeSensitivePaths(file.SensitivePaths))
	if err != nil {
		return nil, err
	}
	return &ConfigVersionDetailView{
		ID: version.ID, ConfigFileID: version.ConfigFileID,
		ScopeLevel: version.ScopeLevel, ScopeRefID: version.ScopeRefID, VersionNo: version.VersionNo,
		Content: masked, ContentHash: version.ContentHash, IsRemoval: version.IsRemoval,
		BasedOnVersionID: version.BasedOnVersionID, Remark: version.Remark,
		CreatedBy: version.CreatedBy, CreatedAt: version.CreatedAt,
	}, nil
}
