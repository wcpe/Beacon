package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// 清理器兜底参数（运维设置未 seed 时使用；正常走 SettingsService 白名单默认）。
const (
	deliveryCleanupFallbackIntervalMin   = 60
	deliveryCleanupFallbackRetentionDays = 7
	deliveryUploadingStaleHours          = 24
	// deliveryUnreferencedGraceHours 是「无人引用 blob」的回收宽限（小时，FR-261）：
	// 远短于保留期（默认 7 天）。不能是 0——写入 blob 与引用它的变更项 / 配置工件落库不在同一事务，
	// 瞬时存在「已落盘、引用行未提交」的窗口，零宽限会误删刚上传、马上要被消费的 blob。
	deliveryUnreferencedGraceHours = 1
)

// DeliveryBlobCleaner 是交付中转 blob 的后台清理器（FR-165，spec §4.5.4）：周期删除
// 「ready 且超保留期、且不被非终态变更单引用」的 blob（磁盘 + 元数据），并清除上传中断残留
// （uploading 元数据 + tmp 目录旧临时文件，超 24h）。有删除时记系统审计（actor=system）。
// 保留期 / 清理间隔热读运维设置，改设置即热生效。
type DeliveryBlobCleaner struct {
	svc   *DeliveryBlobService
	audit *repository.AuditLogRepository
	now   func() time.Time
}

// NewDeliveryBlobCleaner 构造清理器（时间源默认 UTC，测试可覆盖 now 字段）。
//
// 时间源**必须**是 UTC：`delivery_blob.last_referenced_at` 全链路以 UTC 落库（见 persistBlob / TouchAll），
// 用本地时间与它相减会整体偏移一个时区差（UTC+8 机器上等于把保留期 / 宽限提前 8 小时判定），
// 因而误删仍在宽限内的新鲜 blob。
func NewDeliveryBlobCleaner(svc *DeliveryBlobService, audit *repository.AuditLogRepository) *DeliveryBlobCleaner {
	return &DeliveryBlobCleaner{svc: svc, audit: audit, now: func() time.Time { return time.Now().UTC() }}
}

// Run 启动周期清理循环（间隔热读 delivery.cleanup-interval-minutes），随 ctx 取消优雅退出。
func (c *DeliveryBlobCleaner) Run(ctx context.Context) {
	timer := time.NewTimer(c.interval())
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			c.SweepOnce()
			timer.Reset(c.interval())
		}
	}
}

// interval 读清理间隔（分钟），非法值兜底。
func (c *DeliveryBlobCleaner) interval() time.Duration {
	minutes := c.svc.settings.GetInt(SettingDeliveryCleanupIntervalMinutes)
	if minutes <= 0 {
		minutes = deliveryCleanupFallbackIntervalMin
	}
	return time.Duration(minutes) * time.Minute
}

// SweepOnce 执行一轮清理，有删除时记审计（可单测直接调用）。
func (c *DeliveryBlobCleaner) SweepOnce() {
	deleted, freed := c.purgeTerminalBlobs()
	staleDeleted, staleFreed := c.purgeStaleUploading()
	orphanDeleted, orphanFreed := c.purgeOrphans()
	deleted += staleDeleted + orphanDeleted
	freed += staleFreed + orphanFreed
	if deleted > 0 {
		c.recordAudit(deleted, freed)
	}
}

// purgeTerminalBlobs 回收两类中转 blob（FR-261）：
//   - ① 超保留期（默认 7 天）且不被非终态单引用的 ready blob；
//   - ② **无人引用**的 blob（补偿删除）：只过短宽限，不等保留期。
//
// 两类独立判定、结果合并。刻意不把 ② 挂在 ① 的候选集后面——那样一旦「超保留期候选为空」
// 就整轮跳过补偿删除，使无人引用的 blob 白等满保留期。
func (c *DeliveryBlobCleaner) purgeTerminalBlobs() (int, int64) {
	deleted, freed := c.purgeUnreferencedBlobs()
	expiredDeleted, expiredFreed := c.purgeRetentionExpiredBlobs()
	return deleted + expiredDeleted, freed + expiredFreed
}

