package service

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

// referenceDigest 按规格 §4.3 文字定义独立复算摘要（与被测实现分立的参考实现），
// 供交叉校验 computeManifestDigest 是否严格实现文档算法（也是 agent 端须对齐的向量）。
func referenceDigest(entries []ManifestEntry) string {
	// 手工按 path 升序（测试输入已排好或乱序时此处不重排——由调用方保证顺序等同排序后）。
	var s string
	for _, e := range entries {
		s += e.Path + "\n" + e.SHA256 + "\n" + strconv.FormatInt(e.Size, 10) + "\n" + strconv.FormatInt(e.MtimeMs, 10) + "\n"
	}
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestComputeManifestDigest_MatchesDocumentedAlgorithm(t *testing.T) {
	zeros := "0000000000000000000000000000000000000000000000000000000000000000"
	entries := []ManifestEntry{{Path: "a", SHA256: zeros, Size: 1, MtimeMs: 2}}
	got := computeManifestDigest(entries)
	// 参考串 = "a\n<64零>\n1\n2\n"
	want := referenceDigest(entries)
	if got != want {
		t.Fatalf("摘要不符文档算法：got=%s want=%s", got, want)
	}
	// 空清单摘要 = sha256("")
	empty := computeManifestDigest(nil)
	if empty != referenceDigest(nil) {
		t.Fatalf("空清单摘要应为 sha256(\"\")，实际 %s", empty)
	}
}

// TestComputeManifestDigest_CrossLanguageVector 锁死 Go 与 agent（Kotlin）两侧摘要逐字节一致：
// 用与 agent AssetManifestDigestTest 完全相同的向量与期望 hex（该常量由独立 SHA256 工具预算）。
// 两侧同实现文档算法即应同 hex；本测试防「两边各自绿却对不上」的跨语言漂移（首次真机上报前的静态兜底）。
func TestComputeManifestDigest_CrossLanguageVector(t *testing.T) {
	entries := []ManifestEntry{
		{Path: "plugins/Foo/config.yml", SHA256: "0000000000000000000000000000000000000000000000000000000000000001", Size: 10, MtimeMs: 1000},
		{Path: "server.properties", SHA256: "0000000000000000000000000000000000000000000000000000000000000002", Size: 20, MtimeMs: 2000},
	}
	const wantHex = "0ebe64f9e91c48c48daad121d10041aba4f40ca29a14a57b3477ed1a650f1516"
	if got := computeManifestDigest(entries); got != wantHex {
		t.Fatalf("跨语言摘要向量失配（Go 与 agent 摘要不一致）：got=%s want=%s", got, wantHex)
	}
	// 空清单 = sha256("")，与 agent 侧同常量。
	const emptyHex = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got := computeManifestDigest(nil); got != emptyHex {
		t.Fatalf("空清单摘要跨语言失配：got=%s want=%s", got, emptyHex)
	}
}

func TestComputeManifestDigest_OrderIndependentAndSensitive(t *testing.T) {
	h := "1111111111111111111111111111111111111111111111111111111111111111"
	a := ManifestEntry{Path: "plugins/a.yml", SHA256: h, Size: 10, MtimeMs: 100}
	b := ManifestEntry{Path: "plugins/b.yml", SHA256: h, Size: 20, MtimeMs: 200}
	// 输入顺序不影响摘要（内部按 path 排序）。
	d1 := computeManifestDigest([]ManifestEntry{a, b})
	d2 := computeManifestDigest([]ManifestEntry{b, a})
	if d1 != d2 {
		t.Fatalf("摘要应与输入顺序无关：%s != %s", d1, d2)
	}
	// isText 不参与摘要（算法只含 path/sha256/size/mtime）。
	aNoText := a
	aNoText.IsText = !a.IsText
	if computeManifestDigest([]ManifestEntry{aNoText, b}) != d1 {
		t.Fatalf("isText 不应改变摘要")
	}
	// 任一字段变化摘要即变。
	aBigger := a
	aBigger.Size = 11
	if computeManifestDigest([]ManifestEntry{aBigger, b}) == d1 {
		t.Fatalf("size 变化应改变摘要")
	}
}

func TestDeriveExt(t *testing.T) {
	cases := map[string]string{
		"plugins/Foo/config.yml":  "yml",
		"server.properties":       "properties",
		"plugins/Foo/data.tar.gz": "gz",
		"plugins/Foo/README":      "",
		".env":                    "", // dotfile 视作无扩展名
		"plugins/Foo/.gitignore":  "",
		"plugins/Foo/UPPER.YML":   "yml", // 小写化
	}
	for path, want := range cases {
		if got := deriveExt(path); got != want {
			t.Fatalf("deriveExt(%q)=%q want %q", path, got, want)
		}
	}
	// 超 16 字符扩展名截断到 16。
	if got := deriveExt("plugins/x." + longExt(30)); got != longExt(16) {
		t.Fatalf("超长扩展名应截断到 16，实际 %q", got)
	}
}

func longExt(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = 'a'
	}
	return string(s)
}

