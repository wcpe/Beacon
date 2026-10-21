package service

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/agentauth"
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 生效来源标记（FR-277，管理面「这个值是哪来的」的唯一答案）。
const (
	// AgentSettingSourceServer 该键由本 server 级覆盖提供
	AgentSettingSourceServer = "server"
	// AgentSettingSourceNamespace 该键沿用 namespace 级默认
	AgentSettingSourceNamespace = "namespace"
	// AgentSettingSourceUnset 两级都没有值，agent 会回落到环境变量 / config.yml / 代码默认
	AgentSettingSourceUnset = "unset"
)

// AgentSettingItemView 是单个键在某作用域下的生效视图（管理面读端点）。
//
// 同时给出「生效值」和「来源」是刻意的：只回生效值会让运维无法判断自己改的到底是
// server 覆盖还是环境默认，从而在「改了这台却没变化」时无从下手（很可能它被更上一级覆盖着）。
type AgentSettingItemView struct {
	// 配置键
	Key string `json:"key"`
	// 生效值（unset 时为空串）
	Value string `json:"value"`
	// 值类型：int / bool / string / string-list
	ValueType string `json:"valueType"`
	// 面向运维的中文说明（含单位与默认值）
	Desc string `json:"desc"`
	// 是否高危项（前端须显著告警）
	Dangerous bool `json:"dangerous"`
	// 该键是否在控制面已知清单内（false = 库里存着但控制面不认识，建议清理或升级控制面）
	Known bool `json:"known"`
	// 生效来源：server / namespace / unset
	Source string `json:"source"`
	// 生效值的最后修改时间（unset 时为 null）
	UpdatedAt *time.Time `json:"updatedAt"`
	// 生效值的最后修改人（unset 时为空串）
	UpdatedBy string `json:"updatedBy"`
}

// AgentSettingScopeView 是某作用域的完整生效视图；ScopeLabel 同时是写端点的二次确认串。
type AgentSettingScopeView struct {
	// 环境主键
	NamespaceID uint `json:"namespaceId"`
	// 环境编码
	NamespaceCode string `json:"namespaceCode"`
	// 目标 serverId（空 = namespace 级）
	ServerID string `json:"serverId"`
	// 作用域标识（`环境码` 或 `环境码/serverId`），写端点须原样回填
	ScopeLabel string `json:"scopeLabel"`
	// 生效值整体最后修改时间（作用域内无任何值时 null）
	UpdatedAt *time.Time `json:"updatedAt"`
	// 逐键视图（按键字典序）
	Items []AgentSettingItemView `json:"items"`
}

// AgentSettingsPullView 是 agent 拉取端点的响应（FR-277 数据面）。
type AgentSettingsPullView struct {
	// 本机所属环境编码（agent 侧据此判断本地缓存是否仍对应自己的绑定）
	Namespace string `json:"namespace"`
	// 本机 serverId
	ServerID string `json:"serverId"`
	// 键 → 字符串值（已剔除禁止下发的键）
	Values map[string]string `json:"values"`
	// 生效值整体最后修改时间（无下发值时为 null）
	UpdatedAt *time.Time `json:"updatedAt"`
	// 服务端剔除的禁止键（不含它们的值）。agent 侧据此告警，让「Web 上下发了但没生效」可见。
	RejectedKeys []string `json:"rejectedKeys"`
}

// AgentSettingsWriteParams 是批量写入参数（PUT /admin/v2/agent-settings）。
type AgentSettingsWriteParams struct {
	// 环境主键（必填）
	NamespaceID uint
	// 目标 serverId；空 = 写 namespace 级默认
	ServerID string
	// 键 → 值；值为 nil 表示删除该作用域的覆盖（回到上一级 / 本地配置）
	Values map[string]*string
	// 二次确认串：必须与 AgentSettingScopeView.ScopeLabel 逐字一致
	ConfirmationScope string
	// 变更原因（写入审计，便于事后回答「为什么改」）
	Reason string
	// 操作人（取自认证态）
	Operator string
	// 来源 IP
	ClientIP string
}

// AgentSettingsWriteResult 是批量写入结果（哪些键被写入 / 撤销 / 未变）。
type AgentSettingsWriteResult struct {
	// 作用域视图（写入后的最新生效状态）
	Scope AgentSettingScopeView `json:"scope"`
	// 本次实际写入（新增或改值）的键
	Applied []string `json:"applied"`
	// 本次实际撤销（删除覆盖）的键
	Removed []string `json:"removed"`
	// 提交值与库内一致、未产生任何写入的键（幂等重放时全是这一类）
	Unchanged []string `json:"unchanged"`
}

