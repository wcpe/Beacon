package service

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// —— FR-264：agent 交付能力版本守卫（落实 ADR-0069 L58「不对不支持流式交付的旧 agent 下发命令」）——
//
// 与波次 A（ADR-0088）的边界：ADR-0088 的 `gracefulShutdownSupported` 是 **agent 进程内**对「关服原语
// 是否实现」的自探测，只决定 restart 生效能否回执 success；本守卫是 **控制面**对「agent 版本是否够新到
// 认识流式交付命令」的前置判定，只决定是否下发。二者判定主体、时机、失败动作都不同，互不覆盖——
// 同一台旧 agent 会先被本守卫拒（不下发），即便侥幸下发也会被 agent 侧能力探测拒（不回执 success），
// 双 fail-closed，方向一致、不冲突。

// deliveryAgentVersionSegments 是版本比较的最大段数（主.次.修三段足够，不足补零、超出截断）。
const deliveryAgentVersionSegments = 3

// deliveryAgentSupportsStreaming 判定某 agent 上报版本是否满足流式交付的最低能力版本要求。
//
// 口径（fail-closed）：
//   - minVersion 为空串 → 不校验（运维逃生口：真机版本形态未对齐时可临时关闭守卫）；
//   - version 为空串 → **不支持**（旧 agent 未上报版本，正是 ADR-0069 要挡住的那批）；
//   - 逐段数值比较、长度不等补 0，段内非数字后缀（如 `1.4.0-rc.1`）按前缀数字段参与比较——
//     预发布后缀不改变主版本序，不能因 `-rc.1` 把 1.4.0 判成低于 0.29.0。
func deliveryAgentSupportsStreaming(version, minVersion string) bool {
	if strings.TrimSpace(minVersion) == "" {
		return true
	}
	return compareAgentVersions(version, minVersion) >= 0
}

// compareAgentVersions 比较两个点分版本串：a>b 返回正、相等返回 0、a<b 返回负。空串按全零（最低）。
func compareAgentVersions(a, b string) int {
	segA, segB := parseAgentVersion(a), parseAgentVersion(b)
	for i := 0; i < deliveryAgentVersionSegments; i++ {
		if segA[i] != segB[i] {
			return segA[i] - segB[i]
		}
	}
	return 0
}

// parseAgentVersion 把版本串解析为定长数值段：按 `.` 切分，每段取前导数字（非数字后缀忽略），
// 段数不足补 0、超出截断——使 `1.4` 与 `1.4.0`、`1.4.0-rc.1` 与 `1.4.0` 同序。
func parseAgentVersion(version string) [deliveryAgentVersionSegments]int {
	var segs [deliveryAgentVersionSegments]int
	parts := strings.Split(strings.TrimSpace(version), ".")
	for i := 0; i < len(parts) && i < deliveryAgentVersionSegments; i++ {
		segs[i] = leadingNumber(parts[i])
	}
	return segs
}