// purgeUnreferencedBlobs 回收**无人引用**的 blob（FR-261 补偿删除）：只过短宽限，不等保留期。
//
// 依据：撤销审批回 draft 后删掉草稿、或单达终态后，其专属 blob 已无人消费，
// 等到保留期满纯属浪费容量——「不再被任何变更单（文件项 + 配置冻结工件）引用」本身就是回收依据。
//
// 仍留一个**短宽限**（deliveryUnreferencedGraceHours，默认 1 小时，远短于保留期 7 天）而非即时删：
// 写入 blob 与「引用它的变更项 / 配置工件落库」不在同一事务，瞬时存在「已落盘、引用行未提交」的窗口，
// 零宽限会误删刚上传、马上要被消费的 blob。宽限足够覆盖该窗口与控制面重启间隔。
func (c *DeliveryBlobCleaner) purgeUnreferencedBlobs() (int, int64) {
	graceCutoff := c.now().Add(-deliveryUnreferencedGraceHours * time.Hour)
	candidates, err := c.svc.blobs.ListReadyReferencedBefore(graceCutoff)
	if err != nil || len(candidates) == 0 {
		return 0, 0
	}
	shas := make([]string, len(candidates))
	for i := range candidates {
		shas[i] = candidates[i].SHA256
	}
	// excluded 传 nil = 不施加状态过滤：查「被任意状态单引用过」的全部 sha，两侧都不命中才是真无人引用。
	unreferenced, err := c.listUnreferencedSHAs(shas)
	if err != nil {
		return 0, 0 // 查询失败不猜、不误删
	}
	var deleted int
	var freed int64
	for i := range candidates {
		blob := candidates[i]
		if _, reclaim := unreferenced[blob.SHA256]; !reclaim {
			continue
		}
		if c.purgeBlob(blob.SHA256) {
			deleted++
			freed += blob.SizeBytes
		}
	}
	return deleted, freed
}

// purgeRetentionExpiredBlobs 删除「ready 且 last_referenced_at 超保留期、且不被非终态单引用」的 blob。
func (c *DeliveryBlobCleaner) purgeRetentionExpiredBlobs() (int, int64) {
	days := c.svc.settings.GetInt(SettingDeliveryBlobRetentionDays)
	if days <= 0 {
		days = deliveryCleanupFallbackRetentionDays
	}
	cutoff := c.now().Add(-time.Duration(days) * 24 * time.Hour)
	candidates, err := c.svc.blobs.ListReadyReferencedBefore(cutoff)
	if err != nil || len(candidates) == 0 {
		return 0, 0
	}
	shas := make([]string, len(candidates))
	for i := range candidates {
		shas[i] = candidates[i].SHA256
	}
	// 被非终态单（含活动单与未执行完的 draft/pending/approved）以文件项引用的 sha 受保护，不删。
	protected, err := c.svc.orders.ListSHAsReferencedByStatusNotIn(shas, changeOrderTerminalStatuses)
	if err != nil {
		return 0, 0
	}
	// 配置冻结渲染工件 sha 同受非终态单保护（ADR-0071）：并入受保护集合，防误删活动单 config blob。
	cfgProtected, err := c.svc.artifacts.ListSHAsReferencedByStatusNotIn(shas, changeOrderTerminalStatuses)
	if err != nil {
		return 0, 0
	}
	for sha := range cfgProtected {
		protected[sha] = struct{}{}
	}
	var deleted int
	var freed int64
	for i := range candidates {
		blob := candidates[i]
		if _, keep := protected[blob.SHA256]; keep {
			continue
		}
		if c.purgeBlob(blob.SHA256) {
			deleted++
			freed += blob.SizeBytes
		}
	}
	return deleted, freed
}

// listUnreferencedSHAs 求给定 sha 集合中**不被任何变更单引用**的子集（文件项 + 配置冻结工件两侧都不命中）。
// 这是「补偿删除」的判据：无人引用即无人消费，无需等保留期。查询失败返回错误（由调用方放弃本轮回收）。
func (c *DeliveryBlobCleaner) listUnreferencedSHAs(shas []string) (map[string]struct{}, error) {
	// excluded 传 nil 表示「不排除任何状态」——即查「被任意状态单引用过的」全部 sha。
	// 两个仓库方法都对 nil 做了兼容（不加 NOT IN 子句），避免生成非法的 `NOT IN ()`。
	byItems, err := c.svc.orders.ListSHAsReferencedByStatusNotIn(shas, nil)
	if err != nil {
		return nil, err
	}
	byArtifacts, err := c.svc.artifacts.ListSHAsReferencedByStatusNotIn(shas, nil)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(shas))
	for _, sha := range shas {
		if _, hit := byItems[sha]; hit {
			continue
		}
		if _, hit := byArtifacts[sha]; hit {
			continue
		}
		out[sha] = struct{}{}
	}
	return out, nil
}

