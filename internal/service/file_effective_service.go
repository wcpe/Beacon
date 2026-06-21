package service

import (
	"context"
	"time"

	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/repository"
	"github.com/wcpe/Beacon/internal/runtime/longpoll"
)

// FileTree 是某 agent 身份的有效文件树解析结果（整文件覆盖后的 manifest + 独立 fileTreeMd5）。
type FileTree struct {
	Namespace string
	ServerID  string
	Group     string
	Zone      string
	// 整棵文件树指纹（与配置 md5 相互独立，见 ADR-0010）
	FileTreeMD5 string
	Files       []filetree.EffectiveFile
}

// Manifest 返回 path→md5 清单（不含内容，agent 比对增量同步用）。
func (t FileTree) Manifest() map[string]string {
	return filetree.Manifest(t.Files)
}

// FileEffectiveService 按 agent 身份解析有效文件树（scope 整文件覆盖）+ 文件长轮询挂起。
// 持有独立于配置长轮询的 Hub，文件发布只唤醒文件 waiter，互不触发无谓重算。
type FileEffectiveService struct {
	fileRepo   *repository.FileObjectRepository
	assignRepo *repository.ZoneAssignmentRepository
	hub        *longpoll.Hub
}

// NewFileEffectiveService 构造服务。hub 仅长轮询用，纯解析场景可传 nil。
func NewFileEffectiveService(fileRepo *repository.FileObjectRepository, assignRepo *repository.ZoneAssignmentRepository, hub *longpoll.Hub) *FileEffectiveService {
	return &FileEffectiveService{fileRepo: fileRepo, assignRepo: assignRepo, hub: hub}
}

// Resolve 解析某 (namespace, serverId) 的有效文件树：
// 先按 zone_assignment 得 (group, zone)，未分配则 group=groupHint、zone 为空；再拉四层候选整文件覆盖。
func (s *FileEffectiveService) Resolve(ns, serverID, groupHint string) (FileTree, error) {
	group, zone := groupHint, ""
	assign, err := s.assignRepo.FindByServer(ns, serverID)
	if err != nil {
		return FileTree{}, err
	}
	if assign != nil {
		group, zone = assign.GroupCode, assign.ZoneCode
	}
	return s.resolveLayers(ns, serverID, group, zone)
}

// WaitFileTree 文件长轮询：先注册 waiter 再算 fileTreeMd5（消除注册前发布丢唤醒窗口）。
// md5 与 agentMD5 不同 → 立即返回 (tree, true)；相同 → 挂起，被唤醒后重算比对；超时/断连返回 (_, false)。
func (s *FileEffectiveService) WaitFileTree(ctx context.Context, ns, serverID, groupHint, agentMD5 string, timeout time.Duration) (FileTree, bool, error) {
	w := s.hub.Register(ns, serverID)
	defer s.hub.Deregister(w)
	deadline := time.Now().Add(timeout)
	for {
		tree, err := s.Resolve(ns, serverID, groupHint)
		if err != nil {
			return FileTree{}, false, err
		}
		if tree.FileTreeMD5 != agentMD5 {
			return tree, true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return FileTree{}, false, nil
		}
		if !w.Wait(ctx, remaining) {
			return FileTree{}, false, nil // 超时或客户端断连
		}
		// 被唤醒 → 循环重跑 Resolve 比对（唤醒即重算）
	}
}

// resolveLayers 拉四层候选、按 path 整文件覆盖、算 manifest 与 fileTreeMd5。
func (s *FileEffectiveService) resolveLayers(ns, serverID, group, zone string) (FileTree, error) {
	candidates, err := s.fileRepo.FindEffectiveCandidates(ns, group, zone, serverID)
	if err != nil {
		return FileTree{}, err
	}
	files := filetree.Resolve(candidates)
	return FileTree{
		Namespace: ns, ServerID: serverID, Group: group, Zone: zone,
		FileTreeMD5: filetree.FileTreeMD5(filetree.Manifest(files)),
		Files:       files,
	}, nil
}
