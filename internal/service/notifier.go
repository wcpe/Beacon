package service

import (
	"log/slog"

	"beacon/internal/model"
	"beacon/internal/repository"
	"beacon/internal/runtime"
	"beacon/internal/runtime/longpoll"
)

// ChangeNotifier 在配置/指派变更（事务提交后）算最小受影响 serverId 集合并唤醒其 waiter。
// 受影响集合：global→该 ns 全部；group→该 group（查内存）；zone→反查 DB 指派；server/改派→单 serverId。
type ChangeNotifier struct {
	hub        *longpoll.Hub
	registry   *runtime.Registry
	assignRepo *repository.ZoneAssignmentRepository
}

// NewChangeNotifier 构造唤醒器。
func NewChangeNotifier(hub *longpoll.Hub, registry *runtime.Registry, assignRepo *repository.ZoneAssignmentRepository) *ChangeNotifier {
	return &ChangeNotifier{hub: hub, registry: registry, assignRepo: assignRepo}
}

// NotifyConfigChange 按变更配置项的 scope 唤醒受影响实例。
func (n *ChangeNotifier) NotifyConfigChange(ns, scopeLevel, group, scopeTarget string) {
	switch scopeLevel {
	case model.ScopeGlobal:
		n.hub.NotifyNamespace(ns)
	case model.ScopeGroup:
		n.hub.Notify(ns, n.serverIDsInGroup(ns, group))
	case model.ScopeZone:
		ids, err := n.serverIDsInZone(ns, group, scopeTarget)
		if err != nil {
			slog.Warn("反查 zone 成员失败，跳过唤醒", "namespace", ns, "group", group, "zone", scopeTarget, "错误", err)
			return
		}
		n.hub.Notify(ns, ids)
	case model.ScopeServer:
		n.hub.Notify(ns, []string{scopeTarget})
	}
}

// NotifyServer 唤醒单个 serverId（zone 改派/取消时其解析归属变化）。
func (n *ChangeNotifier) NotifyServer(ns, serverID string) {
	n.hub.Notify(ns, []string{serverID})
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
