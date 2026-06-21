package service

import (
	"testing"

	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// TestCreatePersistsWholeFileOverride FR-44：Create 透传并持久化 WholeFileOverride 标记，
// 且 filetree 解析据此对结构化文件整文件覆盖（不深合并、保注释）。闭合服务层唯一接线点。
func TestCreatePersistsWholeFileOverride(t *testing.T) {
	db := newCommandSvcTestDB(t)
	fileRepo := repository.NewFileObjectRepository(db)
	svc := NewFileService(db, fileRepo, repository.NewFileRevisionRepository(db), repository.NewAuditLogRepository(db))

	// 全局层：普通结构化文件（默认深合并）
	if _, err := svc.Create(CreateFileParams{
		Namespace: "prod", Group: "area1", Path: "Demo/config.yml",
		ScopeLevel: model.ScopeGlobal, Content: "a: 0\nb: 9\n", Operator: "alice",
	}); err != nil {
		t.Fatalf("全局层 Create 应成功：%v", err)
	}
	// 单服层：标豁免（强制整文件覆盖、保注释）
	winner := "# 注释保留\na: 1\n"
	if _, err := svc.Create(CreateFileParams{
		Namespace: "prod", Group: "area1", Path: "Demo/config.yml",
		ScopeLevel: model.ScopeServer, ScopeTarget: "lobby-1",
		Content: winner, Operator: "alice", WholeFileOverride: true,
	}); err != nil {
		t.Fatalf("单服层 Create 应成功：%v", err)
	}

	// 持久化校验：单服层行 WholeFileOverride=true
	obj, err := fileRepo.FindByIdentity("prod", "area1", "Demo/config.yml", model.ScopeServer, "lobby-1")
	if err != nil || obj == nil {
		t.Fatalf("应查到单服层文件对象，err=%v obj=%v", err, obj)
	}
	if !obj.WholeFileOverride {
		t.Fatal("WholeFileOverride 未持久化")
	}

	// 解析校验：winner 标豁免 → 整文件取原文（含注释、不被 global 深合并掉）
	candidates, err := fileRepo.FindEffectiveCandidates("prod", "area1", "", "lobby-1")
	if err != nil {
		t.Fatalf("拉候选失败：%v", err)
	}
	files := filetree.Resolve(candidates)
	if len(files) != 1 || files[0].Content != winner {
		t.Fatalf("豁免文件应整文件取单服原文（含注释），实际 %+v", files)
	}
}
