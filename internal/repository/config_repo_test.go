package repository

import (
	"testing"

	"github.com/wcpe/Beacon/internal/model"
)

// BumpGrayVersion 乐观锁：基准版本匹配才 +1 并命中；基准过期则不命中、版本不变（供调用方重读重试）。
// 这是并发灰度发布的 CAS 串行点，单测在 sqlite 上验证其判定逻辑（并发死锁消除由集成测试覆盖）。
func TestConfigRepo_BumpGrayVersionCAS(t *testing.T) {
	db := newConfigTestDB(t)
	repo := NewConfigItemRepository(db, testCipher(t))

	item := &model.ConfigItem{
		NamespaceCode: "prod", GroupCode: model.GlobalGroupCode, DataID: "app.yml",
		ScopeLevel: model.ScopeGlobal, ScopeTarget: "", Format: "yaml",
		Content: "v: 1\n", ContentMD5: "x", Version: 1, Enabled: true,
	}
	if err := repo.Create(item); err != nil {
		t.Fatalf("创建失败: %v", err)
	}

	// 基准 0 匹配：命中并自增到 1
	ok, err := repo.BumpGrayVersion(item.ID, 0)
	if err != nil {
		t.Fatalf("CAS 执行失败: %v", err)
	}
	if !ok {
		t.Fatal("基准匹配应命中")
	}
	got, _ := repo.FindByID(item.ID)
	if got.GrayVersion != 1 {
		t.Fatalf("命中后 gray_version 应为 1，实际 %d", got.GrayVersion)
	}

	// 基准过期（仍用 0，实际已是 1）：不命中、版本不变
	ok, err = repo.BumpGrayVersion(item.ID, 0)
	if err != nil {
		t.Fatalf("CAS 执行失败: %v", err)
	}
	if ok {
		t.Fatal("基准过期不应命中")
	}
	got, _ = repo.FindByID(item.ID)
	if got.GrayVersion != 1 {
		t.Fatalf("未命中 gray_version 应仍为 1，实际 %d", got.GrayVersion)
	}

	// 用新基准 1：再次命中、自增到 2
	ok, err = repo.BumpGrayVersion(item.ID, 1)
	if err != nil {
		t.Fatalf("CAS 执行失败: %v", err)
	}
	if !ok {
		t.Fatal("新基准应命中")
	}
	got, _ = repo.FindByID(item.ID)
	if got.GrayVersion != 2 {
		t.Fatalf("应自增到 2，实际 %d", got.GrayVersion)
	}
}
