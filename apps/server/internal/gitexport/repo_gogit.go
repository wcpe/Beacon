package gitexport

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// GoGitRepoConfig 是 GoGitRepo 所需配置（从 config.GitExportConfig 映射，避免 gitexport 反依赖 internal/config）。
type GoGitRepoConfig struct {
	RepoPath     string // 本地 git 仓路径（非裸仓——快照是文件工作树，需 worktree 写文件）
	RemoteURL    string // 可选远程地址；空则只本地 commit 不推送
	RemoteBranch string // 远程推送分支
	AuthorName   string // commit 作者名（仅 git 身份元数据）
	AuthorEmail  string // commit 作者邮箱
	RemoteToken  string // 远程推送凭据（仅 env 注入，不入库）
}

// GoGitRepo 用 go-git（纯 Go）实现 GitRepo 端口：把快照全量覆盖工作区、提交、可选推送远程。
// 这是唯一 import go-git 的地方（副作用隔离在适配器，纯逻辑 snapshot.go/commit.go 不依赖任何 git 库，
// 与 ADR-0005 让 core 依赖 HttpTransport 端口、具体库只在适配器同构，见 ADR-0030 决策5）。
type GoGitRepo struct {
	cfg GoGitRepoConfig
}

// NewGoGitRepo 构造 go-git 实现。
func NewGoGitRepo(cfg GoGitRepoConfig) *GoGitRepo {
	return &GoGitRepo{cfg: cfg}
}

// Commit 全量覆盖工作区 → add → 有变更才 commit；配置了远程则 push。失败返回 error，调用方降级为 WARN。
func (r *GoGitRepo) Commit(snapshot Snapshot, message string) error {
	repo, err := r.openOrInit()
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("取 worktree 失败: %w", err)
	}
	if err := r.overwriteWorktree(snapshot); err != nil {
		return err
	}
	// git add -A 等效：把新增 / 修改 / 删除全部纳入暂存。
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return fmt.Errorf("git add 失败: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return fmt.Errorf("取 git status 失败: %w", err)
	}
	if status.IsClean() {
		return nil // 无变更：跳过空提交（每次全量快照，无差异即无需提交）
	}
	if _, err := wt.Commit(message, &git.CommitOptions{
		Author: &object.Signature{Name: r.authorName(), Email: r.authorEmail(), When: time.Now()},
	}); err != nil {
		return fmt.Errorf("git commit 失败: %w", err)
	}
	return r.push(repo)
}

// openOrInit 打开已存在的导出仓；不存在则建目录并 init（非裸仓）。
func (r *GoGitRepo) openOrInit() (*git.Repository, error) {
	repo, err := git.PlainOpen(r.cfg.RepoPath)
	if err == nil {
		return repo, nil
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		return nil, fmt.Errorf("打开导出仓失败: %w", err)
	}
	if err := os.MkdirAll(r.cfg.RepoPath, 0o755); err != nil {
		return nil, fmt.Errorf("建导出仓目录失败: %w", err)
	}
	repo, err = git.PlainInit(r.cfg.RepoPath, false)
	if err != nil {
		return nil, fmt.Errorf("初始化导出仓失败: %w", err)
	}
	return repo, nil
}

// overwriteWorktree 清空工作区（保留 .git）后写入快照全部文件——全量覆盖、天然自愈漂移（ADR-0030 决策3）。
// 快照路径由 BuildPath 清洗过（无 .. / 绝对前缀），不会逃出导出仓。
func (r *GoGitRepo) overwriteWorktree(snapshot Snapshot) error {
	entries, err := os.ReadDir(r.cfg.RepoPath)
	if err != nil {
		return fmt.Errorf("读导出仓目录失败: %w", err)
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue // 保留仓元数据
		}
		if err := os.RemoveAll(filepath.Join(r.cfg.RepoPath, e.Name())); err != nil {
			return fmt.Errorf("清空导出仓失败: %w", err)
		}
	}
	for relPath, content := range snapshot.Files {
		full := filepath.Join(r.cfg.RepoPath, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return fmt.Errorf("建导出子目录失败: %w", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			return fmt.Errorf("写导出文件失败: %w", err)
		}
	}
	return nil
}

// push 把当前 HEAD 强推到远程分支（单向镜像，force 覆盖远程）；未配远程即跳过。
func (r *GoGitRepo) push(repo *git.Repository) error {
	if r.cfg.RemoteURL == "" {
		return nil
	}
	if err := r.ensureRemote(repo); err != nil {
		return err
	}
	branch := r.cfg.RemoteBranch
	if branch == "" {
		branch = "master"
	}
	opts := &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec(fmt.Sprintf("+HEAD:refs/heads/%s", branch))},
		Force:      true, // 单向派生镜像：导出覆盖远程，不与远程协作
	}
	if r.cfg.RemoteToken != "" {
		opts.Auth = &githttp.BasicAuth{Username: "beacon", Password: r.cfg.RemoteToken}
	}
	if err := repo.Push(opts); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("git push 失败: %w", err)
	}
	return nil
}

// ensureRemote 确保 origin 指向配置的 URL（不符则重建）。
func (r *GoGitRepo) ensureRemote(repo *git.Repository) error {
	if rem, err := repo.Remote("origin"); err == nil {
		urls := rem.Config().URLs
		if len(urls) == 1 && urls[0] == r.cfg.RemoteURL {
			return nil
		}
		if err := repo.DeleteRemote("origin"); err != nil {
			return fmt.Errorf("删除旧远程失败: %w", err)
		}
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{r.cfg.RemoteURL}}); err != nil {
		return fmt.Errorf("配置远程失败: %w", err)
	}
	return nil
}

// authorName 返回 commit 作者名，未配则用默认。
func (r *GoGitRepo) authorName() string {
	if r.cfg.AuthorName != "" {
		return r.cfg.AuthorName
	}
	return "beacon-export"
}

// authorEmail 返回 commit 作者邮箱，未配则用默认。
func (r *GoGitRepo) authorEmail() string {
	if r.cfg.AuthorEmail != "" {
		return r.cfg.AuthorEmail
	}
	return "beacon-export@localhost"
}