// AgentSettingsService 承载 agent 运行配置的下发侧（FR-277）。
//
// 职责边界：本服务只管**下发层真源**（两级作用域的读写 + 生效来源 + 审计），
// 优先级链的另一半（与 env / config.yml / 代码默认的比较）在 agent 侧本地完成——
// 控制面看不到 agent 的部署参数，硬要在服务端算「最终生效值」只会算出一个不成立的结论。
type AgentSettingsService struct {
	db        *gorm.DB
	repo      *repository.AgentSettingRepository
	auditRepo *repository.AuditLogRepository
	nsRepo    *repository.NamespaceRepository
}

// NewAgentSettingsService 构造服务。
func NewAgentSettingsService(db *gorm.DB, repo *repository.AgentSettingRepository,
	auditRepo *repository.AuditLogRepository, nsRepo *repository.NamespaceRepository,
) *AgentSettingsService {
	return &AgentSettingsService{db: db, repo: repo, auditRepo: auditRepo, nsRepo: nsRepo}
}

// ListEffective 返回某作用域的生效配置（server 级覆盖 namespace 级，逐键给出生效来源）。
//
// serverID 为空时只读 namespace 级——这正是「这台服没设过、看环境默认是什么」的视图。
func (s *AgentSettingsService) ListEffective(namespaceID uint, serverID string) (*AgentSettingScopeView, error) {
	ns, err := s.requireNamespace(namespaceID)
	if err != nil {
		return nil, err
	}
	rows, err := s.repo.ListEffective(ns.ID, strings.TrimSpace(serverID))
	if err != nil {
		return nil, err
	}
	view := buildAgentSettingScopeView(ns, strings.TrimSpace(serverID), rows)
	return &view, nil
}

