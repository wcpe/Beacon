package service

import (
	"log/slog"

	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/longpoll"
)

// ChangeNotifier 在配置/文件/指派变更（事务提交后）算最小受影响 serverId 集合并唤醒其 waiter。
// 受影响集合：global→该 ns 全部；group→该 group（查内存）；zone→反查 DB 指派；server/改派→单 serverId。
// 配置（通道A）与文件（通道B）各持一个独立 Hub，发布只唤醒对应通道的 waiter，互不触发无谓重算（见 ADR-0010）。
type ChangeNotifier struct {
	hub        *longpoll.Hub // 配置长轮询唤醒集合
	fileHub    *longpoll.Hub // 文件长轮询唤醒集合（独立）
	registry   *runtime.Registry
	assignRepo *repository.ZoneAssignmentRepository
}

// NewChangeNotifier 构造唤醒器（hub 为配置通道、fileHub 为文件通道，二者独立）。
func NewChangeNotifier(hub, fileHub *longpoll.Hub, registry *runtime.Registry, assignRepo *repository.ZoneAssignmentRepository) *ChangeNotifier {
	return &ChangeNotifier{hub: hub, fileHub: fileHub, registry: registry, assignRepo: assignRepo}
}

// NotifyConfigChange 按变更配置项的 scope 唤醒受影响实例（仅配置通道）。
func (n *ChangeNotifier) NotifyConfigChange(ns, scopeLevel, group, scopeTarget string) {
	n.notifyScope(n.hub, ns, scopeLevel, group, scopeTarget)
}

// NotifyFileChange 按变更文件对象的 scope 唤醒受影响实例（仅文件通道）。
func (n *ChangeNotifier) NotifyFileChange(ns, scopeLevel, group, scopeTarget string) {
	n.notifyScope(n.fileHub, ns, scopeLevel, group, scopeTarget)
}

// NotifyServer 唤醒单个 serverId（zone 改派/取消时其解析归属变化，配置与文件两通道都受影响）。
func (n *ChangeNotifier) NotifyServer(ns, serverID string) {
	n.hub.Notify(ns, []string{serverID})
	n.fileHub.Notify(ns, []string{serverID})
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