// purgeOrphans 回收元数据与磁盘互不相认的孤儿（FR-261）：两类形态各自收口，使「表里说有」与「盘上真有」重新对齐。
//
//   - ① 磁盘有文件、元数据无行或非 ready（落盘后元数据被回滚 / 手工删库 / 落账丢失）→ 删文件，防磁盘泄漏。
//   - ② 元数据标 ready、磁盘文件缺失（外部清理 / 落盘中断）→ 删元数据行：让 Head 回到「未就绪」而不是
//     「以为就绪、直到目标下载时才 404」——后者的代价是目标侧推送失败且无告警。
//
// 文件名不是 64 位小写 hex 的一律跳过：blobs 目录可能被人手工放过别的东西，清理器不得越界删本域之外的产物。
func (c *DeliveryBlobCleaner) purgeOrphans() (int, int64) {
	root := filepath.Join(c.svc.root, "blobs")
	shards, err := os.ReadDir(root)
	if err != nil {
		return 0, 0 // 根目录尚未创建（从未上传过），无孤儿可回收
	}
	var deleted int
	var freed int64
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !isSHA256Hex(name) {
				continue // 非本域产物，不碰
			}
			deletedOne, freedOne := c.purgeOrphan(name)
			deleted += deletedOne
			freed += freedOne
		}
	}
	return deleted, freed
}

// purgeOrphan 收口单个 sha 的孤儿形态（返回是否删了东西、释放字节）。
func (c *DeliveryBlobCleaner) purgeOrphan(sha string) (int, int64) {
	path := c.svc.blobPath(sha)
	info, statErr := os.Stat(path)
	blob, err := c.svc.blobs.FindBySHA256(sha)
	if err != nil {
		return 0, 0 // 元数据读失败不猜、不动盘
	}
	// 形态①：文件在、元数据缺或非 ready → 删文件。
	if statErr == nil {
		if blob == nil || blob.State != model.DeliveryBlobStateReady {
			if os.Remove(path) == nil {
				return 1, info.Size()
			}
		}
		return 0, 0
	}
	// 形态②：文件缺失、元数据标 ready → 删元数据行（文件已无可删）。
	if errors.Is(statErr, os.ErrNotExist) && blob != nil && blob.State == model.DeliveryBlobStateReady {
		if c.svc.blobs.Delete(sha) == nil {
			return 1, blob.SizeBytes
		}
	}
	return 0, 0
}

// purgeStaleUploading 清除上传中断残留：uploading 元数据（超 24h）+ 该 sha 的磁盘文件 + tmp 目录旧临时文件。
//
// FR-261：此前只删元数据行、不删该 sha 的盘上文件——文件永久滞留占容量，且后续同 sha 重传会因
// placeBlobFile 的「目标已存在即视为去重成功」而跳过实算哈希校验，把一段内容未经校验地当成有效 blob。
func (c *DeliveryBlobCleaner) purgeStaleUploading() (int, int64) {
	cutoff := c.now().Add(-deliveryUploadingStaleHours * time.Hour)
	var deleted int
	var freed int64
	if stale, err := c.svc.blobs.ListUploadingBefore(cutoff); err == nil {
		for i := range stale {
			if c.svc.blobs.Delete(stale[i].SHA256) != nil {
				continue
			}
			deleted++
			// 同时回收该 sha 的盘上残留（可能不存在，Remove 幂等）。
			if info, statErr := os.Stat(c.svc.blobPath(stale[i].SHA256)); statErr == nil {
				if os.Remove(c.svc.blobPath(stale[i].SHA256)) == nil {
					freed += info.Size()
				}
			}
		}
	}
	// tmp 临时文件名随机、不对应 sha，按 mtime 清理。
	entries, err := os.ReadDir(c.svc.tmpDir())
	if err != nil {
		return deleted, freed
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		if os.Remove(filepath.Join(c.svc.tmpDir(), entry.Name())) == nil {
			freed += info.Size()
		}
	}
	return deleted, freed
}

// purgeBlob 删除单个 blob 的磁盘文件（幂等，不存在忽略）与元数据行。
func (c *DeliveryBlobCleaner) purgeBlob(sha string) bool {
	_ = os.Remove(c.svc.blobPath(sha))
	return c.svc.blobs.Delete(sha) == nil
}

// recordAudit 记一条系统清理审计（actor=system，含清理数量与释放字节；绝不含文件内容）。
func (c *DeliveryBlobCleaner) recordAudit(deleted int, freed int64) {
	detail, _ := json.Marshal(map[string]any{"deletedCount": deleted, "freedBytes": freed})
	_ = c.audit.Create(&model.AuditLog{
		Operator:   "system",
		Action:     model.ActionDeliveryOrderBlobCleanup,
		TargetType: model.TargetTypeDeliveryBlob,
		TargetRef:  deliveryBlobSweepRef,
		Detail:     string(detail),
		Result:     model.ResultOK,
	})
}
