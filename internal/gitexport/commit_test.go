package gitexport

import (
	"strings"
	"testing"

	"github.com/wcpe/Beacon/internal/model"
)

// TestBuildCommitMessageFull FR-47：commit message 含操作者 / 动作 / 对象 / 版本，可 git log 追溯。
func TestBuildCommitMessageFull(t *testing.T) {
	msg := BuildCommitMessage(ExportMeta{
		Operator: "admin",
		Action:   model.ActionConfigPublish,
		Target:   "prod/area1/mysql.yml@server:lobby-1",
		Version:  7,
	})
	// 首行摘要含动作与对象
	subject := strings.SplitN(msg, "\n", 2)[0]
	if !strings.Contains(subject, model.ActionConfigPublish) || !strings.Contains(subject, "mysql.yml") {
		t.Fatalf("摘要应含动作与对象，实际 %q", subject)
	}
	for _, want := range []string{"操作者: admin", "动作: config.publish", "对象: prod/area1/mysql.yml@server:lobby-1", "版本: 7"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("commit message 应含 %q，实际：\n%s", want, msg)
		}
	}
}

// TestBuildCommitMessageOmitsVersionZero FR-47：版本为 0（如改派）时不输出版本行。
func TestBuildCommitMessageOmitsVersionZero(t *testing.T) {
	msg := BuildCommitMessage(ExportMeta{
		Operator: "admin",
		Action:   model.ActionZoneMove,
		Target:   "prod/lobby-1",
		Version:  0,
	})
	if strings.Contains(msg, "版本:") {
		t.Fatalf("版本为 0 不应输出版本行，实际：\n%s", msg)
	}
	if !strings.Contains(msg, "动作: zone.move") {
		t.Fatalf("应含动作行，实际：\n%s", msg)
	}
}

// TestBuildCommitMessageEmptyActionFallback FR-47：动作缺失时给兜底标识、不产空摘要。
func TestBuildCommitMessageEmptyActionFallback(t *testing.T) {
	msg := BuildCommitMessage(ExportMeta{})
	subject := strings.SplitN(msg, "\n", 2)[0]
	if strings.TrimSpace(subject) == "" || strings.HasSuffix(strings.TrimSpace(subject), ":") {
		t.Fatalf("空元数据不应产生空摘要，实际 %q", subject)
	}
}

// TestNopGitRepoNeverErrors FR-47：未启用导出的空实现永不报错、不阻断。
func TestNopGitRepoNeverErrors(t *testing.T) {
	if err := (NopGitRepo{}).Commit(BuildSnapshot(nil), "x"); err != nil {
		t.Fatalf("NopGitRepo.Commit 不应报错，实际 %v", err)
	}
}
