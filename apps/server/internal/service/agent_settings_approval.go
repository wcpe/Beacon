package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/auth"
	"github.com/wcpe/Beacon/apps/server/internal/authz"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// Agent 运行配置高危键的审批链路（FR-277 #4，见 ADR-0095 / docs/UX.md）。
//
// 单有「危险标记」并不构成防线：标记只让前端把输入框染红，写入路径若照样 200 落库，
// 一次被劫持的管理会话就能把 command-whitelist 放开到任意命令。所以这里把
// metadata.dangerous 真正接进**写入决策**：高危键的变更必须先冻结成审批申请，
// 由第二个人批准后才在独立事务里落库。
//
// 与 system_dangerous_approval.go 同一范式（注册表 + 执行许可 + 领域事务 + 执行回执），
// 差别只在领域：那边的对象是控制面自身设置 / 升级包，这边是被管端的运行参数，
// 且一次提交可能涉及多个键，故载荷冻结的是**整批**（逐键带并发基线）。

// agentSettingsApprovalItem 是冻结载荷里的单个键值对。
//
// 同时冻结 previousValue 是这一层的关键：agent 配置行没有独立版本列，该键在本作用域的
// **既有值本身就是它的版本**——批准后执行时逐键比对，任一键在等待审批期间被改过，
// 整批即判目标已变。没有这个基线，「按 3 天前看到的值批准」会悄悄盖掉期间的真实改动。
type agentSettingsApprovalItem struct {
	// 配置键
	Key string `json:"key"`
	// 目标值；null = 撤销该作用域的覆盖
	Value *string `json:"value"`
	// 冻结时该键在本作用域的既有值；null = 冻结时本作用域没有该键的覆盖
	PreviousValue *string `json:"previousValue"`
}

// agentSettingsApprovalPayload 是冻结进审批申请的完整变更意图。
type agentSettingsApprovalPayload struct {
	// 环境主键
	NamespaceID uint `json:"namespaceId"`
	// 冻结时的环境码：执行时与重读出的环境码比对，改名即视为目标已变
	NamespaceCode string `json:"namespaceCode"`
	// 目标 serverId（空 = namespace 级）
	ServerID string `json:"serverId"`
	// 作用域标识（`环境码` 或 `环境码/serverId`），执行时复核
	ScopeLabel string `json:"scopeLabel"`
	// 逐键变更（按键字典序，保证载荷哈希稳定）
	Values []agentSettingsApprovalItem `json:"values"`
	// 变更原因（申请人填写，入审计）
	Reason string `json:"reason"`
	// 申请人
	Operator string `json:"operator"`
	// 申请来源 IP
	ClientIP string `json:"clientIP"`
}

// RegisterAgentSettingsApprovalAdapter 登记 agent 运行配置高危键下发的执行适配器。
func RegisterAgentSettingsApprovalAdapter(registry *authz.ApprovalRegistry, svc *AgentSettingsService) {
	if registry == nil || svc == nil {
		return
	}
	registry.RegisterDescriptor(authz.OperationDescriptor{
		Key:           authz.OperationAgentSettingsDangerous,
		SchemaVersion: approvalSchemaVersion,
		Capability:    auth.CapabilityApprovalRequest,
		RiskLevel:     "high",
	}, authz.RequireExecutionReceipt(agentSettingsApprovalAdapter{svc: svc}))
}

