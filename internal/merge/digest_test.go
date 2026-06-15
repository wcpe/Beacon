package merge

import "testing"

// TestMD5HexKnown 已知值校验。
func TestMD5HexKnown(t *testing.T) {
	// echo -n "" | md5
	if got := MD5Hex(""); got != "d41d8cd98f00b204e9800998ecf8427e" {
		t.Errorf("空串 md5 错误：%s", got)
	}
	// echo -n "abc" | md5
	if got := MD5Hex("abc"); got != "900150983cd24fb0d6963f7d28e17f72" {
		t.Errorf("abc md5 错误：%s", got)
	}
}

// TestOverallMD5AvoidsSetCollision {A:x} 与 {B:x} 必须算出不同整体 md5（ADR-0008）。
func TestOverallMD5AvoidsSetCollision(t *testing.T) {
	a := OverallMD5(map[string]string{"A": "deadbeef"})
	b := OverallMD5(map[string]string{"B": "deadbeef"})
	if a == b {
		t.Error("dataId 名未纳入哈希：集合碰撞未消除")
	}
}

// TestOverallMD5OrderIndependent 整体 md5 与 map 遍历顺序无关（内部按 dataId 排序）。
func TestOverallMD5OrderIndependent(t *testing.T) {
	m := map[string]string{"mysql.yml": "aaa", "redis.yml": "bbb", "app.json": "ccc"}
	want := OverallMD5(m)
	for i := 0; i < 50; i++ {
		// 重新构造同内容 map，遍历顺序随机，结果必须一致
		again := map[string]string{"redis.yml": "bbb", "app.json": "ccc", "mysql.yml": "aaa"}
		if OverallMD5(again) != want {
			t.Fatalf("第 %d 次整体 md5 漂移", i)
		}
	}
}

// TestOverallMD5ChangesWithContent 任一 dataId 内容变 → 整体 md5 变。
func TestOverallMD5ChangesWithContent(t *testing.T) {
	base := OverallMD5(map[string]string{"a.yml": "x", "b.yml": "y"})
	changed := OverallMD5(map[string]string{"a.yml": "x", "b.yml": "z"})
	if base == changed {
		t.Error("内容变化未反映到整体 md5")
	}
}
