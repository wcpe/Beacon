package gitexport

import "log/slog"

// GitRepo 是「把快照写进 git 仓」的端口接口（依赖倒置，把副作用与纯逻辑解耦，见 ADR-0030 决策5）。
// 纯逻辑（snapshot.go / commit.go）不依赖任何 git 实现；具体实现（go-git，待依赖批准后接通）
// 是唯一 import 第三方 git 库的地方，可替换、不绑死（与 ADR-0005 让 core 依赖 HttpTransport 端口同构）。
type GitRepo interface {
	// Commit 用 snapshot 全量覆盖工作区、提交一次；若配置了远程则推送。
	// 失败返回 error——调用方（GitExportService）据此降级为 WARN，绝不阻断发布主流程。
	Commit(snapshot Snapshot, message string) error
}

// NopGitRepo 是未启用导出（enabled=false 或无实现注入）时的空实现：
// 不做任何事、永不报错（与 ChangeNotifier / PublishRecorder「未注入即 no-op」同构）。
type NopGitRepo struct{}

// Commit 空实现：仅在 DEBUG 留痕，不写任何文件、不报错。
func (NopGitRepo) Commit(_ Snapshot, _ string) error {
	slog.Debug("git 导出未启用，跳过 commit（NopGitRepo）")
	return nil
}