func TestValidAssetPathAndSHA256(t *testing.T) {
	valid := []string{"plugins/Foo/config.yml", "server.properties", "a/b/c"}
	for _, p := range valid {
		if !validAssetPath(p) {
			t.Fatalf("%q 应合法", p)
		}
	}
	invalid := []string{"", "/abs/path", "..", "a/../b", "a//b", `a\b`, "a/./b"}
	for _, p := range invalid {
		if validAssetPath(p) {
			t.Fatalf("%q 应非法", p)
		}
	}
	if !validSHA256Hex("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef") {
		t.Fatalf("合法 sha256 被拒")
	}
	for _, bad := range []string{"", "abc", "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef", "xyz0"} {
		if validSHA256Hex(bad) {
			t.Fatalf("非法 sha256 %q 被接受", bad)
		}
	}
}

func TestFullUploadStaging(t *testing.T) {
	clock := time.Unix(0, 0).UTC()
	st := newFullUploadStaging(5*time.Minute, func() time.Time { return clock })
	e := func(p string) ManifestEntry { return ManifestEntry{Path: p, SHA256: "x", Size: 1} }

	// seq0 未 eof → 暂存、未收齐。
	assembled, done, err := st.append("k", 0, false, []ManifestEntry{e("a")})
	if err != nil || done || assembled != nil {
		t.Fatalf("seq0 非 eof 应暂存未收齐，got done=%v err=%v", done, err)
	}
	// seq1 eof → 收齐两批。
	assembled, done, err = st.append("k", 1, true, []ManifestEntry{e("b")})
	if err != nil || !done || len(assembled) != 2 {
		t.Fatalf("seq1 eof 应收齐 2 条，got done=%v len=%d err=%v", done, len(assembled), err)
	}

	// 乱序（缺 seq0 直接 seq1）→ 失配。
	if _, _, err := st.append("k2", 1, true, []ManifestEntry{e("a")}); err != errStagingOutOfSync {
		t.Fatalf("乱序应返回 errStagingOutOfSync，实际 %v", err)
	}

	// 过期：seq0 后时钟前进超 TTL，再来 seq1 → 暂存已被清、失配。
	_, _, _ = st.append("k3", 0, false, []ManifestEntry{e("a")})
	clock = clock.Add(6 * time.Minute)
	if _, _, err := st.append("k3", 1, true, []ManifestEntry{e("b")}); err != errStagingOutOfSync {
		t.Fatalf("过期分片应失配，实际 %v", err)
	}

	// 单批 seq0 eof → 直接收齐。
	assembled, done, err = st.append("k4", 0, true, []ManifestEntry{e("a"), e("b"), e("c")})
	if err != nil || !done || len(assembled) != 3 {
		t.Fatalf("单批 eof 应直接收齐 3 条，got done=%v len=%d err=%v", done, len(assembled), err)
	}
}