// requestDangerousBatch 把整批变更冻结成一条审批申请，返回票据而不落库。
//
// ## 为什么是「整单」而不是「逐键」
//
// 一次 PUT 就是运维眼里的一次操作。若把危险键单独摘出去审批、非危险键当场落库，
// 结果是「一半已生效、一半待批」——生效的那半还会在刷新后看起来一切正常，而真正被
// 拦下的那半只在审批中心里。这种中间态既无法向人解释，也让「刚才那一下到底改了没有」
// 没有确定答案。故：只要本次提交含任一危险键，整批一起冻结、一起等待、一起落库。
//
// 代价是非危险键也要等审批，但这正是有意的：它们与危险键属于同一次意图。
func (s *AgentSettingsService) requestDangerousBatch(p AgentSettingsWriteParams, ns *model.Namespace,
	serverID string, pending []agentSettingPending,
) (*AgentSettingsWriteResult, error) {
	// 未注入审批核心时直接失败：宁可这条链路不可用，也不能「审批没装配所以顺手直写了」——
	// 那是把安全承诺变成装配顺序的副作用（fail-closed）。
	if s.approval == nil {
		return nil, apperr.ErrInternal
	}
	// 原因缺失直接拒：审批人看到的唯一上下文就是这句话，空着的申请等于让他盲签。
	if strings.TrimSpace(p.Reason) == "" {
		return nil, apperr.ErrApprovalReasonRequired
	}
	existing, err := s.repo.ListEffective(ns.ID, serverID)
	if err != nil {
		return nil, err
	}
	current := indexAgentSettingRowsAtScope(existing, serverID)
	scopeLabel := agentSettingScopeLabel(ns.Code, serverID)
	payload := buildAgentSettingsApprovalPayload(ns, serverID, scopeLabel, pending, current, p)

	op := authz.Operation{
		Kind:           authz.OperationAgentSettingsDangerous,
		NamespaceID:    &ns.ID,
		Resource:       model.TargetTypeAgentSetting,
		ResourceID:     scopeLabel,
		IdempotencyKey: agentSettingsApprovalIdempotencyKey(scopeLabel, pending, p),
		RiskLevel:      "high",
		Reason:         p.Reason,
	}
	req, err := s.requestWithTerminalSupersede(op, payload, p.Principal, p.ClientIP)
	if err != nil {
		return nil, err
	}
	ticket := &ApprovalTicketView{
		ApprovalRequestID: req.RequestID, Status: req.Status, OperationKey: req.OperationKey,
	}
	return &AgentSettingsWriteResult{
		// 提交前的作用域视图：库内此刻确实什么都没变，视图如实反映它。
		Scope:          buildAgentSettingScopeView(ns, serverID, existing),
		Applied:        []string{},
		Removed:        []string{},
		Unchanged:      []string{},
		ApprovalTicket: ticket,
	}, nil
}

// buildAgentSettingsApprovalPayload 组装冻结载荷：作用域 + 逐键值 + 逐键并发基线 + 申请上下文。
func buildAgentSettingsApprovalPayload(ns *model.Namespace, serverID, scopeLabel string,
	pending []agentSettingPending, current map[string]model.AgentSetting, p AgentSettingsWriteParams,
) map[string]any {
	items := make([]map[string]any, 0, len(pending))
	for _, item := range pending {
		var previousValue, value any
		if row, hit := current[item.key]; hit {
			previousValue = row.Value
		}
		if item.value != nil {
			value = *item.value
		}
		items = append(items, map[string]any{
			"key": item.key, "value": value, "previousValue": previousValue,
		})
	}
	return map[string]any{
		"namespaceId": ns.ID, "namespaceCode": ns.Code, "serverId": serverID,
		"scopeLabel": scopeLabel, "values": items,
		"reason": p.Reason, "operator": p.Operator, "clientIP": p.ClientIP,
	}
}

