// Package roster 持有「玩家 → 所在服」的内存名册快照（FR-145/149，见 spec §4.1、ADR-0063 决策 4）。
//
// 由连接明细 open/close 驱动，供按玩家寻址的跨服消息解析实际目标服（resolved_server_id）。名册属注册健康
// 类内存事实：独立 RWMutex、与三大运行态锁（Registry / Hub / Health）互不嵌套，锁内纯内存操作、绝不碰 DB。
// 进程重启后由 status=open 连接行重建（RebuildFrom）；重建期间按玩家寻址可能短暂落空，接受短暂错位（spec §8-9）。
//
// 键用玩家 UUID（Minecraft 账号 UUID 全局唯一），故跨 namespace 玩家寻址亦可解析——由此得知目标玩家所在
// namespace，供上层判定是否跨域并校验 namespace_trust。
package roster

import "sync"

// Location 是一名在线玩家的当前位置解析结果。
type Location struct {
	NamespaceID uint   // 玩家所在 namespace
	ServerID    string // 解析出的实际目标服（消息投递目标）
}

// entry 是名册内部条目：除位置外记 connID，供 close 精确摘除（避免旧连接的 close 误删新连接条目）。
type entry struct {
	Location
	ConnID string
}

// Store 是全体在线玩家名册，独立 RWMutex 保护。
type Store struct {
	mu       sync.RWMutex
	byPlayer map[string]entry // playerUUID -> 位置
}

// NewStore 构造空名册。
func NewStore() *Store {
	return &Store{byPlayer: make(map[string]entry)}
}

// ApplyOpen 玩家连接建立：登记 / 覆盖其位置（重连以最新连接为准）。
func (s *Store) ApplyOpen(namespaceID uint, playerUUID, connID, serverID string) {
	if playerUUID == "" {
		return
	}
	s.mu.Lock()
	s.byPlayer[playerUUID] = entry{Location: Location{NamespaceID: namespaceID, ServerID: serverID}, ConnID: connID}
	s.mu.Unlock()
}

// ApplyClose 玩家连接断开：仅当当前条目正是该连接（connID 相符）时摘除，防旧连接 close 误删新条目。
func (s *Store) ApplyClose(playerUUID, connID string) {
	if playerUUID == "" {
		return
	}
	s.mu.Lock()
	if cur, ok := s.byPlayer[playerUUID]; ok && cur.ConnID == connID {
		delete(s.byPlayer, playerUUID)
	}
	s.mu.Unlock()
}

// Resolve 解析玩家当前位置；不在线返回 (Location{}, false)。
func (s *Store) Resolve(playerUUID string) (Location, bool) {
	s.mu.RLock()
	e, ok := s.byPlayer[playerUUID]
	s.mu.RUnlock()
	if !ok {
		return Location{}, false
	}
	return e.Location, true
}

// RebuildEntry 是重建名册的一条输入（进程重启从 status=open 连接行读出）。
type RebuildEntry struct {
	PlayerUUID  string
	ConnID      string
	NamespaceID uint
	ServerID    string
}

// RebuildFrom 用一批 open 连接整批原子替换名册（进程重启重建，spec §4.1）：
// 先在锁外构建新 map，锁内仅指针交换，避免长时间持锁。
func (s *Store) RebuildFrom(entries []RebuildEntry) {
	next := make(map[string]entry, len(entries))
	for _, e := range entries {
		if e.PlayerUUID == "" {
			continue
		}
		next[e.PlayerUUID] = entry{
			Location: Location{NamespaceID: e.NamespaceID, ServerID: e.ServerID},
			ConnID:   e.ConnID,
		}
	}
	s.mu.Lock()
	s.byPlayer = next
	s.mu.Unlock()
}

// Count 返回当前在线玩家数（自观测 / 测试用）。
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byPlayer)
}