// UpdateBatch 批量写入 / 撤销某作用域的配置项，返回写入后的作用域视图与实际变更集。
//
// 校验顺序刻意从「作用域是否可信」到「值是否合法」：作用域错了（二次确认不符）就不该
// 再费劲校验值——否则运维看到的错误会指向「某个值不合法」，而真正的问题是他改错了服。
func (s *AgentSettingsService) UpdateBatch(p AgentSettingsWriteParams) (*AgentSettingsWriteResult, error) {
	ns, err := s.requireNamespace(p.NamespaceID)
	if err != nil {
		return nil, err
	}
	serverID := strings.TrimSpace(p.ServerID)
	scopeLabel := agentSettingScopeLabel(ns.Code, serverID)
	if strings.TrimSpace(p.ConfirmationScope) != scopeLabel {
		return nil, agentSettingConfirmMismatch(scopeLabel)
	}
	if len(p.Values) == 0 {
		return nil, apperr.ErrInvalidParam
	}
	pending, err := normalizeAgentSettingsWrite(p.Values)
	if err != nil {
		return nil, err
	}
	// 作用域运行资格：已归档环境 / 已归档 server 不再接受运行参数下发（与其它运行写入同口径）。
	// 这里复用刚读出的 namespace 行判资格、再单独判 server 行——不走 ensureServerActive，
	// 因为那会把同一行 namespace 按主键重读一次（见 ensureServerRowActive 注释）。
	// 注意**不**要求 server 行必须存在：agent 可能已注册而拓扑尚未补录，
	// 「配置先行、服后上」是合法工作流（ensureServerRowActive 自身也只在行存在时判归档）。
	if err := ensureNamespaceRuntimeActive(ns); err != nil {
		return nil, err
	}
	if err := ensureServerRowActive(s.db, ns.ID, serverID); err != nil {
		return nil, err
	}

	existing, err := s.repo.ListEffective(ns.ID, serverID)
	if err != nil {
		return nil, err
	}
	// 写路径按**本作用域**的既有行判变更（见 indexAgentSettingRowsAtScope 注释），
	// 而不是按合并后的生效值——后者会把「与继承值恰好相同」误判成无需写入。
	current := indexAgentSettingRowsAtScope(existing, serverID)
	result := AgentSettingsWriteResult{Applied: []string{}, Removed: []string{}, Unchanged: []string{}}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		repo := s.repo.WithTx(tx)
		audit := s.auditRepo.WithTx(tx)
		for _, item := range pending {
			previous, had := current[item.key]
			if item.value == nil {
				if !had {
					result.Unchanged = append(result.Unchanged, item.key)
					continue
				}
				if _, delErr := repo.Delete(ns.ID, serverID, item.key); delErr != nil {
					return delErr
				}
				if auditErr := audit.Create(agentSettingAuditEntry(ns, serverID, item.key, "", previous.Value,
					agentSettingOperationDelete, p)); auditErr != nil {
					return auditErr
				}
				result.Removed = append(result.Removed, item.key)
				continue
			}
			if had && previous.Value == *item.value {
				result.Unchanged = append(result.Unchanged, item.key)
				continue
			}
			row := &model.AgentSetting{
				NamespaceID: ns.ID, ServerID: serverID, Key: item.key,
				Value: *item.value, UpdatedBy: p.Operator,
			}
			if upErr := repo.Upsert(row); upErr != nil {
				return upErr
			}
			if auditErr := audit.Create(agentSettingAuditEntry(ns, serverID, item.key, *item.value, previous.Value,
				agentSettingOperationUpsert, p)); auditErr != nil {
				return auditErr
			}
			result.Applied = append(result.Applied, item.key)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	rows, err := s.repo.ListEffective(ns.ID, serverID)
	if err != nil {
		return nil, err
	}
	result.Scope = buildAgentSettingScopeView(ns, serverID, rows)
	slog.Info("agent 运行配置已更新", "namespace", ns.Code, "serverId", serverID,
		"applied", len(result.Applied), "removed", len(result.Removed), "operator", p.Operator)
	return &result, nil
}

// Pull 返回某 agent 作用域的生效值（数据面 GET /beacon/v2/agent/settings）。
//
// **服务端键级校验在此再挡一次**：库内若存在按 PushedConfigPolicy 不得下发的键
// （beacon.endpoints / beacon.bootstrap-token / identity.*），一律不进 values，并记审计。
// 这不是重复劳动：agent 侧过滤是「守住自己」，服务端拒绝是「守住边界并留证」——
// 前者的信号只留在被管理端的日志里，后者才让控制面自己知道「有人往这条通道塞了劫持键」。
func (s *AgentSettingsService) Pull(identity agentauth.Identity) (AgentSettingsPullView, error) {
	view := AgentSettingsPullView{
		Namespace: identity.Namespace, ServerID: identity.ServerID,
		Values: map[string]string{}, RejectedKeys: []string{},
	}
	if identity.NamespaceID == 0 || identity.ServerID == "" {
		return view, apperr.ErrIdentityRequired
	}
	rows, err := s.repo.ListEffective(identity.NamespaceID, identity.ServerID)
	if err != nil {
		return view, err
	}
	var latest *time.Time
	for i := range rows {
		row := &rows[i]
		// 只知道禁区判断、不要求「键必须已知」：控制面版本可能落后于 agent，
		// 未知键照发（agent 侧不认识就不读，属前向兼容）；禁区键则一律不发。
		if _, forbidden := agentSettingKeyAllowed(row.Key); forbidden {
			view.RejectedKeys = append(view.RejectedKeys, row.Key)
			continue
		}
		view.Values[row.Key] = row.Value
		if latest == nil || row.UpdatedAt.After(*latest) {
			updated := row.UpdatedAt
			latest = &updated
		}
	}
	if len(view.RejectedKeys) > 0 {
		sort.Strings(view.RejectedKeys)
		s.recordRejectedPull(identity, view.RejectedKeys)
	}
	view.UpdatedAt = latest
	return view, nil
}

// recordRejectedPull 记录一次「拉取时被服务端剔除禁区键」的审计。
//
// 刻意**不**让落库失败中断拉取：这是安全留证，不是业务前置条件；把 agent 的配置拉取
// 拖挂在一个审计写失败上，等于让取证需求反过来伤害可用性。失败只 WARN（与机器注册意图审计同处置）。
func (s *AgentSettingsService) recordRejectedPull(identity agentauth.Identity, rejected []string) {
	detail, err := json.Marshal(map[string]any{
		"namespaceId": identity.NamespaceID, "serverId": identity.ServerID, "rejectedKeys": rejected,
	})
	if err != nil {
		slog.Warn("agent 配置拉取拒绝项审计 detail 序列化失败", "identityId", identity.IdentityID, "error", err)
		return
	}
	entry := &model.AuditLog{
		NamespaceCode: identity.Namespace,
		Operator:      "agent:" + identity.IdentityID,
		Action:        model.ActionAgentSettingsRejected,
		TargetType:    model.TargetTypeAgentSetting,
		TargetRef:     agentSettingScopeLabel(identity.Namespace, identity.ServerID),
		Detail:        string(detail),
		Result:        model.ResultFail,
	}
	if err := s.auditRepo.Create(entry); err != nil {
		slog.Warn("agent 配置拉取拒绝项审计落库失败", "identityId", identity.IdentityID, "error", err)
		return
	}
	slog.Warn("已拒绝下发禁止键并记审计", "namespace", identity.Namespace,
		"serverId", identity.ServerID, "keys", rejected)
}

// requireNamespace 按主键取环境行；不存在 → NAMESPACE_NOT_FOUND（不静默按零值继续）。
func (s *AgentSettingsService) requireNamespace(namespaceID uint) (*model.Namespace, error) {
	if namespaceID == 0 {
		return nil, apperr.ErrInvalidParam
	}
	ns, err := s.nsRepo.FindByID(namespaceID)
	if err != nil {
		return nil, err
	}
	if ns == nil {
		return nil, apperr.ErrNamespaceNotFound
	}
	return ns, nil
}

// agentSettingPending 是校验通过后的待写入项（value 为 nil 表示删除）。
type agentSettingPending struct {
	key   string
	value *string
}

// normalizeAgentSettingsWrite 校验并归一化批量写入项：挡禁区键、挡未知键、挡非法值。
//
// 三类问题分别聚合成一条错误（而不是遇到第一个就返回）：运维一次提交十几个键时，
// 「一次只报一个错」会让他在多轮提交间反复试错，而这些错本可以一次说清。
func normalizeAgentSettingsWrite(values map[string]*string) ([]agentSettingPending, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var forbidden, unknown, invalid []string
	pending := make([]agentSettingPending, 0, len(keys))
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		allowed, isForbidden := agentSettingKeyAllowed(key)
		if isForbidden {
			forbidden = append(forbidden, key)
			continue
		}
		meta, known := agentSettingMetaFor(key)
		if !allowed || !known {
			unknown = append(unknown, key)
			continue
		}
		raw := values[rawKey]
		if raw == nil {
			// 删除：无值可校验。
			pending = append(pending, agentSettingPending{key: key})
			continue
		}
		value := strings.TrimSpace(*raw)
		if !validateAgentSettingValue(meta, value) {
			invalid = append(invalid, key+"（期望"+agentSettingValueTypeName(meta.valueType)+"）")
			continue
		}
		trimmed := value
		pending = append(pending, agentSettingPending{key: key, value: &trimmed})
	}
	if len(forbidden) > 0 {
		return nil, agentSettingForbiddenKeyError(forbidden)
	}
	if len(unknown) > 0 {
		return nil, agentSettingUnknownKeyError(unknown)
	}
	if len(invalid) > 0 {
		return nil, agentSettingValueError(invalid)
	}
	return pending, nil
}

