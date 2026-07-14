package service

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/configschema"
	"github.com/wcpe/Beacon/apps/server/internal/merge"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// ConfigMaxContentBytes 是配置中心 V2 单版本内容上限（1 MiB，spec §3.3）。
const ConfigMaxContentBytes = 1 << 20

// ConfigCenterService 是配置中心 V2 的领域服务（FR-160/161）：
// 文件 CRUD 与回收站、不可变版本链（保存 / 回退 / 撤销）、五层有效解析与来源解释、diff、校验。
// 多表写一律在事务内原子完成并自记专项审计；读出口统一经敏感脱敏（spec §4.7）。
type ConfigCenterService struct {
	db        *gorm.DB
	files     *repository.ConfigFileRepository
	versions  *repository.ConfigLayerVersionRepository
	auditRepo *repository.AuditLogRepository
}

// NewConfigCenterService 构造服务。
func NewConfigCenterService(db *gorm.DB, files *repository.ConfigFileRepository,
	versions *repository.ConfigLayerVersionRepository, auditRepo *repository.AuditLogRepository) *ConfigCenterService {
	return &ConfigCenterService{db: db, files: files, versions: versions, auditRepo: auditRepo}
}

// findActiveFile 取未删除文件；不存在或已入回收站一律 CONFIG_FILE_NOT_FOUND（spec §4.9）。
func (s *ConfigCenterService) findActiveFile(id uint) (*model.ConfigFile, error) {
	file, err := s.files.FindByID(id, false)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, apperr.ErrConfigFileNotFound
	}
	return file, nil
}

// CreateConfigFileRequest 是创建文件请求。
type CreateConfigFileRequest struct {
	NamespaceID    uint
	Name           string
	Format         string
	Description    string
	SchemaJSON     string
	SensitivePaths []string
}

// CreateFile 创建配置文件（spec §5）：校验格式 / schema 合法性 / 名称唯一（仅对未删除生效），事务内自记审计。
func (s *ConfigCenterService) CreateFile(req CreateConfigFileRequest, operator, clientIP string) (*ConfigFileView, error) {
	if err := s.validateFileMeta(req.NamespaceID, req.Name, req.Format, req.SchemaJSON, req.SensitivePaths); err != nil {
		return nil, err
	}
	exists, err := s.files.ActiveNameExists(req.NamespaceID, req.Name, 0)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, apperr.ErrConfigFileDuplicate
	}
	file := &model.ConfigFile{
		NamespaceID: req.NamespaceID, Name: req.Name, Format: req.Format,
		Description: req.Description, SchemaJSON: req.SchemaJSON,
		SensitivePaths: encodeSensitivePaths(req.SensitivePaths), CreatedBy: operator,
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.files.WithTx(tx).Create(file); e != nil {
			return e
		}
		return s.auditConfigFile(tx, file, model.ActionConfigFileCreate, operator, clientIP, map[string]any{
			"name": file.Name, "format": file.Format, "sensitivePaths": decodeSensitivePaths(file.SensitivePaths),
		})
	})
	if err != nil {
		return nil, err
	}
	slog.Info("已创建配置文件", "文件", file.Name, "id", file.ID, "环境", file.NamespaceID, "操作人", operator)
	return configFileView(file), nil
}

// validateFileMeta 校验创建文件的元数据（namespace 存在 / 名称 / 格式 / schema / 敏感路径）。
func (s *ConfigCenterService) validateFileMeta(namespaceID uint, name, format, schemaJSON string, paths []string) error {
	if namespaceID == 0 || strings.TrimSpace(name) == "" || len(name) > 255 {
		return apperr.ErrInvalidParam
	}
	var count int64
	if err := s.db.Model(&model.Namespace{}).Where("id = ?", namespaceID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return apperr.New(http.StatusBadRequest, "INVALID_PARAM", "namespace 不存在")
	}
	if !merge.IsValidFormat(format) {
		return apperr.New(http.StatusBadRequest, "INVALID_PARAM", "format 必须为 yaml / json / properties")
	}
	return validateSchemaAndPaths(format, schemaJSON, paths)
}

// validateSchemaAndPaths 校验 schema 文本可编译与敏感路径形态（创建 / 元数据更新共用）。
func validateSchemaAndPaths(format, schemaJSON string, paths []string) error {
	if schemaJSON != "" {
		if _, err := configschema.Compile(format, schemaJSON); err != nil {
			return apperr.New(http.StatusBadRequest, "INVALID_PARAM", err.Error())
		}
	}
	for _, p := range paths {
		if strings.TrimSpace(p) == "" {
			return apperr.New(http.StatusBadRequest, "INVALID_PARAM", "敏感路径不能为空")
		}
	}
	return nil
}

