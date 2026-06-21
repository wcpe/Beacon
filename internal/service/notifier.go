package service

import (
	"log/slog"

	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
)

// PushRecorder 是推送计数的窄接口（由 metrics 实现，可选注入；未注入即不计数）。
type PushRecorder interface {
	IncPushNotify()
}

// ChangeNotifier 在配置/文件/指派变更（事务提交后）算最小受影响 serverId 集合并唤醒其 waiter。
// 受影响集合：global→该 ns 全部；group→该 group（查内存）；zone→反查 DB 指派；server/改派→单 serverId。
// 配置（通道A）与文件（通道B）各持一个独立 Hub，发布只唤醒对应通道的 waiter，互不触发无谓重算（见 ADR-0010）。
type ChangeNotifier struct {
	hub         *longpoll.Hub // 配置长轮询唤醒集合
	fileHub     *longpoll.Hub // 文件长轮询唤醒集合（独立）
	topologyHub *longpoll.Hub // 拓扑 watch 唤醒集合（namespace 级，FR-29）
	commandHub  *longpoll.Hub // 命令待办唤醒集合（serverId 级，FR-39）
	registry    *runtime.Registry
	assignRepo  *repository.ZoneAssignmentRepository
	metrics     PushRecorder // 可选，推送计数（见 ADR-0020）
}

// NewChangeNotifier 构造唤醒器（hub 配置、fileHub 文件、topologyHub 拓扑、commandHub 命令待办，互相独立）。
func NewChangeNotifier(hub, fileHub, topologyHub, commandHub *longpoll.Hub, registry *runtime.Registry, assignRepo *repository.ZoneAssignmentRepository) *ChangeNotifier {
	return &ChangeNotifier{hub: hub, fileHub: fileHub, topologyHub: topologyHub, commandHub: commandHub, registry: registry, assignRepo: assignRepo}
}

// SetMetrics 注入推送计数器（启动时装配；未注入则不计数）。
func (n *ChangeNotifier) SetMetrics(m PushRecorder) {
	n.metrics = m
}

// recordPush 在每次唤醒触发时累加推送计数（注入了才计）。
func (n *ChangeNotifier) recordPush() {
	if n.metrics != nil {
		n.metrics.IncPushNotify()
	}
}

// NotifyConfigChange 按变更配置项的 scope 唤醒受影响实例（仅配置通道）。
func (n *ChangeNotifier) NotifyConfigChange(ns, scopeLevel, group, scopeTarget string) {
	n.recordPush()
	n.notifyScope(n.hub, ns, scopeLevel, group, scopeTarget)
}

// NotifyFileChange 按变更文件对象的 scope 唤醒受影响实例（仅文件通道）。
func (n *ChangeNotifier) NotifyFileChange(ns, scopeLevel, group, scopeTarget string) {
	n.recordPush()
	n.notifyScope(n.fileHub, ns, scopeLevel, group, scopeTarget)
}

// NotifyServer 唤醒单个 serverId（zone 改派/取消时其解析归属变化，配置与文件两通道都受影响）。
func (n *ChangeNotifier) NotifyServer(ns, serverID string) {
	n.recordPush()
	n.hub.Notify(ns, []string{serverID})
	n.fileHub.Notify(ns, []string{serverID})
}

// NotifyServers 按 serverId 名单唤醒配置通道（灰度发布 / abort 仅影响 cohort 成员，FR-9）。
// 名单为空则不唤醒（无受影响 server）。只动配置通道，不触发文件通道无谓重算。
func (n *ChangeNotifier) NotifyServers(ns string, serverIDs []string) {
	if len(serverIDs) == 0 {
		return
	}
	n.recordPush()
	n.hub.Notify(ns, serverIDs)
}

// NotifyTopologyChange 唤醒该 namespace 全部拓扑 watch waiter（FR-29）。
// 实例上线/下线/改派 zone 时由变更点调用；被唤醒方重算拓扑摘要、真变才推 topology-changed。
func (n *ChangeNotifier) NotifyTopologyChange(ns string) {
	n.recordPush()
	n.topologyHub.NotifyNamespace(ns)
}

// NotifyCommand 唤醒某 serverId 的命令待办 waiter（FR-39，见 ADR-0027）：建命令提交后调用，
// 该 agent 的 SSE 流被唤醒即发 command-pending，agent 拉 /commands 执行。
// agent 离线则无 waiter（信号自然丢弃），命令留待其重连时主动拉取或超时清理。
func (n *ChangeNotifier) NotifyCommand(ns, serverID string) {
	n.recordPush()
	n.commandHub.Notify(ns, []string{serverID})
}

// notifyScope 按 scope 算最小受影响集合并唤醒指定 Hub 的 waiter。
func (n *ChangeNotifier) notifyScope(hub *longpoll.Hub, ns, scopeLevel, group, scopeTarget string) {
	switch scopeLevel {
	case model.ScopeGlobal:
		hub.NotifyNamespace(ns)
	case model.ScopeGroup:
		hub.Notify(ns, n.serverIDsInGroup(ns, group))
	case model.ScopeZone:
		ids, err := n.serverIDsInZone(ns, group, scopeTarget)
		if err != nil {
			slog.Warn("反查 zone 成员失败，跳过唤醒", "namespace", ns, "group", group, "zone", scopeTarget, "错误", err)
			return
		}
		hub.Notify(ns, ids)
	case model.ScopeServer:
		hub.Notify(ns, []string{scopeTarget})
	}
}

// serverIDsInGroup 从内存注册表取某 group 下的实例 serverId。
func (n *ChangeNotifier) serverIDsInGroup(ns, group string) []string {
	insts := n.registry.List(runtime.Filter{Namespace: ns, Group: group})
	ids := make([]string, 0, len(insts))
	for _, i := range insts {
		ids = append(ids, i.ServerID)
	}
	return ids
}

// serverIDsInZone 反查 DB 指派取某 (group, zone) 下的 serverId。
func (n *ChangeNotifier) serverIDsInZone(ns, group, zone string) ([]string, error) {
	list, err := n.assignRepo.List(ns, group, zone)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(list))
	for _, a := range list {
		ids = append(ids, a.ServerID)
	}
	return ids, nil
}
