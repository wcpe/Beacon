package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// EnvService 编排 env 展示维度（FR-178，见 v2-zone-authority.md §3.4/§4.1）。
// env 是纯展示 / 过滤维度：不参与隔离判定、不参与调度、不进配置作用域链；
// env→namespace 映射为整体替换语义，一个 namespace 至多属于一个 env（冲突 409 指明冲突方）。
type EnvService struct {
	db        *gorm.DB
	repo      *repository.EnvRepository
	nsRepo    *repository.NamespaceRepository
	auditRepo *repository.AuditLogRepository
}

// NewEnvService 构造服务。nsRepo 供解析映射 namespace 名与校验存在性（namespace 数量小，一次全量取无 N+1 顾虑）。
func NewEnvService(
	db *gorm.DB,
	repo *repository.EnvRepository,
	nsRepo *repository.NamespaceRepository,
	auditRepo *repository.AuditLogRepository,
) *EnvService {
	return &EnvService{db: db, repo: repo, nsRepo: nsRepo, auditRepo: auditRepo}
}

// EnvNamespaceRef 是 env 映射到的单个 namespace 摘要（id + 展示名，名取 namespace.code 与 /namespaces 列表口径一致）。
type EnvNamespaceRef struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

// EnvView 是 env 对外视图（camelCase），含映射的 namespace 摘要。
type EnvView struct {
	ID             uint              `json:"id"`
	Name           string            `json:"name"`
	Description    string            `json:"description"`
	Namespaces     []EnvNamespaceRef `json:"namespaces"`
	NamespaceCount int               `json:"namespaceCount"`
	CreatedAt      time.Time         `json:"createdAt"`
	UpdatedAt      time.Time         `json:"updatedAt"`
}

// List 返回全部 env 及其映射的 namespace 摘要：一次取 env / 映射 / namespace 三表内存拼装，禁循环内查库（N+1）。
func (s *EnvService) List() ([]EnvView, error) {
	envs, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	mappings, err := s.repo.ListMappings()
	if err != nil {
		return nil, err
	}
	nameByID, err := s.namespaceNamesByID()
	if err != nil {
		return nil, err
	}
	refsByEnv := groupNamespaceRefs(mappings, nameByID)
	views := make([]EnvView, 0, len(envs))
	for i := range envs {
		views = append(views, buildEnvView(&envs[i], refsByEnv[envs[i].ID]))
	}
	return views, nil
}

// Create 新建 env；名为空返回参数错误，同名返回冲突。写入与审计在同一事务内原子完成。
func (s *EnvService) Create(name, description, operator, clientIP string) (*EnvView, error) {
	if strings.TrimSpace(name) == "" {
		return nil, apperr.ErrInvalidParam
	}
	exist, err := s.repo.FindByName(name)
	if err != nil {
		return nil, err
	}
	if exist != nil {
		return nil, apperr.ErrEnvConflict
	}
	env := &model.Env{Name: name, Description: description}
	detail, _ := json.Marshal(map[string]string{"name": name})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.repo.WithTx(tx).Create(env); err != nil {
			return err
		}
		return s.writeAudit(tx, model.ActionEnvCreate, env.ID, string(detail), operator, clientIP)
	}); err != nil {
		return nil, err
	}
	slog.Info("新建 env", "id", env.ID, "name", name, "operator", operatorOrSystem(operator))
	return &EnvView{ID: env.ID, Name: env.Name, Description: env.Description, Namespaces: []EnvNamespaceRef{}, CreatedAt: env.CreatedAt, UpdatedAt: env.UpdatedAt}, nil
}

// Update 改 env 名 / 描述（PATCH 语义，name / description 均可选，nil 表示不改）；env 不存在返回 NOT_FOUND，改名撞名返回冲突。
func (s *EnvService) Update(id uint, name, description *string, operator, clientIP string) (*EnvView, error) {
	if id == 0 {
		return nil, apperr.ErrInvalidParam
	}
	env, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if env == nil {
		return nil, apperr.ErrEnvNotFound
	}
	if err := s.applyEnvNameChange(env, name); err != nil {
		return nil, err
	}
	if description != nil {
		env.Description = *description
	}
	detail, _ := json.Marshal(map[string]string{"name": env.Name, "description": env.Description})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.repo.WithTx(tx).Save(env); err != nil {
			return err
		}
		return s.writeAudit(tx, model.ActionEnvUpdate, env.ID, string(detail), operator, clientIP)
	}); err != nil {
		return nil, err
	}
	slog.Info("更新 env", "id", env.ID, "name", env.Name, "operator", operatorOrSystem(operator))
	return s.viewOf(env)
}