// encodeSensitivePaths 把敏感路径列表编码为 json 文本落库（空列表落空串）。
func encodeSensitivePaths(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	raw, _ := json.Marshal(paths)
	return string(raw)
}

// UpdateConfigFileRequest 是元数据更新请求（nil 字段 = 不改；改敏感路径必须携带原因，spec §4.7）。
type UpdateConfigFileRequest struct {
	Description    *string
	SchemaJSON     *string
	SensitivePaths []string
	HasSensitive   bool
	Reason         string
}

// UpdateFile 更新描述 / schema / 敏感路径（spec §5 PATCH）：schema 非法 400、改敏感路径缺原因 400，事务内自记审计。
func (s *ConfigCenterService) UpdateFile(id uint, req UpdateConfigFileRequest, operator, clientIP string) (*ConfigFileView, error) {
	file, err := s.findActiveFile(id)
	if err != nil {
		return nil, err
	}
	if req.HasSensitive && strings.TrimSpace(req.Reason) == "" {
		return nil, apperr.New(http.StatusBadRequest, "missing_reason", "修改敏感路径必须填写原因")
	}
	changed := applyFileMetaChanges(file, req)
	if err := validateSchemaAndPaths(file.Format, file.SchemaJSON, decodeSensitivePaths(file.SensitivePaths)); err != nil {
		return nil, err
	}
	detail := map[string]any{"changed": changed}
	if req.HasSensitive {
		detail["reason"] = req.Reason
		detail["sensitivePaths"] = decodeSensitivePaths(file.SensitivePaths)
	}
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.files.WithTx(tx).Save(file); e != nil {
			return e
		}
		return s.auditConfigFile(tx, file, model.ActionConfigFileUpdate, operator, clientIP, detail)
	})
	if err != nil {
		return nil, err
	}
	return configFileView(file), nil
}

// applyFileMetaChanges 把请求中出现的字段落到实体，返回被改动的字段名列表（审计摘要用）。
func applyFileMetaChanges(file *model.ConfigFile, req UpdateConfigFileRequest) []string {
	changed := []string{}
	if req.Description != nil {
		file.Description = *req.Description
		changed = append(changed, "description")
	}
	if req.SchemaJSON != nil {
		file.SchemaJSON = *req.SchemaJSON
		changed = append(changed, "schemaJson")
	}
	if req.HasSensitive {
		file.SensitivePaths = encodeSensitivePaths(req.SensitivePaths)
		changed = append(changed, "sensitivePaths")
	}
	return changed
}

// TrashFile 把文件移入回收站（软删标记，版本链原样保留，spec §4.9），204 语义由 handler 承担。
func (s *ConfigCenterService) TrashFile(id uint, reason, operator, clientIP string) error {
	file, err := s.findActiveFile(id)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	file.DeletedAt = &now
	file.DeletedBy = operator
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.files.WithTx(tx).Save(file); e != nil {
			return e
		}
		return s.auditConfigFile(tx, file, model.ActionConfigFileTrash, operator, clientIP, map[string]any{"reason": reason})
	})
	if err != nil {
		return err
	}
	slog.Info("配置文件已移入回收站", "文件", file.Name, "id", file.ID, "操作人", operator)
	return nil
}

// RestoreFile 从回收站恢复（spec §4.9）：名称已被未删除文件占用 → 409 CONFIG_FILE_DUPLICATE。
func (s *ConfigCenterService) RestoreFile(id uint, operator, clientIP string) (*ConfigFileView, error) {
	file, err := s.files.FindByID(id, true)
	if err != nil {
		return nil, err
	}
	if file == nil || file.DeletedAt == nil {
		return nil, apperr.ErrConfigFileNotFound
	}
	occupied, err := s.files.ActiveNameExists(file.NamespaceID, file.Name, file.ID)
	if err != nil {
		return nil, err
	}
	if occupied {
		return nil, apperr.ErrConfigFileDuplicate
	}
	file.DeletedAt = nil
	file.DeletedBy = ""
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Save 全字段更新，可空软删标记随之落回 NULL
		if e := s.files.WithTx(tx).Save(file); e != nil {
			return e
		}
		return s.auditConfigFile(tx, file, model.ActionConfigFileRestore, operator, clientIP, nil)
	})
	if err != nil {
		return nil, err
	}
	slog.Info("配置文件已从回收站恢复", "文件", file.Name, "id", file.ID, "操作人", operator)
	return configFileView(file), nil
}

