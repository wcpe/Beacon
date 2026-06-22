package service

import (
	"log/slog"

	"github.com/wcpe/Beacon/internal/gitexport"
)

// GitExporter 是发布服务触发 git 导出的窄接口（由 *GitExportService 实现，可选注入；
// 未注入即不导出，与 PublishRecorder / ChangeNotifier「未注入即 no-op」同构）。
// ExportAsync 必须非阻塞——发布服务在事务提交后、与 notify 并列调用它，绝不等 git IO。
type GitExporter interface {
	ExportAsync(meta gitexport.ExportMeta)
}

// exportSourceLoader 是导出源层读取的窄接口（由 repository.ExportSourceRepository 实现）。
// 抽成接口便于单测注入桩，不绑死 GORM。
type exportSourceLoader interface {
	LoadSourceLayers() ([]gitexport.SourceLayer, error)
}

// GitExportService 编排 git 单向导出镜像（FR-47，见 ADR-0030）：
// 发布 / 回滚 / 改派事务提交后被触发，**异步、串行、best-effort** 地把源层快照 commit 到 git 仓。
//
// 不变量（守 ADR-0030）：
//   - 单向派生、不作第二真源：只读 MySQL 源层、单向写 git，绝不回灌、不参与下发。
//   - 非阻塞：ExportAsync 只投递信号立即返回，读源 + 渲染 + commit 在单 worker goroutine 跑。
//   - best-effort：任一步失败仅 WARN，绝不回滚发布、绝不阻断主流程。
//   - 串行单写：git 工作区是单写资源，请求经容量受限 channel 串行化；满则丢弃新信号
//     （每次导出都是全量快照，丢信号不丢数据——下一次导出仍取最新全量）。
type GitExportService struct {
	source exportSourceLoader
	repo   gitexport.GitRepo
	// 触发信号队列：缓冲 1，满即丢（合并到下一次全量导出）。
	signals chan gitexport.ExportMeta
}

// NewGitExportService 构造导出服务。repo 传 gitexport.NopGitRepo{} 即等于"未启用导出"（no-op）。
func NewGitExportService(source exportSourceLoader, repo gitexport.GitRepo) *GitExportService {
	return &GitExportService{
		source:  source,
		repo:    repo,
		signals: make(chan gitexport.ExportMeta, 1),
	}
}

// Run 启动单 worker goroutine 串行消费导出信号，直到 stop 关闭。
// 在 cmd/beacon 装配后 go s.Run(stop) 调用；stop 关闭即退出（随进程关停）。
func (s *GitExportService) Run(stop <-chan struct{}) {
	slog.Info("git 导出 worker 已启动")
	for {
		select {
		case <-stop:
			slog.Info("git 导出 worker 收到关停信号，退出")
			return
		case meta := <-s.signals:
			s.exportOnce(meta)
		}
	}
}

// ExportAsync 投递一次导出信号（发布 / 回滚 / 改派提交后调用）。
// **非阻塞**：channel 满（上一次导出尚在跑）即丢弃本信号——下一次导出仍取最新全量快照，不丢数据。
// 绝不在调用方（发布请求线程）做任何 git / DB IO。
func (s *GitExportService) ExportAsync(meta gitexport.ExportMeta) {
	select {
	case s.signals <- meta:
		// 已入队
	default:
		// 队列满：合并到下一次全量导出，仅 DEBUG 留痕（不是错误）
		slog.Debug("git 导出信号队列忙，合并到下一次全量导出", "动作", meta.Action)
	}
}

// exportOnce 执行一次导出：读全量源层 → 组装快照 → commit。任一步失败仅 WARN、不抛、不阻断。
func (s *GitExportService) exportOnce(meta gitexport.ExportMeta) {
	layers, err := s.source.LoadSourceLayers()
	if err != nil {
		slog.Warn("git 导出读取源层失败，跳过本次导出（不影响发布）", "错误", err)
		return
	}
	snapshot := gitexport.BuildSnapshot(layers)
	message := gitexport.BuildCommitMessage(meta)
	if err := s.repo.Commit(snapshot, message); err != nil {
		slog.Warn("git 导出 commit 失败，跳过本次导出（不影响发布）", "动作", meta.Action, "错误", err)
		return
	}
	slog.Info("git 导出完成", "动作", meta.Action, "文件数", len(snapshot.Files))
}
