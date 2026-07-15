//go:build integration

package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/testsupport"
)

// TestFileAssetSearchLikeEscapeMySQL 真 MySQL：Search 的 pathPrefix / name LIKE 过滤语法可移植。
// 回归背景：曾用 `ESCAPE '\'`（Go 字面量 '\\'），MySQL 字符串字面量消费反斜杠致 Error 1064，
// sqlite 单测无感、仅真 MySQL 能逮住。同时验证 '#' 转义使 `_` 按字面匹配（转义函数与 ESCAPE 字符配套）。
func TestFileAssetSearchLikeEscapeMySQL(t *testing.T) {
	db := testsupport.OpenTestDB(t, "p8_asset")
	repo := NewFileAssetRepository(db)

	now := time.Now().UTC()
	rows := []model.FileAsset{
		{NamespaceID: 1, ServerID: 1, Path: "plugins/a_b/config.yml", Ext: "yml", SHA256: "aa", ScannedAt: now},
		{NamespaceID: 1, ServerID: 1, Path: "plugins/axb/config.yml", Ext: "yml", SHA256: "bb", ScannedAt: now},
		{NamespaceID: 1, ServerID: 2, Path: "mods/data.json", Ext: "json", SHA256: "cc", ScannedAt: now},
	}
	if err := repo.UpsertAssets(rows); err != nil {
		t.Fatalf("准备资产行失败: %v", err)
	}

	// pathPrefix 过滤（第一处 LIKE）：不得报语法错，且 `_` 按字面匹配、不误吞 axb
	got, total, err := repo.Search(AssetSearchQuery{NamespaceID: 1, PathPrefix: "plugins/a_b", Limit: 10})
	if err != nil {
		t.Fatalf("pathPrefix 搜索失败: %v", err)
	}
	if total != 1 || len(got) != 1 || got[0].Path != "plugins/a_b/config.yml" {
		t.Fatalf("pathPrefix 应字面匹配仅命中 a_b 目录，实际 total=%d rows=%+v", total, got)
	}

	// name 子串过滤（第二处 LIKE）
	got, total, err = repo.Search(AssetSearchQuery{NamespaceID: 1, Name: "config", Limit: 10})
	if err != nil {
		t.Fatalf("name 搜索失败: %v", err)
	}
	if total != 2 || len(got) != 2 {
		t.Fatalf("name=config 应命中 2 行，实际 total=%d rows=%d", total, len(got))
	}
}
