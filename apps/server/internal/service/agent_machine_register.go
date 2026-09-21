package service

import (
	"crypto/rand"
	"errors"
	"log/slog"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 机器注册通道（FR-222，见 docs/specs/internal-trust-channel.md §3.3/§3.4）。
//
// 背景：agent 接入是分权设计——身份注册落 pending、人工审批后转 active（FR-203/220）。单操作者内网部署下
// 外部管理平台批量建实例后无法自助接入（60 台 = 60 次人工审批）。本通道让**受信内部调用方**的注册直落 active。
//
// 落点说明（真机验证后确定）：共享 token 只到达 v1 数据面 `/beacon/v1/agent/register`（该组挂了
// agentTokenMiddleware，命中共享 token 即被标记为受信内部调用方）；v2 身份注册端点要求 namespace token、
// 且不在该中间件组内，故机器注册的分支落点是 **v1 注册** + 本文件的身份直落逻辑。
//
// 三条硬约束：
//  1. 分支依据是中间件判定的调用方类型 + 部署开关，**绝不取自请求体**（调用方无法伪造）。
//  2. 开关关闭时本文件逻辑不可达，v1 注册保持既有「只写内存 registry + instance.register 审计」语义。
//  3. 无论开关状态，机器注册意图都写一条强审计（开启=已直落、关闭=已提交待审批），可追溯来源 IP。

const (
	// machineRegisterOperator 是机器注册审计的操作者标识（spec §3.4）。
	machineRegisterOperator = "system:machine-register"

	// 审计 detail 的结果取值（spec §3.4 / §6 验收 5、6）。
	machineRegisterOutcomeActive  = "active"
	machineRegisterOutcomePending = "pending"
)

// MachineRegisterResult 是机器注册直落的结果（v1 注册响应据此回带权威绑定）。
type MachineRegisterResult struct {
	// IdentityID 是本次机器注册生成 / 复用的 agent 身份标识。
	IdentityID string
	// ServerID 是绑定到的 serverId（机器注册必须完成绑定）。
	ServerID string
	// BoundAt 是本次激活绑定时刻。
	BoundAt time.Time
}

// machineRegisterIfTrusted 处理来自受信内部调用方的注册（FR-222），返回值语义：
//   - err != nil：机器注册被拒（serverId 占用 / 命名空间不可用 / 身份禁用等），注册整体失败
//   - (零值, nil)：非受信调用方或开关关闭 → 调用方按既有 v1 注册语义继续（投影路径，行为逐字不变）
//   - (非零值, nil)：已直落 active 并绑定 serverId，且已写身份审计
//
// 开关关闭时仍会为受信调用方补记一条「已提交待审批」审计（spec §3.4：机器注册意图一律留痕）。
func (s *InstanceService) machineRegisterIfTrusted(p RegisterParams) (MachineRegisterResult, error) {
	if !p.TrustedInternal {
		return MachineRegisterResult{}, nil
	}
	if !s.machineRegisterEnabled() {
		// 开关关闭：注册仍走既有投影路径（v2 侧 pending 语义不变），仅补记机器注册意图审计。
		s.auditMachineRegisterIntent(p, machineRegisterOutcomePending)
		return MachineRegisterResult{}, nil
	}
	return s.applyMachineRegister(p)
}

// SetMachineRegisterAllowed 注入机器注册开关（进程启动时按 mcp.allow-machine-register 调用一次）。
func (s *InstanceService) SetMachineRegisterAllowed(allowed bool) {
	s.machineRegisterAllowed = allowed
}

// machineRegisterEnabled 报告机器注册通道是否开启（默认 false）。
func (s *InstanceService) machineRegisterEnabled() bool {
	return s.machineRegisterAllowed
}

// applyMachineRegister 把受信内部调用方的注册直落为 active 身份并完成 serverId 绑定。
//
// 事实写入与人工审批路径（V2ControlPlaneService.applyApproveAgentIdentity）等价：
// 同事务内解析 namespace、校验 serverId 归属、按角色建未分配 server 行（不落 zone / 默认入口 ——
// 分配与换区仍走各自审批）、置 active + boundAt，并写 identity.machine_registered 强审计。
func (s *InstanceService) applyMachineRegister(p RegisterParams) (MachineRegisterResult, error) {
	serverID := strings.TrimSpace(p.ServerID)
	if serverID == "" {
		// 机器注册必须完成绑定（FR-222：直接 active 并绑定指定 serverId），缺 serverId 无从绑定。
		return MachineRegisterResult{}, apperr.ErrIdentityRequired
	}
	kind, err := agentRoleToServerKind(p.Role)
	if err != nil {
		return MachineRegisterResult{}, err
	}
	now := time.Now().UTC()
	identityID, err := newAgentIdentityID()
	if err != nil {
		return MachineRegisterResult{}, err
	}
	var result MachineRegisterResult
	err = s.db.Transaction(func(tx *gorm.DB) error {
		ns, err := findNamespaceByCodeInTx(tx, p.Namespace)
		if err != nil {
			return err
		}
		if ns == nil {
			// v1 注册既有语义允许未知 namespace 只进内存 registry；机器注册要写权威身份行，必须能解析归属。
			return apperr.ErrNamespaceNotFound
		}
		if err := ensureServerActive(tx, ns.ID, serverID); err != nil {
			return err
		}
		ident, err := upsertMachineRegisterIdentity(tx, ns, identityID, serverID, kind, p, now)
		if err != nil {
			return err
		}
		result = MachineRegisterResult{IdentityID: ident.IdentityID, ServerID: serverID, BoundAt: now}
		return writeMachineRegisterAudit(tx, ns, ident, p, machineRegisterOutcomeActive)
	})
	if err != nil {
		return MachineRegisterResult{}, err
	}
	return result, nil
}

// upsertMachineRegisterIdentity 建行或复用同一 (namespace, serverId) 上的既有身份，终态统一为 active 绑定。
//
// 复用而非重复建行：同一 serverId 重复注册（实例重启 / 管理平台重复推送）应幂等，且同 namespace
// 内活跃态的 serverId 不得重复（v2-agent-identity spec §4）。
func upsertMachineRegisterIdentity(tx *gorm.DB, ns *model.Namespace, identityID, serverID, kind string, p RegisterParams, now time.Time) (*model.AgentIdentity, error) {
	current, err := findBoundIdentityByServer(tx, ns.ID, serverID)
	if err != nil {
		return nil, err
	}
	if current != nil {
		return refreshMachineRegisterIdentity(tx, current, p, now)
	}
	return createMachineRegisterIdentity(tx, ns, identityID, serverID, kind, p, now)
}

// refreshMachineRegisterIdentity 复用既有活跃身份：刷新上报事实，保持 active 与既有绑定不变。
func refreshMachineRegisterIdentity(tx *gorm.DB, current *model.AgentIdentity, p RegisterParams, now time.Time) (*model.AgentIdentity, error) {
	// 已禁用身份不得被机器注册通道悄悄复活：禁用是人类/审批侧的止损动作，须由 enable 流程显式恢复。
	if current.Status == model.AgentIdentityStatusDisabled {
		return nil, apperr.ErrIllegalState
	}
	current.LastAddr = agentLastAddr(p)
	current.StatusChangedAt = now
	if err := tx.Save(current).Error; err != nil {
		return nil, err
	}
	return current, nil
}

// createMachineRegisterIdentity 为受信调用方建一条已绑定且 active 的身份行（server 行同事务就绪）。
func createMachineRegisterIdentity(tx *gorm.DB, ns *model.Namespace, identityID, serverID, kind string, p RegisterParams, now time.Time) (*model.AgentIdentity, error) {
	if err := ensureServerIDAvailableForRegister(tx, ns.ID, serverID); err != nil {
		return nil, err
	}
	if _, err := ensureServerRow(tx, ns.ID, serverID, kind); err != nil {
		return nil, err
	}
	ident := &model.AgentIdentity{
		IdentityID: identityID, NamespaceID: ns.ID, ServerID: model.NullableServerID(serverID),
		Kind: kind, Status: model.AgentIdentityStatusActive,
		LastAddr: agentLastAddr(p), AgentVersion: p.AgentVersion,
		BoundAt: &now, StatusChangedAt: now,
		BindingSource: model.AgentIdentityBindingSourceAdminAssigned,
	}
	if err := tx.Create(ident).Error; err != nil {
		return nil, err
	}
	return ident, nil
}

// writeMachineRegisterAudit 落一条 identity.machine_registered 强审计（spec §3.4）：
// operator 恒为 system:machine-register，target 为 agent-identity/<identityId>，
// detail 含 serverId / lastAddr / 调用来源 IP 与本次结果（active=已直落、pending=已提交待审批）。
func writeMachineRegisterAudit(tx *gorm.DB, ns *model.Namespace, ident *model.AgentIdentity, p RegisterParams, outcome string) error {
	detail := auditJSON(map[string]string{
		"outcome":  outcome,
		"serverId": p.ServerID,
		"lastAddr": agentLastAddr(p),
		"clientIp": p.ClientIP,
	})
	return createAudit(tx, model.AuditLog{
		NamespaceCode: nsCodeOf(ns),
		Operator:      machineRegisterOperator,
		Action:        model.ActionIdentityMachineRegistered,
		TargetType:    model.TargetTypeIdentity,
		TargetRef:     ident.IdentityID,
		Detail:        detail,
		Result:        model.ResultOK,
		ClientIP:      p.ClientIP,
	})
}

// auditMachineRegisterIntent 在开关关闭时补记机器注册意图（spec §3.4：无论开关状态都留痕）。
// 此时尚未建身份行，targetRef 取既有身份（若有）或退回 serverId，仍保证来源可追溯。
func (s *InstanceService) auditMachineRegisterIntent(p RegisterParams, outcome string) {
	ns, err := findNamespaceByCodeInTx(s.db, p.Namespace)
	if err != nil || ns == nil {
		return
	}
	ident := &model.AgentIdentity{IdentityID: machineRegisterIntentRef(s.db, ns.ID, p.ServerID)}
	if err := writeMachineRegisterAudit(s.db, ns, ident, p, outcome); err != nil {
		// 旁路语义：审计落库失败只告警，不影响注册主响应。
		slog.Warn("机器注册意图审计落库失败", "namespace", p.Namespace, "serverId", p.ServerID,
			"客户端", p.ClientIP, "原因", err)
	}
}

// machineRegisterIntentRef 取意图审计的 targetRef：优先复用该 serverId 上的既有身份，
// 否则退回 serverId 本身（此时尚未建身份行，无 identityId 可用）。
func machineRegisterIntentRef(tx *gorm.DB, namespaceID uint, serverID string) string {
	if serverID == "" {
		return namespaceIDString(namespaceID)
	}
	ident, err := findBoundIdentityByServer(tx, namespaceID, serverID)
	if err != nil || ident == nil {
		return serverID
	}
	return ident.IdentityID
}

// agentLastAddr 取身份行的兼容单地址投影（agent 上报的对外可达地址）。
func agentLastAddr(p RegisterParams) string {
	return strings.TrimSpace(p.Address)
}

// nsCodeOf 取 namespace code（nil 安全）。
func nsCodeOf(ns *model.Namespace) string {
	if ns == nil {
		return ""
	}
	return ns.Code
}

// findNamespaceByCodeInTx 按 code 取 namespace；不存在返回 (nil, nil)。
func findNamespaceByCodeInTx(tx *gorm.DB, code string) (*model.Namespace, error) {
	if strings.TrimSpace(code) == "" {
		return nil, nil
	}
	var ns model.Namespace
	err := tx.Where("code = ?", code).First(&ns).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ns, nil
}

// agentRoleToServerKind 把 v1 注册的 agent 角色映射为 v2 server kind（与 agent 侧 v2Kind 同口径）。
func agentRoleToServerKind(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "bungee", "bungeecord", "waterfall", "velocity":
		return model.ServerKindProxy, nil
	case "":
		// 旧 agent 可能不报 role；缺省按 backend（v2 口径的默认归类）。
		return model.ServerKindBackend, nil
	default:
		return model.ServerKindBackend, nil
	}
}

// newAgentIdentityID 生成机器注册的 agent 身份标识（UUIDv4 小写，与 agent 首启生成的形态一致）。
func newAgentIdentityID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // 版本 4
	b[8] = (b[8] & 0x3f) | 0x80 // 变体 10
	return formatUUID(b), nil
}

// formatUUID 把 16 字节按 8-4-4-4-12 小写十六进制格式化。
func formatUUID(b [16]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, v := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexdigits[v>>4], hexdigits[v&0x0f])
	}
	return string(out)
}

// namespaceIDString 把 namespace 主键渲染为审计 targetRef 的退化形态。
func namespaceIDString(id uint) string {
	return "namespace:" + itoaUint(id)
}

// itoaUint 是无依赖的 uint → 十进制字符串。
func itoaUint(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