// leadingNumber 取一段版本的前导数字（`6` / `0-rc` 分别得 6 / 0）；无前导数字得 0。
func leadingNumber(part string) int {
	end := 0
	for end < len(part) && part[end] >= '0' && part[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	value, err := strconv.Atoi(part[:end])
	if err != nil {
		return 0 // 理论上不可达（已限定纯数字子串），防御性兜底
	}
	return value
}

// deliveryCapabilityRejectReason 组装「agent 能力不足」的可读原因（落目标行 error 展示给运维）：
// 必须同时给出**实际版本**与**最低要求**，否则运维只看到「被拒了」而无从处置。
// 未上报版本按「未提供」表述（不能显示空字符串，那与「版本就是空」无从区分）。
func deliveryCapabilityRejectReason(version, minVersion string) string {
	actual := strings.TrimSpace(version)
	if actual == "" {
		actual = "未提供"
	}
	return fmt.Sprintf("agent 版本 %s 低于交付所需最低版本 %s，已拒绝下发交付命令；请升级该服 agent 后重试（或调低运维设置 %s）",
		actual, minVersion, SettingDeliveryMinAgentVersion)
}

// deliveryCapabilityUnsupported 构造带不合格 serverId 清单的能力拒绝错误（启动期整单拒绝用）：
// 运维要立刻知道「哪几台要升级」，故把排序后的 serverId 清单与首个目标的完整原因一并拼进 message。
func deliveryCapabilityUnsupported(unsupported map[string]string, minVersion string) *apperr.Error {
	serverIDs := make([]string, 0, len(unsupported))
	for serverID := range unsupported {
		serverIDs = append(serverIDs, serverID)
	}
	sort.Strings(serverIDs)
	return apperr.New(http.StatusConflict, apperr.ErrDeliveryAgentCapabilityUnsupported.Code,
		fmt.Sprintf("%s；不合格目标：%s", deliveryCapabilityRejectReason(unsupported[serverIDs[0]], minVersion),
			strings.Join(serverIDs, ", ")))
}

// deliveryAgentVersionLookup 是 agent 版本批量查询的窄依赖（由 repository 实现，一次取回全部目标，防逐台查库）。
//
// 带 WithTx：守卫在「已开启事务的调用链」里（如审批适配器在事务内启动变更单）必须复用同一连接，
// 不能另开一条查询——单连接池（测试 / 受限部署）下另开会与外层事务互等死锁。
type deliveryAgentVersionLookup interface {
	WithTx(tx *gorm.DB) deliveryAgentVersionLookup
	FindVersionsByServerIDs(namespaceID uint, serverIDs []string) (map[string]string, error)
}

// capabilityGuard 是交付能力版本守卫：持有版本查询与最低版本设置读源，供启动 / 下发两处复用。
type capabilityGuard struct {
	versions deliveryAgentVersionLookup
	minFn    func() string
}

// newCapabilityGuard 构造守卫；minFn 读运维设置（热改即生效）。
func newCapabilityGuard(versions deliveryAgentVersionLookup, minFn func() string) *capabilityGuard {
	return &capabilityGuard{versions: versions, minFn: minFn}
}

// minVersion 读当前最低版本要求（热读运维设置；未配置读源返回空串=不校验）。
func (g *capabilityGuard) minVersion() string {
	if g == nil || g.minFn == nil {
		return ""
	}
	return strings.TrimSpace(g.minFn())
}

// filterUnsupported 返回给定 serverId 中**不具备**流式交付能力的子集（serverId → 实际版本）。
// 守卫未装配 / 查询源缺失 / 下限为空时返回空集——不因守卫自身的装配缺失而阻断交付。
func (g *capabilityGuard) filterUnsupported(namespaceID uint, serverIDs []string) (map[string]string, error) {
	if g == nil || g.versions == nil || len(serverIDs) == 0 {
		return nil, nil
	}
	minVersion := g.minVersion()
	if minVersion == "" {
		return nil, nil // 下限为空 = 关闭守卫
	}
	versions, err := g.versions.FindVersionsByServerIDs(namespaceID, serverIDs)
	if err != nil {
		return nil, err
	}
	unsupported := make(map[string]string)
	for _, serverID := range serverIDs {
		actual := versions[serverID]
		if deliveryAgentSupportsStreaming(actual, minVersion) {
			continue
		}
		unsupported[serverID] = actual
	}
	return unsupported, nil
}

// agentVersionLookup 把 repository 的身份仓库适配为守卫的窄查询接口（Go 不支持协变返回，
// 仓库的 WithTx 返回具体类型，故在此包一层：WithTx 返回接口自身）。
type agentVersionLookup struct {
	repo *repository.AgentIdentityRepository
}

// WithTx 返回绑定到事务的查询副本。
func (l *agentVersionLookup) WithTx(tx *gorm.DB) deliveryAgentVersionLookup {
	return &agentVersionLookup{repo: l.repo.WithTx(tx)}
}

// FindVersionsByServerIDs 批量取各 server 的 agent 自报版本。
func (l *agentVersionLookup) FindVersionsByServerIDs(namespaceID uint, serverIDs []string) (map[string]string, error) {
	return l.repo.FindVersionsByServerIDs(namespaceID, serverIDs)
}

// withTx 返回复用当前事务连接的守卫副本：供「已开启事务的调用链」内做能力校验，避免另开连接互等。
func (g *capabilityGuard) withTx(tx *gorm.DB) *capabilityGuard {
	if g == nil || tx == nil {
		return g
	}
	copied := *g
	if g.versions != nil {
		copied.versions = g.versions.WithTx(tx)
	}
	return &copied
}
