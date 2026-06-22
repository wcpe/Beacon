package gitexport

import (
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// countCommits 数某仓 HEAD 链上的提交数（无提交返回 0）。
func countCommits(t *testing.T, repoPath string) int {
	t.Helper()
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("打开仓失败: %v", err)
	}
	ref, err := repo.Head()
	if err != nil {
		return 0 // 尚无提交
	}
	iter, err := repo.Log(&git.LogOptions{From: ref.Hash()})
	if err != nil {
		t.Fatalf("取 log 失败: %v", err)
	}
	n := 0
	_ = iter.ForEach(func(*object.Commit) error { n++; return nil })
	return n
}

// headMessage 取某仓 HEAD 提交的 message。
func headMessage(t *testing.T, repoPath string) string {
	t.Helper()
	repo, _ := git.PlainOpen(repoPath)
	ref, err := repo.Head()
	if err != nil {
		t.Fatalf("取 HEAD 失败: %v", err)
	}
	c, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("取提交失败: %v", err)
	}
	return c.Message
}

// TestGoGitRepoCommitLocal 首次导出：init 仓、写文件、提交一次、message 正确。
func TestGoGitRepoCommitLocal(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "export")
	r := NewGoGitRepo(GoGitRepoConfig{RepoPath: repoPath, AuthorName: "t", AuthorEmail: "t@e"})
	snap := Snapshot{Files: map[string]string{
		"configs/prod/_global_/mysql.yml":            "url: x\n",
		"files/prod/area1/server/s1/Demo/config.yml": "a: 1\n",
	}}
	if err := r.Commit(snap, "导出 #1"); err != nil {
		t.Fatalf("commit 失败: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(repoPath, "configs/prod/_global_/mysql.yml"))
	if err != nil || string(b) != "url: x\n" {
		t.Fatalf("文件未正确写入：err=%v 内容=%q", err, string(b))
	}
	if n := countCommits(t, repoPath); n != 1 {
		t.Fatalf("应有 1 次提交，实际 %d", n)
	}
	if msg := headMessage(t, repoPath); msg != "导出 #1" {
		t.Fatalf("提交 message 错：%q", msg)
	}
}

// TestGoGitRepoFullOverwrite 二次导出全量覆盖：旧文件删除、新文件写入，两次提交。
func TestGoGitRepoFullOverwrite(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "export")
	r := NewGoGitRepo(GoGitRepoConfig{RepoPath: repoPath, AuthorName: "t", AuthorEmail: "t@e"})
	if err := r.Commit(Snapshot{Files: map[string]string{"configs/old.yml": "v1\n"}}, "导出 #1"); err != nil {
		t.Fatalf("首次 commit 失败: %v", err)
	}
	if err := r.Commit(Snapshot{Files: map[string]string{"configs/new.yml": "v2\n"}}, "导出 #2"); err != nil {
		t.Fatalf("二次 commit 失败: %v", err)
	}
	// 旧文件应已从工作区删除、新文件存在
	if _, err := os.Stat(filepath.Join(repoPath, "configs/old.yml")); !os.IsNotExist(err) {
		t.Fatalf("全量覆盖应删除旧文件 old.yml，实际仍在（err=%v）", err)
	}
	if _, err := os.Stat(filepath.Join(repoPath, "configs/new.yml")); err != nil {
		t.Fatalf("新文件 new.yml 应存在：%v", err)
	}
	if n := countCommits(t, repoPath); n != 2 {
		t.Fatalf("应有 2 次提交，实际 %d", n)
	}
}

// TestGoGitRepoNoChangeSkips 内容无变化时跳过空提交（同快照提交两次仅一次提交）。
func TestGoGitRepoNoChangeSkips(t *testing.T) {
	repoPath := filepath.Join(t.TempDir(), "export")
	r := NewGoGitRepo(GoGitRepoConfig{RepoPath: repoPath, AuthorName: "t", AuthorEmail: "t@e"})
	snap := Snapshot{Files: map[string]string{"configs/a.yml": "x\n"}}
	if err := r.Commit(snap, "导出 #1"); err != nil {
		t.Fatalf("首次 commit 失败: %v", err)
	}
	if err := r.Commit(snap, "导出 #2（无变化）"); err != nil {
		t.Fatalf("二次 commit（无变化）应成功 no-op：%v", err)
	}
	if n := countCommits(t, repoPath); n != 1 {
		t.Fatalf("无变化不应产生新提交，应仍为 1 次，实际 %d", n)
	}
}

// TestGoGitRepoRemoteConfigured 配了远程时：ensureRemote 把 origin 指向配置 URL、本地提交照常完成。
// 不在此断言「实际推送传输」——生产远程是 https（go-git 自身已测域），本地 file 传输在 Windows 测试环境不稳定；
// 而 push 失败的 best-effort 降级（不阻断发布、仅 WARN）已由 service 级 TestGitExportServiceBestEffortNeverBlocks 覆盖。
func TestGoGitRepoRemoteConfigured(t *testing.T) {
	dir := t.TempDir()
	remoteURL := filepath.ToSlash(filepath.Join(dir, "remote.git"))
	repoPath := filepath.Join(dir, "export")
	r := NewGoGitRepo(GoGitRepoConfig{
		RepoPath: repoPath, RemoteURL: remoteURL, RemoteBranch: "main",
		AuthorName: "t", AuthorEmail: "t@e",
	})
	// 配了远程仍会本地提交（push 在 commit 之后）；本地 file 远程不存在时 push 可能 best-effort 失败，不影响本断言。
	_ = r.Commit(Snapshot{Files: map[string]string{"configs/a.yml": "x\n"}}, "导出")
	if n := countCommits(t, repoPath); n != 1 {
		t.Fatalf("本地提交应已完成，实际 %d 次", n)
	}
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatalf("打开导出仓失败: %v", err)
	}
	rem, err := repo.Remote("origin")
	if err != nil || len(rem.Config().URLs) == 0 || rem.Config().URLs[0] != remoteURL {
		t.Fatalf("origin 应按配置指向 %q，实际 err=%v cfg=%+v", remoteURL, err, rem)
	}
}