// PurgeFile 彻底删除（仅回收站内文件，物理删除连带版本链，spec §4.9）：
// 原因必填；审计 detail 记文件名 / format / 各链最终版本号与 hash 摘要（不含内容），保证事后可追溯。
func (s *ConfigCenterService) PurgeFile(id uint, reason, operator, clientIP string) error {
	file, err := s.files.FindByID(id, true)
	if err != nil {
		return err
	}
	if file == nil {
		return apperr.ErrConfigFileNotFound
	}
	if file.DeletedAt == nil {
		return apperr.ErrConfigFileNotTrashed
	}
	if strings.TrimSpace(reason) == "" {
		return apperr.New(http.StatusBadRequest, "missing_reason", "彻底删除必须填写原因")
	}
	heads, err := s.versions.HeadsByFile(file.ID)
	if err != nil {
		return err
	}
	detail := purgeAuditDetail(reason, heads)
	err = s.db.Transaction(func(tx *gorm.DB) error {
		if e := s.versions.WithTx(tx).DeleteByFileID(file.ID); e != nil {
			return e
		}
		if e := s.files.WithTx(tx).DeleteByID(file.ID); e != nil {
			return e
		}
		return s.auditConfigFile(tx, file, model.ActionConfigFilePurge, operator, clientIP, detail)
	})
	if err != nil {
		return err
	}
	slog.Warn("配置文件已彻底删除（连带版本链）", "文件", file.Name, "id", file.ID, "链数", len(heads), "操作人", operator)
	return nil
}

// purgeAuditDetail 组装 purge 审计摘要：各链最终版本号与 hash（不含任何内容明文）。
func purgeAuditDetail(reason string, heads []model.ConfigLayerVersion) map[string]any {
	chains := make([]map[string]any, 0, len(heads))
	for i := range heads {
		chains = append(chains, map[string]any{
			"scopeLevel": heads[i].ScopeLevel, "scopeRefId": heads[i].ScopeRefID,
			"finalVersionNo": heads[i].VersionNo, "headHash": heads[i].ContentHash, "isRemoval": heads[i].IsRemoval,
		})
	}
	return map[string]any{"reason": reason, "chains": chains}
}

// auditConfigFile 在事务内写一条配置文件域审计（detail 只落键路径级摘要，任何位置不落内容明文，spec §4.7）。
func (s *ConfigCenterService) auditConfigFile(tx *gorm.DB, file *model.ConfigFile, action, operator, clientIP string, detail map[string]any) error {
	entry := &model.AuditLog{
		Operator: operatorOrSystem(operator), Action: action,
		TargetType: model.TargetTypeConfigFile,
		TargetRef:  fmt.Sprintf("%d/%s", file.ID, file.Name),
		Result:     model.ResultOK, ClientIP: clientIP,
	}
	if detail != nil {
		raw, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		entry.Detail = string(raw)
	}
	return s.auditRepo.WithTx(tx).Create(entry)
}

// ConfigFileListQuery 是文件列表查询（spec §5）。
type ConfigFileListQuery struct {
	NamespaceID uint
	Keyword     string
	ServerRef   string
	Page        int
	PageSize    int
}

