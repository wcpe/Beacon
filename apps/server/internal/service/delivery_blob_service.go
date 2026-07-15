package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// defaultDeliveryBlobRoot 是交付中转存储默认根目录（相对 CWD，仿 FileSync .beacon/file-sync-blobs 惯例）。
// 布局：<root>/blobs/<sha256 前 2 位>/<sha256> + <root>/tmp/（上传临时文件，成功后原子 rename 进 blobs）。
const defaultDeliveryBlobRoot = ".beacon/delivery"

// deliveryBlobSettings 是数据面对运维设置的窄读依赖（并发 / 容量 / 保留期热读，由 SettingsService 实现）。
type deliveryBlobSettings interface {
	GetInt(key string) int
}

// deliveryProgressWaker 是编排推进器唤醒的窄依赖（由 DeliveryOrchestrator 实现，M3 可选注入）：
// agent 阶段回执落定后即时唤醒推进器重扫，由推进器单一驱动源推进 change_target / 批次（避免回执并发改状态）。
type deliveryProgressWaker interface {
	WakeOrder(orderID uint)
}

// flowLimiter 是热读上限的进程内计数信号量：每次 tryAcquire 按当前设置值判定，改设置即热生效。
// 满则拒绝（由调用方回 429 让 agent 稍后重试），不排队挂起——与既有采集面背压（ingest busy）口径一致。
type flowLimiter struct {
	mu       sync.Mutex
	inflight int
}

// tryAcquire 尝试占一个并发额度：当前在途数已达 limit 返回 false。
func (l *flowLimiter) tryAcquire(limit int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inflight >= limit {
		return false
	}
	l.inflight++
	return true
}

// release 归还一个并发额度。
func (l *flowLimiter) release() {
	l.mu.Lock()
	l.inflight--
	l.mu.Unlock()
}

// DeliveryBlobService 是交付数据面控制面核心（FR-165，见 ADR-0069）：全局 sha256 内容寻址 blob 中转存储。
// 职责：流式收发（TeeReader 边收边算哈希 + 临时文件 + 原子 rename）、全局去重、并发限流与容量预检、
// blob 归属校验、agent 面清单 / 回执（见 delivery_blob_agent.go）。编排推进（payload_state / 目标状态机）归 M3。
type DeliveryBlobService struct {
	db        *gorm.DB
	blobs     *repository.DeliveryBlobRepository
	orders    *repository.ChangeOrderRepository
	artifacts *repository.DeliveryConfigArtifactRepository
	cmdRepo   *repository.AgentCommandRepository
	settings  deliveryBlobSettings
	root      string
	uploads   *flowLimiter
	downloads *flowLimiter
	// waker 编排推进器唤醒器（M3 可选注入；未注入则回执后不主动唤醒，靠推进器 ticker 兜底）。
	waker deliveryProgressWaker
	// configRenderer 配置灰度渲染器（可选注入，ADR-0071）：把 config_change 项按目标渲染为生效明文文件。
	// 未注入（M2 数据面路径）则配置项不参与 blob 准备与清单下发。
	configRenderer *deliveryConfigRenderer
}

// NewDeliveryBlobService 构造服务（存储根默认 .beacon/delivery，可 SetRoot 覆盖）。
func NewDeliveryBlobService(db *gorm.DB, blobs *repository.DeliveryBlobRepository,
	orders *repository.ChangeOrderRepository, cmdRepo *repository.AgentCommandRepository,
	settings deliveryBlobSettings) *DeliveryBlobService {
	return &DeliveryBlobService{
		db: db, blobs: blobs, orders: orders,
		artifacts: repository.NewDeliveryConfigArtifactRepository(db),
		cmdRepo:   cmdRepo, settings: settings,
		root: filepath.Clean(defaultDeliveryBlobRoot), uploads: &flowLimiter{}, downloads: &flowLimiter{},
	}
}

// SetProgressWaker 注入编排推进器唤醒器（M3 启动时装配；未注入则回执后不主动唤醒）。
func (s *DeliveryBlobService) SetProgressWaker(w deliveryProgressWaker) { s.waker = w }

// SetConfigRenderer 注入配置灰度渲染器（配置灰度装配时调用，ADR-0071）：内部据配置中心 + 版本 / 文件仓库构造。
func (s *DeliveryBlobService) SetConfigRenderer(config *ConfigCenterService,
	versions *repository.ConfigLayerVersionRepository, files *repository.ConfigFileRepository) {
	s.configRenderer = newDeliveryConfigRenderer(config, versions, files)
}

// SetRoot 覆盖中转存储根目录（主要供测试隔离；空串忽略）。
func (s *DeliveryBlobService) SetRoot(root string) {
	if strings.TrimSpace(root) == "" {
		return
	}
	s.root = filepath.Clean(root)
}