// indexAgentSettingRowsAtScope 只保留**恰好属于该作用域**的行（键 → 行）。
//
// 写路径必须用它而不是合并后的生效值，否则会犯两个错：
//   - 某键只在 namespace 级有值 30000，运维在 server 级也填 30000 —— 按生效值比较会判「未变化」
//     而不写这行覆盖；此后环境默认一改，这台服就跟着变了，与运维「我把它钉在 30000」的意图不符。
//   - 某键只有 namespace 级的值，运维在 server 级提交 null 想撤销 —— 按生效值比较会认为「有值可删」，
//     删掉 0 行却照样记一条 delete 审计，于是审计里出现根本没发生的变更。
func indexAgentSettingRowsAtScope(rows []model.AgentSetting, serverID string) map[string]model.AgentSetting {
	out := make(map[string]model.AgentSetting, len(rows))
	for _, row := range rows {
		if row.ServerID != serverID {
			continue
		}
		out[row.Key] = row
	}
	return out
}

// indexAgentSettingRows 把「namespace 级 + 该 server 级」的行按 server 视角合并成键 → 行（读路径用）。
//
// 合并顺序依赖仓库的稳定排序（server_id ASC：namespace 级的空串在前），server 级在其后覆盖——
// 但这里显式按 ServerID 判优先级，不把正确性寄托在排序上。
func indexAgentSettingRows(rows []model.AgentSetting, serverID string) map[string]model.AgentSetting {
	out := make(map[string]model.AgentSetting, len(rows))
	for _, row := range rows {
		if row.ServerID != "" && row.ServerID != serverID {
			continue
		}
		existing, had := out[row.Key]
		if !had || row.ServerID != "" || existing.ServerID == "" {
			out[row.Key] = row
		}
	}
	return out
}