// ListFiles 分页列出未删除文件；带 serverRef 时只列对该 server 有生效贡献的文件并附有效 hash（spec §5）。
func (s *ConfigCenterService) ListFiles(q ConfigFileListQuery) (*ConfigFileListView, error) {
	if q.ServerRef != "" {
		return s.listFilesForServer(q)
	}
	files, total, err := s.files.ListActive(repository.ConfigFileFilter{
		NamespaceID: q.NamespaceID, Keyword: q.Keyword, Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		return nil, err
	}
	counts, err := s.contributingCounts(fileIDsOf(files))
	if err != nil {
		return nil, err
	}
	items := make([]ConfigFileItemView, 0, len(files))
	for i := range files {
		items = append(items, fileItemView(&files[i], counts[files[i].ID], ""))
	}
	return &ConfigFileListView{Items: items, Total: total}, nil
}

// listFilesForServer 按 server 过滤：只列有生效贡献的文件并附有效 hash（过滤先于分页）。
func (s *ConfigCenterService) listFilesForServer(q ConfigFileListQuery) (*ConfigFileListView, error) {
	chain, err := resolveTargetChain(s.db, q.NamespaceID, ConfigEffectiveTarget{ServerRef: q.ServerRef})
	if err != nil {
		return nil, err
	}
	files, err := s.files.ListActiveAll(q.NamespaceID, q.Keyword)
	if err != nil {
		return nil, err
	}
	headsByFile, counts, err := s.headsAndCounts(fileIDsOf(files))
	if err != nil {
		return nil, err
	}
	matched := make([]ConfigFileItemView, 0, len(files))
	for i := range files {
		item, ok, err := s.serverEffectiveItem(&files[i], chain, headsByFile[files[i].ID], counts[files[i].ID])
		if err != nil {
			return nil, err
		}
		if ok {
			matched = append(matched, item)
		}
	}
	items, total := paginateItems(matched, q.Page, q.PageSize)
	return &ConfigFileListView{Items: items, Total: total}, nil
}

// serverEffectiveItem 判断文件对该 server 链是否有生效贡献；有则合并算有效 hash 组装列表项。
func (s *ConfigCenterService) serverEffectiveItem(file *model.ConfigFile, chain []configScopeRef,
	heads map[configScopeRef]*model.ConfigLayerVersion, count int) (ConfigFileItemView, bool, error) {
	layered := make([]string, 0, len(chain))
	for _, ref := range chain {
		head := heads[ref]
		if head == nil || head.IsRemoval {
			continue
		}
		layered = append(layered, head.Content)
	}
	if len(layered) == 0 {
		return ConfigFileItemView{}, false, nil
	}
	effective, err := merge.MergeDataID(file.Format, layered)
	if err != nil {
		return ConfigFileItemView{}, false, fmt.Errorf("合并文件 %s 的有效配置失败: %w", file.Name, err)
	}
	return fileItemView(file, count, merge.Sha256Hex(effective)), true, nil
}

// fileItemView 组装列表项视图。
func fileItemView(f *model.ConfigFile, contributing int, effectiveHash string) ConfigFileItemView {
	return ConfigFileItemView{
		ID: f.ID, NamespaceID: f.NamespaceID, Name: f.Name, Format: f.Format,
		Description: f.Description, ContributingLayerCount: contributing,
		UpdatedAt: f.UpdatedAt, EffectiveHash: effectiveHash,
	}
}

// fileIDsOf 收集文件 id 列表。
func fileIDsOf(files []model.ConfigFile) []uint {
	ids := make([]uint, 0, len(files))
	for i := range files {
		ids = append(ids, files[i].ID)
	}
	return ids
}

// contributingCounts 批量统计各文件的有贡献链数（head 非撤销）。
func (s *ConfigCenterService) contributingCounts(fileIDs []uint) (map[uint]int, error) {
	heads, err := s.versions.HeadsByFileIDs(fileIDs)
	if err != nil {
		return nil, err
	}
	counts := map[uint]int{}
	for i := range heads {
		if !heads[i].IsRemoval {
			counts[heads[i].ConfigFileID]++
		}
	}
	return counts, nil
}

// headsAndCounts 批量取各文件全链 head 映射与有贡献链数（一次查询两用）。
func (s *ConfigCenterService) headsAndCounts(fileIDs []uint) (map[uint]map[configScopeRef]*model.ConfigLayerVersion, map[uint]int, error) {
	heads, err := s.versions.HeadsByFileIDs(fileIDs)
	if err != nil {
		return nil, nil, err
	}
	byFile := map[uint]map[configScopeRef]*model.ConfigLayerVersion{}
	counts := map[uint]int{}
	for i := range heads {
		head := &heads[i]
		if byFile[head.ConfigFileID] == nil {
			byFile[head.ConfigFileID] = map[configScopeRef]*model.ConfigLayerVersion{}
		}
		byFile[head.ConfigFileID][configScopeRef{Level: head.ScopeLevel, RefID: head.ScopeRefID}] = head
		if !head.IsRemoval {
			counts[head.ConfigFileID]++
		}
	}
	return byFile, counts, nil
}

// paginateItems 对内存过滤后的列表分页（server 过滤须先全量过滤再分页）。
func paginateItems(items []ConfigFileItemView, page, pageSize int) ([]ConfigFileItemView, int64) {
	total := int64(len(items))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	start := (page - 1) * pageSize
	if start >= len(items) {
		return []ConfigFileItemView{}, total
	}
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], total
}