// blobPath 返回某 sha 的最终落盘路径：<root>/blobs/<前 2 位>/<sha256>。
func (s *DeliveryBlobService) blobPath(sha string) string {
	return filepath.Join(s.root, "blobs", sha[:2], sha)
}

// tmpDir 返回上传临时目录：<root>/tmp。
func (s *DeliveryBlobService) tmpDir() string {
	return filepath.Join(s.root, "tmp")
}

// normalizeBlobSHA 归一并校验 sha256 hex（64 位小写），非法返回参数错误。
func normalizeBlobSHA(sha string) (string, error) {
	lower := strings.ToLower(strings.TrimSpace(sha))
	if !isSHA256Hex(lower) {
		return "", apperr.ErrInvalidParam
	}
	return lower, nil
}

// deliveryBlobCapacityExceeded 构造带实测数字的容量拒绝错误（明确报错不静默，ADR-0057/ADR-0069）。
func deliveryBlobCapacityExceeded(used, incoming, capacity int64) *apperr.Error {
	return apperr.New(http.StatusInsufficientStorage, "blob_capacity_exceeded",
		fmt.Sprintf("中转存储容量不足：已用 %d + 本次 %d 字节超出上限 %d 字节，请清理或调大 delivery.blob-capacity-bytes", used, incoming, capacity))
}

// Store 流式写入一个 blob（PUT 语义，agent 上传，spec §4.5.2）：受上传并发限流保护，核心写入委托 persistBlob。
func (s *DeliveryBlobService) Store(sha string, contentLength int64, reader io.Reader) error {
	if !s.uploads.tryAcquire(s.settings.GetInt(SettingDeliveryUploadConcurrency)) {
		return apperr.ErrDeliveryUploadBusy
	}
	defer s.uploads.release()
	return s.persistBlob(sha, contentLength, reader)
}

// StoreInternal 控制面内部写入一个 blob（配置灰度渲染明文落盘，ADR-0071）：不占 agent 上传并发额度
// （非外部上传、不是限流针对的背压面），其余（秒传去重 / 容量预检 / 边收边校验 sha / 原子落位）与 Store 一致。
func (s *DeliveryBlobService) StoreInternal(sha string, contentLength int64, reader io.Reader) error {
	return s.persistBlob(sha, contentLength, reader)
}

// persistBlob 内容寻址写入核心（Store / StoreInternal 共用）：归一校验 sha → 秒传去重 → 容量预检 →
// 占位落账 → 临时文件边收边算 sha256 → 与声明比对（不符 422 丢弃）→ 原子 rename 进 blobs → 元数据置 ready。
func (s *DeliveryBlobService) persistBlob(sha string, contentLength int64, reader io.Reader) error {
	sha, err := normalizeBlobSHA(sha)
	if err != nil {
		return err
	}
	if contentLength < 0 {
		return apperr.ErrDeliveryLengthRequired
	}
	if ready, e := s.isReady(sha); e != nil {
		return e
	} else if ready {
		return nil // 同 sha256 已就绪：幂等成功（秒传），不再读体
	}
	if err := s.precheckCapacity(sha, contentLength); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := s.blobs.UpsertUploading(sha, contentLength, now); err != nil {
		return err
	}
	size, err := s.receiveBlobFile(sha, reader)
	if err != nil {
		return err
	}
	return s.blobs.MarkReady(sha, size, time.Now().UTC())
}

// isReady 判断 blob 是否已就绪（元数据 ready 且磁盘文件在场；二者缺一按未就绪处理，可重传自愈）。
func (s *DeliveryBlobService) isReady(sha string) (bool, error) {
	blob, err := s.blobs.FindBySHA256(sha)
	if err != nil {
		return false, err
	}
	if blob == nil || blob.State != model.DeliveryBlobStateReady {
		return false, nil
	}
	if _, statErr := os.Stat(s.blobPath(sha)); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return false, nil // 元数据标 ready 但磁盘文件缺失：视同不就绪，让上传重来
		}
		return false, statErr // 其他 stat 错误（权限 / IO）上报，不静默吞
	}
	return true, nil
}

// precheckCapacity 容量预检：现存总量（含 uploading 占位声明值、不含本 sha 自身）+ 本次声明超上限即拒绝。
// 预检与占位之间存在极小并发窗口可能轻微超限，属可接受的保守近似（上限本身即软性护栏）。
func (s *DeliveryBlobService) precheckCapacity(sha string, incoming int64) error {
	capacity := int64(s.settings.GetInt(SettingDeliveryBlobCapacityBytes))
	used, err := s.blobs.SumBytesExcluding(sha)
	if err != nil {
		return err
	}
	if used+incoming > capacity {
		return deliveryBlobCapacityExceeded(used, incoming, capacity)
	}
	return nil
}

