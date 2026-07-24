package roster

import "testing"

// TestRosterOpenResolveClose 校验 open 登记、Resolve 命中、close 摘除的基本闭环。
func TestRosterOpenResolveClose(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "alice", "conn-1", "game-3")

	loc, ok := s.Resolve("alice")
	if !ok || loc.ServerID != "game-3" || loc.NamespaceID != 1 {
		t.Fatalf("Resolve 应命中 game-3@ns1，实际 %+v ok=%v", loc, ok)
	}

	s.ApplyClose("alice", "conn-1")
	if _, ok := s.Resolve("alice"); ok {
		t.Fatalf("close 后应摘除，Resolve 不应命中")
	}
}

// TestRosterStaleCloseIgnored 校验旧连接的 close 不误删新连接条目（重连场景）。
func TestRosterStaleCloseIgnored(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "alice", "conn-1", "game-3")
	s.ApplyOpen(1, "alice", "conn-2", "game-7") // 重连到新连接 / 新服
	s.ApplyClose("alice", "conn-1")             // 旧连接的迟到 close

	loc, ok := s.Resolve("alice")
	if !ok || loc.ServerID != "game-7" {
		t.Fatalf("旧连接 close 不应删除新条目，应仍解析到 game-7，实际 %+v ok=%v", loc, ok)
	}
}

// TestRosterCrossNamespace 校验跨 namespace 玩家可解析出其所在 namespace（全局 UUID 键）。
func TestRosterCrossNamespace(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(2, "bob", "conn-9", "game-5") // bob 在 ns2
	loc, ok := s.Resolve("bob")
	if !ok || loc.NamespaceID != 2 {
		t.Fatalf("应解析出 bob 所在 ns2，实际 %+v ok=%v", loc, ok)
	}
}

// TestRosterRebuild 校验整批重建原子替换、旧条目清空。
func TestRosterRebuild(t *testing.T) {
	s := NewStore()
	s.ApplyOpen(1, "stale", "c0", "game-1")
	s.RebuildFrom([]RebuildEntry{
		{PlayerUUID: "alice", ConnID: "c1", NamespaceID: 1, ServerID: "game-2"},
		{PlayerUUID: "", ConnID: "cx", NamespaceID: 1, ServerID: "game-9"}, // 空 UUID 跳过
	})
	if _, ok := s.Resolve("stale"); ok {
		t.Fatalf("重建应清空旧条目 stale")
	}
	if loc, ok := s.Resolve("alice"); !ok || loc.ServerID != "game-2" {
		t.Fatalf("重建应含 alice→game-2，实际 %+v ok=%v", loc, ok)
	}
	if s.Count() != 1 {
		t.Fatalf("重建后应只有 1 条（空 UUID 跳过），实际 %d", s.Count())
	}
}
