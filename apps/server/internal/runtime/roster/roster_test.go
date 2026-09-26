package roster

import "testing"

// TestRosterOpenResolveClose 校验 open 登记、Resolve 命中、close 摘除的基本闭环。
func TestRosterOpenResolveClose(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-alice", "Alice", "conn-1", "game-3")

	loc, ok := s.Resolve("uuid-alice")
	if !ok || loc.ServerID != "game-3" || loc.NamespaceID != 1 {
		t.Fatalf("Resolve 应命中 game-3@ns1，实际 %+v ok=%v", loc, ok)
	}

	s.ApplyClose("uuid-alice", "conn-1")
	if _, ok := s.Resolve("uuid-alice"); ok {
		t.Fatalf("close 后应摘除，Resolve 不应命中")
	}
}

// TestRosterStaleCloseIgnored 校验旧连接的 close 不误删新连接条目（重连场景）。
func TestRosterStaleCloseIgnored(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-alice", "Alice", "conn-1", "game-3")
	s.ApplyOpen(1, "uuid-alice", "Alice", "conn-2", "game-7") // 重连到新连接 / 新服
	s.ApplyClose("uuid-alice", "conn-1")                      // 旧连接的迟到 close

	loc, ok := s.Resolve("uuid-alice")
	if !ok || loc.ServerID != "game-7" {
		t.Fatalf("旧连接 close 不应删除新条目，应仍解析到 game-7，实际 %+v ok=%v", loc, ok)
	}
}

// TestRosterCrossNamespace 校验跨 namespace 玩家可解析出其所在 namespace（全局 UUID 键）。
func TestRosterCrossNamespace(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(2, "uuid-bob", "Bob", "conn-9", "game-5") // bob 在 ns2
	loc, ok := s.Resolve("uuid-bob")
	if !ok || loc.NamespaceID != 2 {
		t.Fatalf("应解析出 bob 所在 ns2，实际 %+v ok=%v", loc, ok)
	}
}

// TestRosterRebuild 校验整批重建原子替换、旧条目清空。
func TestRosterRebuild(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-stale", "Stale", "c0", "game-1")
	s.RebuildFrom([]RebuildEntry{
		{PlayerUUID: "uuid-alice", PlayerName: "Alice", ConnID: "c1", NamespaceID: 1, ServerID: "game-2"},
		{PlayerUUID: "", PlayerName: "Ghost", ConnID: "cx", NamespaceID: 1, ServerID: "game-9"}, // 空 UUID 跳过
	})
	if _, ok := s.Resolve("uuid-stale"); ok {
		t.Fatalf("重建应清空旧条目 stale")
	}
	if loc, ok := s.Resolve("uuid-alice"); !ok || loc.ServerID != "game-2" {
		t.Fatalf("重建应含 alice→game-2，实际 %+v ok=%v", loc, ok)
	}
	if s.Count() != 1 {
		t.Fatalf("重建后应只有 1 条（空 UUID 跳过），实际 %d", s.Count())
	}
}

// TestRosterResolveByName 按**玩家名**解析（上层门面 sendToPlayer 的入参形态）。
//
// 子服 agent 看不到异服玩家、无法自行把名换成 UUID，故名册必须能按名解析；缺这条路径时
// 按玩家寻址的消息会全部落空并报 player_not_online。
func TestRosterResolveByName(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-steve", "Steve", "c1", "game-9")

	loc, ok := s.ResolvePlayer("Steve")
	if !ok || loc.ServerID != "game-9" {
		t.Fatalf("按名应解析到 game-9，实际 %+v ok=%v", loc, ok)
	}
	// UUID 仍优先命中（权威键），且两种形态解析结果一致
	byUUID, okUUID := s.ResolvePlayer("uuid-steve")
	if !okUUID || byUUID != loc {
		t.Fatalf("按 UUID 与按名应得同一位置，实际 uuid=%+v name=%+v", byUUID, loc)
	}
}

// TestRosterResolveByNameCaseInsensitive 玩家名大小写不敏感（Minecraft 语义："Steve" 与 "steve" 同一账号）。
func TestRosterResolveByNameCaseInsensitive(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-steve", "Steve", "c1", "game-9")

	for _, variant := range []string{"steve", "STEVE", "StEvE"} {
		if loc, ok := s.ResolvePlayer(variant); !ok || loc.ServerID != "game-9" {
			t.Fatalf("按名 %q 应解析到 game-9（大小写不敏感），实际 %+v ok=%v", variant, loc, ok)
		}
	}
}

// TestRosterNameRemovedOnClose 校验 close 时按名索引同步摘除（否则会给已离线玩家投递）。
func TestRosterNameRemovedOnClose(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-steve", "Steve", "c1", "game-9")
	s.ApplyClose("uuid-steve", "c1")

	if _, ok := s.ResolvePlayer("Steve"); ok {
		t.Fatalf("close 后按名不应再命中")
	}
}

// TestRosterRebuildRestoresNameIndex 守护「重启后按名仍可解析」——重建必须一并恢复名索引。
//
// 这条直接守住一个曾出现的缺陷：重建的 SELECT 未取 player_name，只加内存索引不改查询的话，
// 控制面一重启名索引即为空、按名寻址立刻退化为 player_not_online。
func TestRosterRebuildRestoresNameIndex(t *testing.T) {
	s := NewStore()
	s.RebuildFrom([]RebuildEntry{
		{PlayerUUID: "uuid-steve", PlayerName: "Steve", ConnID: "c1", NamespaceID: 1, ServerID: "game-9"},
	})

	if loc, ok := s.ResolvePlayer("Steve"); !ok || loc.ServerID != "game-9" {
		t.Fatalf("重建后按名应可解析到 game-9，实际 %+v ok=%v", loc, ok)
	}
}

// TestRosterReconnectKeepsNameIndexConsistent 玩家换服重连后，按名应指向**新**服。
func TestRosterReconnectKeepsNameIndexConsistent(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-steve", "Steve", "c1", "game-1")
	s.ApplyOpen(1, "uuid-steve", "Steve", "c2", "game-7") // 换服重连

	if loc, ok := s.ResolvePlayer("Steve"); !ok || loc.ServerID != "game-7" {
		t.Fatalf("重连后按名应指向新服 game-7，实际 %+v ok=%v", loc, ok)
	}
}

// TestRosterEmptyNameSkipped 空玩家名只登记 UUID 索引，不污染名索引（且不 panic）。
func TestRosterEmptyNameSkipped(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "uuid-noname", "", "c1", "game-3")

	if _, ok := s.ResolvePlayer(""); ok {
		t.Fatalf("空名不应命中")
	}
	if loc, ok := s.ResolvePlayer("uuid-noname"); !ok || loc.ServerID != "game-3" {
		t.Fatalf("空名不应影响 UUID 解析，实际 %+v ok=%v", loc, ok)
	}
	s.ApplyClose("uuid-noname", "c1") // 空名下 close 不应 panic
}
