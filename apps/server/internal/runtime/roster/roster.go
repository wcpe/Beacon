// Package roster 持有「玩家 → 所在服」的内存名册快照（FR-145/149，见 spec §4.1、ADR-0063 决策 4）。
//
// 由连接明细 open/close 驱动，供按玩家寻址的跨服消息解析实际目标服（resolved_server_id）。名册属注册健康
// 类内存事实：独立 RWMutex、与三大运行态锁（Registry / Hub / Health）互不嵌套，锁内纯内存操作、绝不碰 DB。
// 进程重启后由 status=open 连接行重建（RebuildFrom）；重建期间按玩家寻址可能短暂落空，接受短暂错位（spec §8-9）。
//
// 名册同时持有两套索引，供按玩家寻址使用：
//   - **UUID → 位置**（权威）：Minecraft 账号 UUID 全局唯一，故跨 namespace 寻址亦可解析——由此得知目标玩家
//     所在 namespace，供上层判定是否跨域并校验 namespace_trust。
//   - **玩家名（小写归一）→ UUID**：上层门面 `sendToPlayer(playerName, ...)` 面向调用方收的是**玩家名**，
//     而子服 agent 物理上看不到异服玩家、无法自行把名换成 UUID（Bukkit 只有本服在线列表），故转换只能在
//     持有全量连接明细的控制面完成。缺这套索引时按名寻址必落空，报 `player_not_online`。
package roster

import (
	"strings"
	"sync"
)

// Location 是一名在线玩家的当前位置解析结果。
type Location struct {
	NamespaceID uint   // 玩家所在 namespace
	ServerID    string // 解析出的实际目标服（消息投递目标）
}

// entry 是名册内部条目：除位置外记 connID，供 close 精确摘除（避免旧连接的 close 误删新连接条目）。
type entry struct {
	Location
	ConnID string
	// PlayerName 登录时的玩家名（原样保留），close 时据它同步摘除 byName 索引。
	PlayerName string
}

// Store 是全体在线玩家名册，独立 RWMutex 保护。
type Store struct {
	mu       sync.RWMutex
	byPlayer map[string]entry  // playerUUID -> 位置
	byName   map[string]string // 小写玩家名 -> playerUUID（供按名寻址；键小写归一）
}

// NewStore 构造空名册。
func NewStore() *Store {
	return &Store{
		byPlayer: make(map[string]entry),
		byName:   make(map[string]string),
	}
}

// normName 归一玩家名字键：Minecraft 玩家名大小写不敏感（"Steve" 与 "steve" 同一账号），故索引键统一小写。
func normName(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// ApplyOpen 玩家连接建立：登记 / 覆盖其位置（重连以最新连接为准）。
//
// playerName 可为空（连接明细不强制携带）：为空时只更新 UUID 索引，按名寻址对该玩家不可用。
func (s *Store) ApplyOpen(namespaceID uint, playerUUID, playerName, connID, serverID string) {
	if playerUUID == "" {
		return
	}
	s.mu.Lock()
	// 先摘除该 UUID 的旧名键（玩家改名 / 重连换了名字时，旧名键不该继续指向他）
	if old, ok := s.byPlayer[playerUUID]; ok && old.PlayerName != "" {
		if key := normName(old.PlayerName); key != normName(playerName) {
			delete(s.byName, key)
		}
	}
	s.byPlayer[playerUUID] = entry{
		Location:   Location{NamespaceID: namespaceID, ServerID: serverID},
		ConnID:     connID,
		PlayerName: playerName,
	}
	if key := normName(playerName); key != "" {
		s.byName[key] = playerUUID
	}
	s.mu.Unlock()
}

// ApplyClose 玩家连接断开：仅当当前条目正是该连接（connID 相符）时摘除，防旧连接 close 误删新条目。
// 摘除 UUID 条目时**同步**摘除其名键（否则按名寻址会命中已离线玩家）。
func (s *Store) ApplyClose(playerUUID, connID string) {
	if playerUUID == "" {
		return
	}
	s.mu.Lock()
	if cur, ok := s.byPlayer[playerUUID]; ok && cur.ConnID == connID {
		delete(s.byPlayer, playerUUID)
		if key := normName(cur.PlayerName); key != "" {
			// 仅当该名键仍指向本 UUID 时才删（防玩家改名后旧连接的 close 误删新名键）
			if s.byName[key] == playerUUID {
				delete(s.byName, key)
			}
		}
	}
	s.mu.Unlock()
}

// Resolve 按玩家 UUID 解析当前位置；不在线返回 (Location{}, false)。
func (s *Store) Resolve(playerUUID string) (Location, bool) {
	s.mu.RLock()
	e, ok := s.byPlayer[playerUUID]
	s.mu.RUnlock()
	if !ok {
		return Location{}, false
	}
	return e.Location, true
}

// ResolvePlayer 解析玩家当前位置，**优先按 UUID、未命中回退按名**（大小写不敏感）。
//
// 兼容两种入参的原因：门面 `sendToPlayer(playerName, ...)` 面向调用方收玩家名，而子服 agent 看不到异服玩家、
// 无法自行把名换成 UUID（Bukkit 只有本服在线列表），故由持有全量连接明细的本名册完成两种形态的解析。
// 保留 UUID 优先：UUID 是权威键且能跨 namespace 解析，玩家名仅作兜底（改名 / 大小写差异不影响 UUID 命中）。
func (s *Store) ResolvePlayer(playerID string) (Location, bool) {
	if playerID == "" {
		return Location{}, false
	}
	s.mu.RLock()
	e, ok := s.byPlayer[playerID]
	if !ok {
		if uuid, found := s.byName[normName(playerID)]; found {
			e, ok = s.byPlayer[uuid]
		}
	}
	s.mu.RUnlock()
	if !ok {
		return Location{}, false
	}
	return e.Location, true
}

// RebuildEntry 是重建名册的一条输入（进程重启从 status=open 连接行读出）。
type RebuildEntry struct {
	PlayerUUID  string
	PlayerName  string
	ConnID      string
	NamespaceID uint
	ServerID    string
}

// RebuildFrom 用一批 open 连接整批原子替换名册（进程重启重建，spec §4.1）：
// 先在锁外构建新 map，锁内仅指针交换，避免长时间持锁。
func (s *Store) RebuildFrom(entries []RebuildEntry) {
	next := make(map[string]entry, len(entries))
	nextByName := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.PlayerUUID == "" {
			continue
		}
		next[e.PlayerUUID] = entry{
			Location:   Location{NamespaceID: e.NamespaceID, ServerID: e.ServerID},
			ConnID:     e.ConnID,
			PlayerName: e.PlayerName,
		}
		if key := normName(e.PlayerName); key != "" {
			nextByName[key] = e.PlayerUUID
		}
	}
	s.mu.Lock()
	s.byPlayer = next
	s.byName = nextByName
	s.mu.Unlock()
}

// Count 返回当前在线玩家数（自观测 / 测试用）。
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byPlayer)
}
