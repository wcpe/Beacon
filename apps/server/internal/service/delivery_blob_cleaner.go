package service

import (
	"context"
	"encoding/json"
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

// NewDeliveryBlobCleaner 构造清理器（时间源默认 time.Now，测试可覆盖 now 字段）。
func NewDeliveryBlobCleaner(svc *DeliveryBlobService, audit *repository.AuditLogRepository) *DeliveryBlobCleaner {
	return &DeliveryBlobCleaner{svc: svc, audit: audit, now: time.Now}
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
	deleted += staleDeleted
	freed += staleFreed
	if deleted > 0 {
		c.recordAudit(deleted, freed)
	}
}

// purgeTerminalBlobs 删除「ready 且 last_referenced_at 超保留期、且不被非终态单引用」的 blob。
func (c *DeliveryBlobCleaner) purgeTerminalBlobs() (int, int64) {
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
	// 被非终态单（含活动单与未执行完的 draft/pending/approved）引用的 sha 受保护，不删。
	protected, err := c.svc.orders.ListSHAsReferencedByStatusNotIn(shas, changeOrderTerminalStatuses)
	if err != nil {
		return 0, 0
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

// purgeStaleUploading 清除上传中断残留：uploading 元数据（超 24h）+ tmp 目录旧临时文件。
func (c *DeliveryBlobCleaner) purgeStaleUploading() (int, int64) {
	cutoff := c.now().Add(-deliveryUploadingStaleHours * time.Hour)
	var deleted int
	var freed int64
	if stale, err := c.svc.blobs.ListUploadingBefore(cutoff); err == nil {
		for i := range stale {
			if c.svc.blobs.Delete(stale[i].SHA256) == nil {
				deleted++
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
		Result:     "success",
	})
}