// ListTrash 分页列出回收站文件（spec §4.9/§5）。
func (s *ConfigCenterService) ListTrash(q ConfigFileListQuery) (*ConfigTrashListView, error) {
	files, total, err := s.files.ListTrash(repository.ConfigFileFilter{
		NamespaceID: q.NamespaceID, Keyword: q.Keyword, Page: q.Page, PageSize: q.PageSize,
	})
	if err != nil {
		return nil, err
	}
	items := make([]ConfigTrashItemView, 0, len(files))
	for i := range files {
		f := &files[i]
		item := ConfigTrashItemView{ID: f.ID, NamespaceID: f.NamespaceID, Name: f.Name, Format: f.Format, DeletedAt: f.DeletedAt}
		if f.DeletedBy != "" {
			by := f.DeletedBy
			item.DeletedBy = &by
		}
		items = append(items, item)
	}
	return &ConfigTrashListView{Items: items, Total: total}, nil
}

// GetFileDetail 取文件元数据 + 各层覆盖概览（spec §5 GET {id}）。
func (s *ConfigCenterService) GetFileDetail(id uint) (*ConfigFileDetailView, error) {
	file, err := s.findActiveFile(id)
	if err != nil {
		return nil, err
	}
	summaries, err := s.scopeSummaries(file)
	if err != nil {
		return nil, err
	}
	scopes := make([]ConfigFileScopeOverview, 0, len(summaries))
	for _, sum := range summaries {
		scopes = append(scopes, ConfigFileScopeOverview{
			ScopeLevel: sum.ScopeLevel, ScopeRefID: sum.ScopeRefID, ScopeName: sum.ScopeName,
			HeadVersionNo: sum.HeadVersionNo, IsRemoval: sum.IsRemoval,
		})
	}
	return &ConfigFileDetailView{ConfigFileView: *configFileView(file), Scopes: scopes}, nil
}

// GetScopes 取各贡献链概览（spec §5 GET {id}/scopes）。
func (s *ConfigCenterService) GetScopes(id uint) (*ConfigScopesView, error) {
	file, err := s.findActiveFile(id)
	if err != nil {
		return nil, err
	}
	summaries, err := s.scopeSummaries(file)
	if err != nil {
		return nil, err
	}
	return &ConfigScopesView{Scopes: summaries}, nil
}

// scopeSummaries 组装文件全部链的概览（按低→高层序 + refId 稳定排序，批量取名防 N+1）。
func (s *ConfigCenterService) scopeSummaries(file *model.ConfigFile) ([]ConfigScopeSummaryView, error) {
	heads, err := s.versions.HeadsByFile(file.ID)
	if err != nil {
		return nil, err
	}
	refs := make([]configScopeRef, 0, len(heads))
	for i := range heads {
		refs = append(refs, configScopeRef{Level: heads[i].ScopeLevel, RefID: heads[i].ScopeRefID})
	}
	names, err := resolveScopeNames(s.db, refs)
	if err != nil {
		return nil, err
	}
	out := make([]ConfigScopeSummaryView, 0, len(heads))
	for i := range heads {
		h := &heads[i]
		out = append(out, ConfigScopeSummaryView{
			ScopeLevel: h.ScopeLevel, ScopeRefID: h.ScopeRefID,
			ScopeName:     names[configScopeRef{Level: h.ScopeLevel, RefID: h.ScopeRefID}],
			HeadVersionNo: h.VersionNo, HeadHash: h.ContentHash, IsRemoval: h.IsRemoval,
			UpdatedBy: h.CreatedBy, UpdatedAt: h.CreatedAt,
		})
	}
	sortScopeSummaries(out)
	return out, nil
}

// sortScopeSummaries 按作用域层序（低→高）+ refId 稳定排序。
func sortScopeSummaries(list []ConfigScopeSummaryView) {
	order := map[string]int{}
	for i, level := range model.ConfigScopeLevelsLowToHigh {
		order[level] = i
	}
	sort.Slice(list, func(i, j int) bool {
		if order[list[i].ScopeLevel] != order[list[j].ScopeLevel] {
			return order[list[i].ScopeLevel] < order[list[j].ScopeLevel]
		}
		return list[i].ScopeRefID < list[j].ScopeRefID
	})
}
