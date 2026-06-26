package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
)

// TestRequestBrowseHappy 触发浏览 → 建 pending fs-browse 命令 + file.browse 审计；
// 另一 goroutine 模拟 agent 拉取 + 回传结果 → admin 取到结果 JSON、命令 done。
func TestRequestBrowseHappy(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	hub := longpoll.NewHub()
	svc.SetBrowseResultHub(hub)
	cmdRepo := repository.NewAgentCommandRepository(db)

	const wantResult = `{"path":"AllinCore","entries":[{"name":"config.yml","dir":false}]}`

	// 模拟 agent：等命令建好（pending）→ 拉取（CAS fetched）→ 回传结果。
	go func() {
		// 轮询等到命令出现并被建为 pending，再走 Fetch → ReceiveBrowseResult。
		for i := 0; i < 200; i++ {
			cmd, _ := cmdRepo.FindOldestPending("prod", "lobby-1")
			if cmd != nil {
				fetched, _ := svc.FetchPending("prod", "lobby-1")
				_ = svc.ReceiveBrowseResult("prod", "lobby-1", fetched.ID, true, wantResult, "")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	got, err := svc.RequestBrowse(context.Background(), BrowseParams{
		Namespace: "prod", ServerID: "lobby-1", Op: model.BrowseOpList,
		Path: "AllinCore", Operator: "alice", ClientIP: "10.0.0.1",
	})
	if err != nil {
		t.Fatalf("浏览应成功: %v", err)
	}
	if got != wantResult {
		t.Fatalf("应取到 agent 回传结果，实际 %q", got)
	}
	// 命令应为 done
	cmds := allCommands(t, db)
	if len(cmds) != 1 || cmds[0].Status != model.CommandStatusDone || cmds[0].Type != model.CommandTypeFsBrowse {
		t.Fatalf("命令应为 done/fs-browse，实际 %+v", cmds)
	}
	// 载荷应含 op/path
	var pl browsePayload
	if json.Unmarshal([]byte(cmds[0].Payload), &pl) != nil || pl.Op != model.BrowseOpList || pl.Path != "AllinCore" {
		t.Fatalf("载荷应含 op=list/path=AllinCore，实际 %q", cmds[0].Payload)
	}
	// 记一条 file.browse 审计（detail 不含文件内容，仅 commandId/op/path）
	if countAudit(t, db, model.ActionFileBrowse) != 1 {
		t.Fatal("应记一条 file.browse 审计")
	}
	if auditDetailContains(t, db, model.ActionFileBrowse, "config.yml") {
		t.Fatal("审计 detail 绝不应含文件内容 / 结果")
	}
}

// TestRequestBrowseFailedTarget agent 回传 ok=false（越权 / 非目录 / 非文本）→ 命令 failed、admin 得 404。
func TestRequestBrowseFailedTarget(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	hub := longpoll.NewHub()
	svc.SetBrowseResultHub(hub)
	cmdRepo := repository.NewAgentCommandRepository(db)

	go func() {
		for i := 0; i < 200; i++ {
			if cmd, _ := cmdRepo.FindOldestPending("prod", "lobby-1"); cmd != nil {
				fetched, _ := svc.FetchPending("prod", "lobby-1")
				_ = svc.ReceiveBrowseResult("prod", "lobby-1", fetched.ID, false, "", "路径越权")
				return
			}
			time.Sleep(2 * time.Millisecond)
		}
	}()

	_, err := svc.RequestBrowse(context.Background(), BrowseParams{
		Namespace: "prod", ServerID: "lobby-1", Op: model.BrowseOpFile,
		Path: "../etc/passwd", Operator: "alice",
	})
	if err != apperr.ErrBrowseTargetNotFound {
		t.Fatalf("agent 拒读应 ErrBrowseTargetNotFound，实际 %v", err)
	}
	cmds := allCommands(t, db)
	if len(cmds) != 1 || cmds[0].Status != model.CommandStatusFailed {
		t.Fatalf("命令应 failed，实际 %+v", cmds)
	}
}

// TestRequestBrowseTimeout agent 不回传（离线）→ admin 等到超时 504。
func TestRequestBrowseTimeout(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	svc.SetBrowseResultHub(longpoll.NewHub())

	// 用很短的 ctx deadline 逼出超时（避免真等 browseWaitTimeout）。
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	_, err := svc.RequestBrowse(ctx, BrowseParams{
		Namespace: "prod", ServerID: "lobby-1", Op: model.BrowseOpList, Operator: "alice",
	})
	if err != apperr.ErrBrowseTimeout {
		t.Fatalf("无回传应超时 ErrBrowseTimeout，实际 %v", err)
	}
	// 命令仍 pending（未被拉取 / 回传），留待过期清理。
	cmds := allCommands(t, db)
	if len(cmds) != 1 || cmds[0].Status != model.CommandStatusPending {
		t.Fatalf("超时命令应仍 pending，实际 %+v", cmds)
	}
}

// TestRequestBrowseGuards 缺参 / 非法 op / 未装配 hub 一律拒（不建命令）。
func TestRequestBrowseGuards(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	svc.SetBrowseResultHub(longpoll.NewHub())
	ctx := context.Background()

	if _, err := svc.RequestBrowse(ctx, BrowseParams{Namespace: "prod", ServerID: "", Op: model.BrowseOpList, Operator: "a"}); err != apperr.ErrInvalidParam {
		t.Fatalf("缺 serverId 应 ErrInvalidParam，实际 %v", err)
	}
	if _, err := svc.RequestBrowse(ctx, BrowseParams{Namespace: "prod", ServerID: "s", Op: "evil", Operator: "a"}); err != apperr.ErrInvalidParam {
		t.Fatalf("非法 op 应 ErrInvalidParam，实际 %v", err)
	}
	// 未装配 hub
	svc2 := newCommandSvc(db)
	if _, err := svc2.RequestBrowse(ctx, BrowseParams{Namespace: "prod", ServerID: "s", Op: model.BrowseOpList, Operator: "a"}); err != apperr.ErrInternal {
		t.Fatalf("未装配 hub 应 ErrInternal，实际 %v", err)
	}
	// 不应建任何命令
	if cmds := allCommands(t, db); len(cmds) != 0 {
		t.Fatalf("守卫拒后不应建命令，实际 %d 条", len(cmds))
	}
}

// TestReceiveBrowseResultGuards 回传守卫：不存在 / 类型不符 / 非 fetched 一律拒。
func TestReceiveBrowseResultGuards(t *testing.T) {
	db := newCommandSvcTestDB(t)
	svc := newCommandSvc(db)
	svc.SetBrowseResultHub(longpoll.NewHub())

	// 不存在的命令
	if err := svc.ReceiveBrowseResult("prod", "s", 99999, true, "{}", ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("不存在命令应 ErrCommandNotFound，实际 %v", err)
	}
	// 非 fs-browse 类型（ingest-plugins）回传 → 类型不符拒
	_, _ = svc.RequestReverseFetch("prod", "lobby-1", model.ScopeGroup, "g", "", "alice", "")
	ingest, _ := svc.FetchPending("prod", "lobby-1")
	if err := svc.ReceiveBrowseResult("prod", "lobby-1", ingest.ID, true, "{}", ""); err != apperr.ErrCommandNotFound {
		t.Fatalf("非 fs-browse 类型回传应被拒，实际 %v", err)
	}
}

// allCommands 取全部命令行（含瞬态字段，仅测试断言用）。
func allCommands(t *testing.T, db *gorm.DB) []model.AgentCommand {
	t.Helper()
	var cmds []model.AgentCommand
	if err := db.Order("id asc").Find(&cmds).Error; err != nil {
		t.Fatalf("查命令失败: %v", err)
	}
	return cmds
}

// auditDetailContains 判断某 action 的审计 detail 是否含某子串（用于断言敏感内容不入 detail）。
func auditDetailContains(t *testing.T, db *gorm.DB, action, sub string) bool {
	t.Helper()
	var logs []model.AuditLog
	if err := db.Where("action = ?", action).Find(&logs).Error; err != nil {
		t.Fatalf("查审计失败: %v", err)
	}
	for _, l := range logs {
		if strings.Contains(l.Detail, sub) {
			return true
		}
	}
	return false
}
