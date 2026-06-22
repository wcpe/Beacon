package service

import (
	"context"
	"time"

	"github.com/wcpe/Beacon/internal/filetree"
	"github.com/wcpe/Beacon/internal/merge"
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

// EffectiveFileWithProvenance 是某文件的有效内容 + 逐文件/逐键来源（admin 只读预览用，FR-45）。
type EffectiveFileWithProvenance struct {
	Path      string
	MD5       string
	Content   string
	WholeFile bool                  // 是否整文件覆盖模式（非结构化 / 豁免 / 坏内容回退）
	Sources   []merge.KeyProvenance // 逐叶子键来源（整文件模式为单条空路径 = winner 层）
	Deletions []merge.KeyProvenance // 被减量删除且最终不存在的键（整文件模式恒空）
}

// ProvenancedFileTree 是某目标的 admin 只读有效文件树预览结果（含逐文件/逐键来源）。
type ProvenancedFileTree struct {
	Namespace   string
	ServerID    string
	Group       string
	Zone        string
	FileTreeMD5 string
	Files       []EffectiveFileWithProvenance
}

// ResolveWithProvenance 解析某目标的有效文件树并附逐文件/逐键来源（admin 只读预览，见 ADR-0013 模式扩展到 ADR-0029 文件树，FR-45）。
// serverID 非空时优先按 zone_assignment 解出 (group,zone)；未指派则用传入的 groupHint/zoneHint。
// 不挂长轮询、不强制注册（同 FR-22 的克制）；对同一解析出的 (group,zone)，每个 path 的合并内容/ md5 与 Resolve 一致
// （provenance 经 filetree 平行纯函数计算，不改 agent 下发热路径 Resolve）。
func (s *FileEffectiveService) ResolveWithProvenance(ns, serverID, groupHint, zoneHint string) (ProvenancedFileTree, error) {
	group, zone := groupHint, zoneHint
	if serverID != "" {
		assign, err := s.assignRepo.FindByServer(ns, serverID)
		if err != nil {
			return ProvenancedFileTree{}, err
		}
		if assign != nil {
			group, zone = assign.GroupCode, assign.ZoneCode
		}
	}

	candidates, err := s.fileRepo.FindEffectiveCandidates(ns, group, zone, serverID)
	if err != nil {
		return ProvenancedFileTree{}, err
	}
	resolved := filetree.ResolveWithProvenance(candidates)

	files := make([]EffectiveFileWithProvenance, 0, len(resolved))
	pathToMD5 := make(map[string]string, len(resolved))
	for _, f := range resolved {
		files = append(files, EffectiveFileWithProvenance{
			Path: f.Path, MD5: f.MD5, Content: f.Content, WholeFile: f.WholeFile,
			Sources: f.Sources, Deletions: f.Deletions,
		})
		pathToMD5[f.Path] = f.MD5
	}
	return ProvenancedFileTree{
		Namespace: ns, ServerID: serverID, Group: group, Zone: zone,
		FileTreeMD5: filetree.FileTreeMD5(pathToMD5),
		Files:       files,
	}, nil
}