// agentSettingsApprovalIdempotencyKey 由提交内容派生幂等键。
//
// 前端这个 PUT 不带 Idempotency-Key（它原本是「填完就保存」的直写表单），而审批申请
// 强制要求幂等键。因此由服务端按「作用域 + 逐键值 + 原因 + 申请人 + 来源」派生：
//   - 内容相同（运维点两次「保存」）→ 同键 → 命中既有申请，返回**同一张**票据，不会攒出两条待批；
//   - 内容不同 → 不同键 → 正常建新申请。
//
// 派生输入与冻结载荷包含的字段一致，故同键必然同载荷哈希，不会触发幂等键复用冲突。
//
// 注意这个键只覆盖「内容相同」这一层幂等，不管旧申请已经终结：终结之后的重提由
// requestWithTerminalSupersede 在这个键之上再派生改派键，绝不能直接把死票返回给运维。
func agentSettingsApprovalIdempotencyKey(scopeLabel string, pending []agentSettingPending, p AgentSettingsWriteParams) string {
	var b strings.Builder
	b.WriteString(scopeLabel)
	for _, item := range pending {
		b.WriteString("\x1f")
		b.WriteString(item.key)
		b.WriteString("=")
		if item.value != nil {
			b.WriteString(*item.value)
		} else {
			b.WriteString("\x00") // 与任何真实值都不同：撤销不应与「设为某个值」撞键
		}
	}
	for _, extra := range []string{p.Reason, p.Operator, p.ClientIP} {
		b.WriteString("\x1e")
		b.WriteString(extra)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "agent-settings-" + hex.EncodeToString(sum[:])[:32]
}

// agentSettingsApprovalSupersedeHops 是「旧票已死 → 改派新票」链条的最大跳数。
//
// 每一跳都换一张单号派生的新键，键互不相同，故链条长度上界就是「同一内容的既有申请条数」，
// 正常运营下最多一两次（驳回 → 改原因重提）。这里留一个显式上限只为防脏数据把请求卡在循环里：
// 一旦越过上限就报错，绝不再返回一张死票据。
const agentSettingsApprovalSupersedeHops = 8

// errAgentSettingsApprovalSupersedeExhausted 表示同一内容的改派链已连续终结到上限。
//
// 用一个明确的 409 而不是含糊的内部错误：出现它只可能是同一变更被反复终结（或库被手工改过），
// 需要人去看审批中心，而不是让运维继续等一张不会再被处理的申请。
var errAgentSettingsApprovalSupersedeExhausted = apperr.New(http.StatusConflict,
	"AGENT_SETTING_APPROVAL_SUPERSEDE_EXHAUSTED",
	"同一变更的旧审批申请已连续终结多次，未能建出新的待批申请，请到审批中心确认该变更的历史")

// requestWithTerminalSupersede 提交审批申请，并在命中「已经不可能产生效果」的旧申请时改派一张新票。
//
// ## 为什么必须有这一步（这是本组「不得假报成功」的核心）
//
// 幂等键由提交内容派生（见 agentSettingsApprovalIdempotencyKey），而 ApprovalService.Request
// 命中幂等键后**不按状态过滤**就直接返回既有申请，uniq_approval_idempotency 又让那条记录永久存在。
// 于是白名单变更被驳回之后，运维用同样的值、同样的原因再提交一次，拿回的是那张已 rejected 的旧申请，
// handler 照常回 202 并显示「已提交审批，批准后才会下发」——**而实际上没有任何新申请产生，
// 没有审批人会看到它，这次变更永远不会生效**。运维以为自己在等审批。这比直接报错坏得多：
// 系统谎称成功了。
//
// 所以在 agent settings 的申请入口做终态感知替换：旧申请已死 → 用「原内容键 + 旧申请单号」
// 派生出新键重新申请一次。**刻意不放进 approval_service.go**：那是所有审批调用方共用的通用核心，
// 在那里按状态改道会同时改掉几十条链路的幂等语义，风险远大于收益。
//
// ## 改派之后幂等性为什么仍然成立
//
// 新键 = f(原内容键, 旧申请单号)，两个输入都是确定的：
//   - 旧票仍活着 → 第一跳就命中它，键根本不会变，行为与引入本函数之前逐字一致；
//   - 旧票已死 → 两次相同提交算出同一个新键：第一次建出新申请，第二次命中它 → 依然不会攒出两条待批。
//
// 链条（每条申请恰好有一个后继键，故整条链确定）：K0 建 R1；R1 死了用 f(K0,R1) 建 R2；
// R2 也死了用 f(K0,R2) 建 R3……同样的内容任何时候重提，都沿同一条链走到当前那张活票。
//
// ## 为什么极端并发下多建一条是可接受的
//
// Request 内部是 s.db.Transaction，本函数两次调用就是两次事务，做不到跨事务的原子「判死 + 改派」。
// 极端并发（同一内容两次提交且旧票此刻正终结）可能各建一条申请，但**绝不会再次谎报成功**：
// 宁可多一条待批，不可让运维以为自己在等一个不存在的审批。多出来的那条也只是待批，可被驳回。
func (s *AgentSettingsService) requestWithTerminalSupersede(op authz.Operation, payload map[string]any,
	principal auth.Principal, clientIP string,
) (model.ApprovalRequest, error) {
	// 改派键始终从**原内容键 + 死亡票单号**派生（而不是从上一跳的键链式派生），
	// 这样同一张死票的后继键唯一，重放走的是同一条链，幂等性不依赖调用顺序。
	baseKey := op.IdempotencyKey
	req, err := s.approval.Request(op, payload, principal, clientIP)
	if err != nil {
		return model.ApprovalRequest{}, err
	}
	for hop := 0; hop < agentSettingsApprovalSupersedeHops; hop++ {
		if !agentSettingsApprovalTicketIsDead(req.Status, req.ExpiresAt) {
			return req, nil
		}
		op.IdempotencyKey = agentSettingsApprovalSupersedeKey(baseKey, req.RequestID)
		next, nextErr := s.approval.Request(op, payload, principal, clientIP)
		if nextErr != nil {
			return model.ApprovalRequest{}, nextErr
		}
		slog.Info("agent 运行配置高危键的旧审批申请已无法生效，已改派新申请",
			"supersededRequestId", req.RequestID, "supersededStatus", req.Status,
			"newRequestId", next.RequestID, "scope", op.ResourceID)
		req = next
	}
	// 改派链都没能建出一张活票：如实报错，不再把一张死票据当成「已受理」交给运维。
	return model.ApprovalRequest{}, errAgentSettingsApprovalSupersedeExhausted
}

// agentSettingsApprovalTicketIsDead 判断一张既有申请是否已经不可能再产生任何效果。
//
// 两种情形会「死」：
//   - 终态（驳回 / 撤回 / 过期 / 已成功 / 已失败）：再提交同样的内容，绝不会有任何人处理它；
//   - pending 但已过期：审批内核在批准时会把过期的 pending 直接改成 expired 并回 ErrApprovalExpired
//     （见 ApprovalService.markExecuting），所以它同样只能等死，复用它就是给运维一张必失效的票据。
//
// 仍在 pending / executing 的申请才允许复用：executing 表示批准已发生、执行在途，
// 此时同内容的再次提交本就是同一个意图的重放，复用那张票是正确行为。
//
// 参数取 status + expiresAt 而不是整条实体：这是个纯函数，穷举测试只需喂两个字段。
func agentSettingsApprovalTicketIsDead(status string, expiresAt *time.Time) bool {
	if model.IsTerminalApprovalStatus(status) {
		return true
	}
	return status == model.ApprovalStatusPending && expiresAt != nil && !expiresAt.After(time.Now().UTC())
}

// agentSettingsApprovalSupersedeKey 由「原内容键 + 被替换的旧申请单号」派生改派键。
//
// 两个输入都确定（内容键由提交内容派生，单号来自既有申请），故改派键也确定——
// 这正是「旧票终结后重复提交仍然幂等」的全部依据。
// 输出固定 48 字节的可打印 ASCII，落在 idempotency_key 列（size:64）与 validIdempotencyKey 的约束内。
func agentSettingsApprovalSupersedeKey(baseKey, supersededRequestID string) string {
	// \x1d 分隔符不会出现在两个输入里（前者是 hex + 连字符，后者是 apr_ + hex），不会撞键。
	sum := sha256.Sum256([]byte(baseKey + "\x1d" + supersededRequestID))
	return "agent-settings-s" + hex.EncodeToString(sum[:])[:32]
}

// agentSettingsApprovalAdapter 是批准后的执行适配器。
type agentSettingsApprovalAdapter struct {
	svc *AgentSettingsService
}

// Execute 阻止适配器被误用在非事务执行路径（领域写入必须与执行回执同事务）。
func (agentSettingsApprovalAdapter) Execute(authz.ApprovalRequest, authz.Permit) error {
	return apperr.ErrForbidden
}

// ExecuteInTx 在审批 worker 提供的领域事务中重校验并落库，最后提交执行回执。
func (a agentSettingsApprovalAdapter) ExecuteInTx(tx *gorm.DB, req authz.ApprovalRequest, permit authz.Permit) (func(), error) {
	if tx == nil || a.svc == nil {
		return nil, apperr.ErrForbidden
	}
	if err := ensureRequestPermit(req, permit); err != nil {
		return nil, err
	}
	if permit.Operation() != authz.OperationAgentSettingsDangerous {
		return nil, apperr.ErrForbidden
	}
	var payload agentSettingsApprovalPayload
	if err := json.Unmarshal(req.Payload, &payload); err != nil {
		return nil, apperr.ErrInvalidParam
	}
	// 变更集的日志由 applyApprovedBatchInTx 内部记录，此处只需要「是否成功」。
	// 申请 ID 一路传下去是为了让每条配置变更审计都能追到「凭哪张批准执行的」。
	if _, err := a.svc.applyApprovedBatchInTx(tx, req.RequestID, payload); err != nil {
		return nil, err
	}
	receipt := &model.ApprovalExecutionReceipt{
		RequestID: req.RequestID, OperationKey: req.OperationKey, PayloadHash: req.PayloadHash,
		ResultRef: "agent-settings-" + payload.ScopeLabel,
	}
	if err := tx.Create(receipt).Error; err != nil {
		return nil, err
	}
	// 下发侧没有内存缓存可刷新（读路径直查库），故无提交后动作。
	return nil, nil
}

// applyApprovedBatchInTx 按冻结载荷重校验并落库。
//
// 重校验的顺序与写入入口一致，且**一条都不能省**：
//   - 作用域：环境可能已被归档或改名（冻结的作用域标识必须仍成立）；
//   - 键与值：白名单 / 禁区 / 取值复核——批准的是「那批变更」，不是「绕过写入校验的通行证」；
//   - 并发基线：等待审批期间该作用域的相关键必须没被改过。
//
// 任一项不符即整批失败：审批的语义是「按冻结内容执行」，不是「尽量执行」。
func (s *AgentSettingsService) applyApprovedBatchInTx(tx *gorm.DB, requestID string,
	payload agentSettingsApprovalPayload,
) (AgentSettingsWriteResult, error) {
	var empty AgentSettingsWriteResult
	ns, err := s.nsRepo.WithTx(tx).FindByID(payload.NamespaceID)
	if err != nil {
		return empty, err
	}
	if ns == nil {
		return empty, apperr.ErrNamespaceNotFound
	}
	// 改名 / 改作用域等价于「批准的已不是同一批东西」。
	if ns.Code != payload.NamespaceCode || agentSettingScopeLabel(ns.Code, payload.ServerID) != payload.ScopeLabel {
		return empty, apperr.ErrApprovalTargetChanged
	}
	if err := ensureNamespaceRuntimeActive(ns); err != nil {
		return empty, err
	}
	if err := ensureServerRowActive(tx, ns.ID, payload.ServerID); err != nil {
		return empty, err
	}
	values := make(map[string]*string, len(payload.Values))
	for _, item := range payload.Values {
		values[item.Key] = item.Value
	}
	pending, err := normalizeAgentSettingsWrite(values)
	if err != nil {
		return empty, err
	}
	// 审批通道只服务高危键变更：载荷里若一个危险键都没有，说明它是被拼装出来的，
	// 不能借这条通道把普通写入伪装成「已批准」。
	if len(dangerousAgentSettingKeys(pending)) == 0 {
		return empty, apperr.ErrForbidden
	}
	existing, err := s.repo.WithTx(tx).ListEffective(ns.ID, payload.ServerID)
	if err != nil {
		return empty, err
	}
	current := indexAgentSettingRowsAtScope(existing, payload.ServerID)
	if !agentSettingsBaselineIntact(payload.Values, current) {
		return empty, apperr.ErrApprovalTargetChanged
	}
	writeParams := AgentSettingsWriteParams{
		NamespaceID: ns.ID, ServerID: payload.ServerID, Reason: payload.Reason,
		Operator: payload.Operator, ClientIP: payload.ClientIP,
		// 让每条变更审计都能反查审批单据（审计列表只看得出「改了」，加上它才说明「凭什么改的」）。
		ApprovalRequestID: requestID,
	}
	result, err := s.applyInTx(tx, ns, payload.ServerID, pending, writeParams)
	if err != nil {
		return empty, err
	}
	slog.Info("agent 运行配置高危键已按审批结果下发", "namespace", ns.Code, "serverId", payload.ServerID,
		"applied", len(result.Applied), "removed", len(result.Removed), "operator", payload.Operator)
	return result, nil
}

// agentSettingsBaselineIntact 校验冻结时逐键记录的既有值是否仍是库内现状。
//
// 三种情形都算「已变」：本无覆盖却出现了、本有覆盖却消失了、值被改成别的。仅比对值不够——
// 「撤销」的正确性完全依赖「当时确实存在一行可撤销」，漏判会让一次撤销变成静默成功。
func agentSettingsBaselineIntact(frozen []agentSettingsApprovalItem, current map[string]model.AgentSetting) bool {
	for _, item := range frozen {
		row, had := current[item.Key]
		switch {
		case item.PreviousValue == nil && had:
			return false
		case item.PreviousValue != nil && !had:
			return false
		case item.PreviousValue != nil && row.Value != *item.PreviousValue:
			return false
		}
	}
	return true
}

// dangerousAgentSettingKeys 取出本次提交里命中高危标记的键（按键字典序，便于日志与断言）。
//
// 撤销（value 为 nil）同样算高危变更：白名单被悄悄清空等于关掉命令派发能力，
// 而「关掉」与「放开」一样需要留痕，也要防止一个人单方面改掉运维正在依赖的能力边界。
func dangerousAgentSettingKeys(pending []agentSettingPending) []string {
	keys := make([]string, 0, len(pending))
	for _, item := range pending {
		if meta, known := agentSettingMetaFor(item.key); known && meta.dangerous {
			keys = append(keys, item.key)
		}
	}
	return keys
}