// applyEnvNameChange 处理改名：nil 或空白不改；改为新值前校验撞名（唯一）。
func (s *EnvService) applyEnvNameChange(env *model.Env, name *string) error {
	if name == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*name)
	if trimmed == "" {
		return apperr.ErrInvalidParam
	}
	if trimmed == env.Name {
		return nil
	}
	other, err := s.repo.FindByName(trimmed)
	if err != nil {
		return err
	}
	if other != nil && other.ID != env.ID {
		return apperr.ErrEnvConflict
	}
	env.Name = trimmed
	return nil
}

// Delete 删 env（硬删）；env 不存在返回 NOT_FOUND。删 env 不受映射保护——映射行级联删除、
// env 消失只影响前端过滤视图不影响任何权威数据（spec §4.1）。级联删除 + env 删除 + 审计在同一事务内原子完成。
func (s *EnvService) Delete(id uint, operator, clientIP string) error {
	if id == 0 {
		return apperr.ErrInvalidParam
	}
	env, err := s.repo.FindByID(id)
	if err != nil {
		return err
	}
	if env == nil {
		return apperr.ErrEnvNotFound
	}
	detail, _ := json.Marshal(map[string]string{"name": env.Name})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		txRepo := s.repo.WithTx(tx)
		if err := txRepo.DeleteMappingsByEnv(id); err != nil {
			return err
		}
		if err := txRepo.DeleteByID(id); err != nil {
			return err
		}
		return s.writeAudit(tx, model.ActionEnvDelete, id, string(detail), operator, clientIP)
	}); err != nil {
		return err
	}
	slog.Info("删除 env", "id", id, "name", env.Name, "operator", operatorOrSystem(operator))
	return nil
}

// SetNamespaces 整体替换 env→namespace 映射（PUT 幂等，spec §4.1）：事务内先删该 env 全部映射再插新集合。
// 被其他 env 占用的 namespace 报 409 并指明冲突方；不存在的 namespace 报 400。映射变更入审计。
func (s *EnvService) SetNamespaces(id uint, namespaceIDs []uint, operator, clientIP string) (*EnvView, error) {
	if id == 0 {
		return nil, apperr.ErrInvalidParam
	}
	env, err := s.repo.FindByID(id)
	if err != nil {
		return nil, err
	}
	if env == nil {
		return nil, apperr.ErrEnvNotFound
	}
	wanted := dedupeUints(namespaceIDs)
	nameByID, err := s.namespaceNamesByID()
	if err != nil {
		return nil, err
	}
	if err := ensureNamespaceRefsExist(wanted, nameByID); err != nil {
		return nil, err
	}
	if err := s.ensureNoEnvConflict(id, wanted, nameByID); err != nil {
		return nil, err
	}
	detail, _ := json.Marshal(map[string]any{"namespaceIds": wanted})
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		txRepo := s.repo.WithTx(tx)
		if err := txRepo.DeleteMappingsByEnv(id); err != nil {
			return err
		}
		if err := txRepo.CreateMappings(buildMappingRows(id, wanted)); err != nil {
			// 唯一索引兜底：并发下另一 env 抢先占用某 namespace，插入撞唯一约束也归一为映射冲突。
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				return apperr.ErrEnvNamespaceConflict
			}
			return err
		}
		return s.writeAudit(tx, model.ActionEnvSetNamespaces, id, string(detail), operator, clientIP)
	}); err != nil {
		return nil, err
	}
	slog.Info("整体替换 env 映射", "id", id, "name", env.Name, "namespaceCount", len(wanted), "operator", operatorOrSystem(operator))
	refs := make([]EnvNamespaceRef, 0, len(wanted))
	for _, nsID := range wanted {
		refs = append(refs, EnvNamespaceRef{ID: nsID, Name: nameByID[nsID]})
	}
	return &EnvView{ID: env.ID, Name: env.Name, Description: env.Description, Namespaces: refs, NamespaceCount: len(refs), CreatedAt: env.CreatedAt, UpdatedAt: env.UpdatedAt}, nil
}

// ensureNoEnvConflict 校验待映射 namespace 未被其他 env 占用；命中则构造同码 409 错误并指明冲突方（namespace 名 + 占用 env 名）。
func (s *EnvService) ensureNoEnvConflict(envID uint, namespaceIDs []uint, nameByID map[uint]string) error {
	rows, err := s.repo.FindMappingsByNamespaceIDs(namespaceIDs)
	if err != nil {
		return err
	}
	envNameByID, err := s.envNamesByID()
	if err != nil {
		return err
	}
	conflicts := make([]string, 0)
	for i := range rows {
		if rows[i].EnvID == envID {
			continue
		}
		conflicts = append(conflicts, fmt.Sprintf("%s（env「%s」）", nameByID[rows[i].NamespaceID], envNameByID[rows[i].EnvID]))
	}
	if len(conflicts) == 0 {
		return nil
	}
	sort.Strings(conflicts)
	return apperr.New(http.StatusConflict, apperr.ErrEnvNamespaceConflict.Code,
		"以下 namespace 已归属其他 env，请先从对方移除："+strings.Join(conflicts, "、"))
}