// buildAgentSettingScopeView 组装作用域视图：以「控制面认识的键」并上「库里存着的键」为全集。
//
// 并上库里的键是为了不隐藏数据：某个键被移出元数据后，行还在库里（可能需要运维清理），
// 视图若不展示它，运维就再也看不到、也删不掉这条遗留下发。
func buildAgentSettingScopeView(ns *model.Namespace, serverID string, rows []model.AgentSetting) AgentSettingScopeView {
	effective := indexAgentSettingRows(rows, serverID)
	keySet := make(map[string]struct{}, len(agentSettingKeys)+len(effective))
	for key := range agentSettingKeys {
		keySet[key] = struct{}{}
	}
	for key := range effective {
		keySet[key] = struct{}{}
	}
	keys := make([]string, 0, len(keySet))
	for key := range keySet {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	view := AgentSettingScopeView{
		NamespaceID: ns.ID, NamespaceCode: ns.Code, ServerID: serverID,
		ScopeLabel: agentSettingScopeLabel(ns.Code, serverID),
		Items:      make([]AgentSettingItemView, 0, len(keys)),
	}
	var latest *time.Time
	for _, key := range keys {
		meta, known := agentSettingMetaFor(key)
		item := AgentSettingItemView{
			Key: key, ValueType: meta.valueType, Desc: meta.desc, Dangerous: meta.dangerous,
			Known: known, Source: AgentSettingSourceUnset,
		}
		if row, hit := effective[key]; hit {
			item.Value = row.Value
			item.UpdatedBy = row.UpdatedBy
			updated := row.UpdatedAt
			item.UpdatedAt = &updated
			item.Source = AgentSettingSourceNamespace
			if row.ServerID != "" {
				item.Source = AgentSettingSourceServer
			}
			if latest == nil || updated.After(*latest) {
				latest = &updated
			}
		}
		view.Items = append(view.Items, item)
	}
	view.UpdatedAt = latest
	return view
}

// agentSettingOperationUpsert 是审计 detail 里的「写入」操作类型。
const agentSettingOperationUpsert = "upsert"

// agentSettingOperationDelete 是审计 detail 里的「撤销下发」操作类型。
const agentSettingOperationDelete = "delete"

// agentSettingAuditEntry 构造一条配置变更审计：谁、何时、对哪台服、改了哪个键、改成什么。
//
// TargetRef 用 `作用域/键` 的组合而非只放键：审计列表按 TargetRef 检索时，
// 「这台服的全部配置变更」与「某键在各服的取值历史」都能按前缀命中。
func agentSettingAuditEntry(ns *model.Namespace, serverID, key, value, previousValue, operation string,
	p AgentSettingsWriteParams,
) *model.AuditLog {
	detail, _ := json.Marshal(map[string]any{
		"namespaceId": ns.ID, "namespaceCode": ns.Code, "serverId": serverID,
		"key": key, "value": value, "previousValue": previousValue, "operation": operation,
		"reason": p.Reason,
	})
	return &model.AuditLog{
		NamespaceCode: ns.Code,
		Operator:      p.Operator,
		Action:        model.ActionAgentSettingsUpdate,
		TargetType:    model.TargetTypeAgentSetting,
		TargetRef:     agentSettingScopeLabel(ns.Code, serverID) + "/" + key,
		Detail:        string(detail),
		Result:        model.ResultOK,
		ClientIP:      p.ClientIP,
	}
}

// agentSettingConfirmMismatch 构造二次确认不符的错误：message 直接给出期望值，
// 让调用方（含 MCP / 脚本）无需再查一次就能纠正。
func agentSettingConfirmMismatch(scopeLabel string) error {
	return apperr.New(400, "AGENT_SETTING_CONFIRM_MISMATCH",
		"二次确认不一致：confirmationScope 须与作用域标识逐字一致（本次应为 "+scopeLabel+"）")
}

// agentSettingForbiddenKeyError 构造禁区键错误：点明「为什么不许」，而不是只说「非法」。
func agentSettingForbiddenKeyError(keys []string) error {
	return apperr.New(400, "AGENT_SETTING_KEY_FORBIDDEN",
		"以下键禁止由控制面下发（接入目标与身份只能本地决定）："+strings.Join(keys, "、"))
}

// agentSettingUnknownKeyError 构造未知键错误：提示拼写与控制面版本两条排查方向。
func agentSettingUnknownKeyError(keys []string) error {
	return apperr.New(400, "AGENT_SETTING_KEY_NOT_ALLOWED",
		"以下键不在可下发清单内（请检查拼写，或该键需要更新版本的控制面支持）："+strings.Join(keys, "、"))
}

// agentSettingValueError 构造值非法错误。
func agentSettingValueError(items []string) error {
	return apperr.New(400, "AGENT_SETTING_VALUE_INVALID",
		"以下配置值不合法："+strings.Join(items, "、"))
}