// receiveBlobFile 把请求体写入临时文件并校验 sha256，一致则原子 rename 进 blobs 目录，返回实收字节数。
// 复用 FileSync writeBlobFile 范式（TeeReader + CreateTemp + rename），并叠加全局去重的并发容错：
// rename 时目标已被并发上传者落位 → 丢弃临时文件按成功处理（内容寻址下同 sha 内容必然一致）。
func (s *DeliveryBlobService) receiveBlobFile(sha string, reader io.Reader) (int64, error) {
	if err := os.MkdirAll(s.tmpDir(), 0o755); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(s.tmpDir(), sha+".tmp-*")
	if err != nil {
		return 0, err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	sum := sha256.New()
	size, err := io.Copy(tmp, io.TeeReader(reader, sum))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	if hex.EncodeToString(sum.Sum(nil)) != sha {
		return 0, apperr.ErrDeliveryBlobHashMismatch // 实算哈希与声明不符：丢弃临时文件（defer 清理），占位行由清理器回收
	}
	return size, s.placeBlobFile(tmpName, sha)
}

// placeBlobFile 把校验通过的临时文件原子 rename 到最终路径；目标已存在（并发去重命中）按成功处理。
func (s *DeliveryBlobService) placeBlobFile(tmpName, sha string) error {
	target := s.blobPath(sha)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return nil
	}
	if err := os.Rename(tmpName, target); err != nil {
		// Windows 下 rename 不覆盖已存在目标：并发上传者刚落位即视为去重成功。
		if _, statErr := os.Stat(target); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// Head 查询 blob 存在性 / 就绪度（HEAD 语义，spec §5.3）：就绪返回字节数，未就绪 / 不存在返回 blob_not_found。
func (s *DeliveryBlobService) Head(sha string) (int64, error) {
	sha, err := normalizeBlobSHA(sha)
	if err != nil {
		return 0, err
	}
	blob, err := s.blobs.FindBySHA256(sha)
	if err != nil {
		return 0, err
	}
	if blob == nil || blob.State != model.DeliveryBlobStateReady {
		return 0, apperr.ErrDeliveryBlobNotFound
	}
	if _, statErr := os.Stat(s.blobPath(sha)); statErr != nil {
		slog.Warn("交付 blob 元数据就绪但磁盘文件缺失，按未就绪处理", "sha256", sha)
		return 0, apperr.ErrDeliveryBlobNotFound
	}
	return blob.SizeBytes, nil
}

// Open 打开就绪 blob 供流式下载（GET+Range 语义，spec §4.5.3）：占用下载并发额度并返回文件句柄与释放函数。
// 调用方须在响应写完后先 Close 文件、再调用 release 归还额度；未就绪返回 blob_not_found。
func (s *DeliveryBlobService) Open(sha string) (*os.File, int64, func(), error) {
	sha, err := normalizeBlobSHA(sha)
	if err != nil {
		return nil, 0, nil, err
	}
	if _, err := s.Head(sha); err != nil {
		return nil, 0, nil, err
	}
	if !s.downloads.tryAcquire(s.settings.GetInt(SettingDeliveryDownloadConcurrency)) {
		return nil, 0, nil, apperr.ErrDeliveryDownloadBusy
	}
	file, err := os.Open(s.blobPath(sha))
	if err != nil {
		s.downloads.release()
		if os.IsNotExist(err) {
			return nil, 0, nil, apperr.ErrDeliveryBlobNotFound
		}
		return nil, 0, nil, err
	}
	st, err := file.Stat()
	if err != nil {
		_ = file.Close()
		s.downloads.release()
		return nil, 0, nil, err
	}
	return file, st.Size(), s.downloads.release, nil
}

// DeliveryBlobRequirement 是一条待上传需求（upload-manifest 项 / M3 payload 准备输入）：变更项粒度。
type DeliveryBlobRequirement struct {
	// 服务器根内相对路径
	Path string `json:"path"`
	// 内容哈希（小写 hex）
	SHA256 string `json:"sha256"`
	// 字节数
	Size int64 `json:"size"`
}

// MissingBlobs 求某变更单文件项中控制面尚缺（无元数据或未就绪）的 blob 需求集
// （spec §4.4.2 第 1 步；M3 编排器 payload 准备与模板源 upload-manifest 共用）。
// delete 项无内容不需上传；同 sha 多路径逐项列出（agent 侧经 HEAD 天然去重）。
func (s *DeliveryBlobService) MissingBlobs(orderID uint) ([]DeliveryBlobRequirement, error) {
	items, err := s.orders.ListItems(orderID)
	if err != nil {
		return nil, err
	}
	all := make([]DeliveryBlobRequirement, 0, len(items))
	shas := make([]string, 0, len(items))
	for _, item := range items {
		if item.Kind != model.ChangeItemKindFileDiff || item.SHA256 == nil || item.Action == nil || *item.Action == model.ChangeItemActionDelete {
			continue
		}
		req := DeliveryBlobRequirement{Path: derefString(item.Path), SHA256: *item.SHA256}
		if item.SizeBytes != nil {
			req.Size = *item.SizeBytes
		}
		all = append(all, req)
		shas = append(shas, *item.SHA256)
	}
	ready, err := s.blobs.ReadySet(shas)
	if err != nil {
		return nil, err
	}
	missing := make([]DeliveryBlobRequirement, 0, len(all))
	for _, req := range all {
		if _, ok := ready[req.SHA256]; !ok {
			missing = append(missing, req)
		}
	}
	return missing, nil
}

// PrepareConfigBlobs 控制面在启动 payload 准备期为本单每个 config_change 项按每个目标渲染灰度生效明文并写入
// 内容寻址 blob（restart 读盘生效依据，ADR-0071 决策2）：区别于文件项由模板源 agent 上传中转——配置明文由
// 控制面主动渲染写入。per-target 渲染经 content-addressed sha256 天然去重（同作用域层多目标渲染相同明文只落
// 一个 blob）。无配置项 / 无目标 / 未装配渲染器则空操作。渲染失败原样上抛（由调用方脱敏展示，ADR-0057）。
func (s *DeliveryBlobService) PrepareConfigBlobs(orderID uint, serverIDs []string) error {
	if s.configRenderer == nil || len(serverIDs) == 0 {
		return nil
	}
	items, err := s.orders.ListItems(orderID)
	if err != nil {
		return err
	}
	written := make(map[string]struct{}) // 已落盘 blob 的 sha（跨目标去重，省重复落盘往返）
	artifactIdx := make(map[string]int)  // (目标, 路径) → artifacts 下标，同键后项覆盖（防批量 upsert 跨方言重复键）
	artifacts := make([]model.DeliveryConfigArtifact, 0)
	for i := range items {
		if items[i].Kind != model.ChangeItemKindConfigChange {
			continue
		}
		for _, serverID := range serverIDs {
			rendered, err := s.configRenderer.render(&items[i], serverID)
			if err != nil {
				return err
			}
			// 每个 (项, 目标) 都冻结一条工件（授权与 manifest 按目标反查，不随 blob 去重）；
			artifact := model.DeliveryConfigArtifact{
				OrderID: orderID, ServerID: serverID,
				Path: rendered.Path, SHA256: rendered.SHA256, SizeBytes: rendered.Size,
			}
			key := serverID + "\x00" + rendered.Path
			if idx, dup := artifactIdx[key]; dup {
				artifacts[idx] = artifact
			} else {
				artifactIdx[key] = len(artifacts)
				artifacts = append(artifacts, artifact)
			}
			// 而 blob 落盘按内容 sha 去重（同作用域多目标渲染相同明文只落一个 blob）。
			if _, dup := written[rendered.SHA256]; dup {
				continue
			}
			if err := s.StoreInternal(rendered.SHA256, rendered.Size, strings.NewReader(rendered.Content)); err != nil {
				return err
			}
			written[rendered.SHA256] = struct{}{}
		}
	}
	return s.artifacts.UpsertBatch(artifacts)
}

// TouchReferences 刷新某变更单全部文件项引用 blob 的 last_referenced_at（清理保护）。
// M3 编排器在单启动 / 准备期调用；本切片在模板源上传回执成功时调用。
func (s *DeliveryBlobService) TouchReferences(orderID uint) error {
	items, err := s.orders.ListItems(orderID)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(items))
	shas := make([]string, 0, len(items))
	for _, item := range items {
		if item.Kind != model.ChangeItemKindFileDiff || item.SHA256 == nil {
			continue
		}
		if _, dup := seen[*item.SHA256]; dup {
			continue
		}
		seen[*item.SHA256] = struct{}{}
		shas = append(shas, *item.SHA256)
	}
	// 配置冻结渲染工件 sha（ADR-0071）一并刷新引用，防保留期清理误删 config blob。
	cfgSHAs, err := s.artifacts.ListSHAsByOrder(orderID)
	if err != nil {
		return err
	}
	for _, sha := range cfgSHAs {
		if _, dup := seen[sha]; dup {
			continue
		}
		seen[sha] = struct{}{}
		shas = append(shas, sha)
	}
	return s.blobs.TouchAll(shas, time.Now().UTC())
}

// derefString 安全解引用字符串指针（nil 返回空串）。
func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