// namespaceNamesByID 一次取全部 namespace 建 id→name 映射（名取 code，与 v2 /namespaces 列表口径一致）。
func (s *EnvService) namespaceNamesByID() (map[uint]string, error) {
	namespaces, err := s.nsRepo.List()
	if err != nil {
		return nil, err
	}
	nameByID := make(map[uint]string, len(namespaces))
	for i := range namespaces {
		nameByID[namespaces[i].ID] = namespaces[i].Code
	}
	return nameByID, nil
}

// envNamesByID 一次取全部 env 建 id→name 映射（冲突提示解析占用 env 名）。
func (s *EnvService) envNamesByID() (map[uint]string, error) {
	envs, err := s.repo.List()
	if err != nil {
		return nil, err
	}
	nameByID := make(map[uint]string, len(envs))
	for i := range envs {
		nameByID[envs[i].ID] = envs[i].Name
	}
	return nameByID, nil
}

// viewOf 补齐单个 env 的映射摘要视图。
func (s *EnvService) viewOf(env *model.Env) (*EnvView, error) {
	mappings, err := s.repo.ListMappingsByEnv(env.ID)
	if err != nil {
		return nil, err
	}
	nameByID, err := s.namespaceNamesByID()
	if err != nil {
		return nil, err
	}
	refs := make([]EnvNamespaceRef, 0, len(mappings))
	for i := range mappings {
		refs = append(refs, EnvNamespaceRef{ID: mappings[i].NamespaceID, Name: nameByID[mappings[i].NamespaceID]})
	}
	view := buildEnvView(env, refs)
	return &view, nil
}

// writeAudit 在事务内落一条 env 域专项审计（对象类型固定 env，targetRef 取 env 行数字 id）。
func (s *EnvService) writeAudit(tx *gorm.DB, action string, envID uint, detail, operator, clientIP string) error {
	return s.auditRepo.WithTx(tx).Create(&model.AuditLog{
		Operator:   operatorOrSystem(operator),
		Action:     action,
		TargetType: model.TargetTypeEnv,
		TargetRef:  fmt.Sprintf("%d", envID),
		Detail:     detail,
		Result:     model.ResultOK,
		ClientIP:   clientIP,
	})
}

// buildEnvView 组装单个 env 视图（refs 已按 namespace 排好，nil 归一为空数组）。
func buildEnvView(env *model.Env, refs []EnvNamespaceRef) EnvView {
	if refs == nil {
		refs = []EnvNamespaceRef{}
	}
	return EnvView{
		ID: env.ID, Name: env.Name, Description: env.Description,
		Namespaces: refs, NamespaceCount: len(refs),
		CreatedAt: env.CreatedAt, UpdatedAt: env.UpdatedAt,
	}
}

// groupNamespaceRefs 把映射行按 env 分组为 namespace 摘要（名取自 id→name 映射，未知名回退空串）。
func groupNamespaceRefs(mappings []model.EnvNamespace, nameByID map[uint]string) map[uint][]EnvNamespaceRef {
	byEnv := make(map[uint][]EnvNamespaceRef)
	for i := range mappings {
		envID := mappings[i].EnvID
		byEnv[envID] = append(byEnv[envID], EnvNamespaceRef{ID: mappings[i].NamespaceID, Name: nameByID[mappings[i].NamespaceID]})
	}
	return byEnv
}

// ensureNamespaceRefsExist 校验待映射 namespace 全部存在（不存在返回 400）。
func ensureNamespaceRefsExist(namespaceIDs []uint, nameByID map[uint]string) error {
	for _, id := range namespaceIDs {
		if _, ok := nameByID[id]; !ok {
			return apperr.ErrEnvNamespaceNotFound
		}
	}
	return nil
}

// buildMappingRows 把 namespace id 列表构造为某 env 的映射行。
func buildMappingRows(envID uint, namespaceIDs []uint) []model.EnvNamespace {
	rows := make([]model.EnvNamespace, 0, len(namespaceIDs))
	for _, nsID := range namespaceIDs {
		rows = append(rows, model.EnvNamespace{EnvID: envID, NamespaceID: nsID})
	}
	return rows
}

// dedupeUints 去重并保序（整体替换映射前归一 namespace id 列表，容忍前端重复传入）。
func dedupeUints(ids []uint) []uint {
	seen := make(map[uint]struct{}, len(ids))
	out := make([]uint, 0, len(ids))
	for _, id := range ids {
		if id == 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
