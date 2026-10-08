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
	// deliveryOrphanScanFileLimit 是单轮孤儿扫描的文件数上限（FR-261 P1-3）：
	// 超大部署下 blobs 目录可能达万级文件，全量扫会把每轮清理的 IO 放大成热瓶颈。
	// 单轮有界截断，**但起点每轮轮转**（见 DeliveryBlobCleaner.orphanCursor）——
	// 只截断不轮转的话，字典序靠后的分片永远扫不到，孤儿会持续累积。
	deliveryOrphanScanFileLimit = 5000
)

// DeliveryBlobCleaner 是交付中转 blob 的后台清理器（FR-165，spec §4.5.4）：周期删除
// 「ready 且超保留期、且不被非终态变更单引用」的 blob（磁盘 + 元数据），并清除上传中断残留
// （uploading 元数据 + tmp 目录旧临时文件，超 24h）。有删除时记系统审计（actor=system）。
// 保留期 / 清理间隔热读运维设置，改设置即热生效。
type DeliveryBlobCleaner struct {
	svc   *DeliveryBlobService
	audit *repository.AuditLogRepository
	now   func() time.Time
	// orphanScanLimit 单轮孤儿扫描的文件数上限（默认 deliveryOrphanScanFileLimit，测试可压小）。
	orphanScanLimit int
	// orphanCursor 是孤儿扫描的轮转游标：记录本轮结束时扫到的 (分片, 文件名)，下轮从**其后**继续。
	// 为什么必须轮转：os.ReadDir 按字典序返回，只截断不轮转的话每轮都切在同一位置，
	// 字典序靠后的分片永远扫不到——孤儿持续累积成磁盘泄漏，而不是「留待下轮」。
	orphanCursor string
}

// NewDeliveryBlobCleaner 构造清理器（时间源默认 UTC，测试可覆盖 now 字段）。
//
// 时间源**必须**是 UTC：`delivery_blob.last_referenced_at` 全链路以 UTC 落库（见 persistBlob / TouchAll），
// 用本地时间与它相减会整体偏移一个时区差（UTC+8 机器上等于把保留期 / 宽限提前 8 小时判定），
// 因而误删仍在宽限内的新鲜 blob。
func NewDeliveryBlobCleaner(svc *DeliveryBlobService, audit *repository.AuditLogRepository) *DeliveryBlobCleaner {
	return &DeliveryBlobCleaner{
		svc: svc, audit: audit, now: func() time.Time { return time.Now().UTC() },
		orphanScanLimit: deliveryOrphanScanFileLimit,
	}
}

// orphanScanFileLimit 取本轮扫描上限（未配置 / 非正值时回退到默认值）。
func (c *DeliveryBlobCleaner) orphanScanFileLimit() int {
	if c.orphanScanLimit > 0 {
		return c.orphanScanLimit
	}
	return deliveryOrphanScanFileLimit
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
	toDelete := make([]string, 0, len(candidates))
	for i := range candidates {
		if _, reclaim := unreferenced[candidates[i].SHA256]; reclaim {
			toDelete = append(toDelete, candidates[i].SHA256)
		}
	}
	if len(toDelete) == 0 {
		return 0, 0
	}
	// 删除前**二次确认引用**（TOCTOU 收口）：上面判定与这里删除之间存在时间窗，
	// 期间可能有变更单已提交引用行（准备期上传的单尤其常见）。不二次确认就等于
	// 拿一分钟前的快照去删别人的 blob——正是 FR-261 要根除的「删掉仍在被消费的 blob」。
	confirmed, err := c.listUnreferencedSHAs(toDelete)
	if err != nil {
		return 0, 0
	}
	for _, sha := range toDelete {
		if _, reclaim := confirmed[sha]; !reclaim {
			continue
		}
		if c.purgeBlob(sha) {
			deleted++
			freed += blobSizeOf(candidates, sha)
		}
	}
	return deleted, freed
}

// blobSizeOf 从候选集里取某 sha 的声明字节数（审计 freedBytes 用；未命中记 0）。
func blobSizeOf(candidates []model.DeliveryBlob, sha string) int64 {
	for i := range candidates {
		if candidates[i].SHA256 == sha {
			return candidates[i].SizeBytes
		}
	}
	return 0
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
	toDelete := make([]string, 0, len(candidates))
	for i := range candidates {
		if _, keep := protected[candidates[i].SHA256]; !keep {
			toDelete = append(toDelete, candidates[i].SHA256)
		}
	}
	if len(toDelete) == 0 {
		return 0, 0
	}
	// 删除前二次确认「仍不被非终态单引用」（TOCTOU 收口，同 purgeUnreferencedBlobs）：
	// 准备期的 approved 单随时可能在判定与删除之间提交引用行。
	stillProtected, err := c.listReferencedSHAs(toDelete, changeOrderTerminalStatuses)
	if err != nil {
		return 0, 0
	}
	for _, sha := range toDelete {
		if _, keep := stillProtected[sha]; keep {
			continue
		}
		if c.purgeBlob(sha) {
			deleted++
			freed += blobSizeOf(candidates, sha)
		}
	}
	return deleted, freed
}

// listReferencedSHAs 求给定 sha 集合中「被状态不在 excluded 集合内的变更单引用」的子集
// （保护判定；excluded 为 nil 表示不施加状态过滤）。文件项与配置冻结工件两侧取并集。
func (c *DeliveryBlobCleaner) listReferencedSHAs(shas []string, excluded []string) (map[string]struct{}, error) {
	out, err := c.svc.orders.ListSHAsReferencedByStatusNotIn(shas, excluded)
	if err != nil {
		return nil, err
	}
	cfg, err := c.svc.artifacts.ListSHAsReferencedByStatusNotIn(shas, excluded)
	if err != nil {
		return nil, err
	}
	for sha := range cfg {
		out[sha] = struct{}{}
	}
	return out, nil
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
//
// 批量化 + 有界 + **起点轮转**（FR-261 P1-3 + 复审 P2-1）：
// 先收集本轮候选 sha，**一次**批量取回它们的就绪态（逐文件查一次是 N+1，小文件场景单轮可达万级查询），
// 再走内存差集判定。单轮扫描文件数超上限即截断，**下轮从本轮结束处继续**（游标轮转）——
// 只截断不轮转的话，字典序靠后的分片永远扫不到，孤儿持续累积成磁盘泄漏。
func (c *DeliveryBlobCleaner) purgeOrphans() (int, int64) {
	root := filepath.Join(c.svc.root, "blobs")
	shards, err := os.ReadDir(root)
	if err != nil {
		return 0, 0 // 根目录尚未创建（从未上传过），无孤儿可回收
	}
	limit := c.orphanScanFileLimit()
	names := make([]string, 0, limit)
	cursor := c.orphanCursor
	nextCursor := ""
	wrapped := false // 本轮是否已从游标回到头部（用于跨轮接续）
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, shard.Name()))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !isSHA256Hex(entry.Name()) {
				continue // 子目录 / 非本域产物，不碰
			}
			key := shard.Name() + "/" + entry.Name()
			// 游标之前的部分上一轮已扫过（含跨轮回到头部的情形），跳过。
			if !wrapped && cursor != "" && key <= cursor {
				continue
			}
			wrapped = true
			// 达上限即截断。**不更新 nextCursor**（它记录的是「最后一个已处理的键」），
			// 故本项留给下一轮作为起点——若在此把未处理的键写进游标，下一轮的
			// 「跳过 <= cursor」会把它永久跳过去，正是「永不轮转」的同款泄漏。
			if len(names) >= limit {
				break
			}
			names = append(names, entry.Name())
			nextCursor = key
		}
		if nextCursor != "" && len(names) >= limit {
			break
		}
	}
	// 未截断即本轮走完全量：游标归零，下轮从头开始（常规规模下等价于全量扫）。
	if len(names) < limit {
		nextCursor = ""
	}
	c.orphanCursor = nextCursor
	if len(names) == 0 {
		return 0, 0
	}
	// 一次批量取回就绪态（缺失的 sha 不出现在结果里），替代逐文件 FindBySHA256。
	states, err := c.svc.blobs.StatesBySHAs(names)
	if err != nil {
		return 0, 0 // 元数据读失败不猜、不动盘
	}
	var deleted int
	var freed int64
	for _, sha := range names {
		deletedOne, freedOne := c.purgeOrphan(sha, states)
		deleted += deletedOne
		freed += freedOne
	}
	return deleted, freed
}

// purgeOrphan 收口单个 sha 的孤儿形态（返回是否删了东西、释放字节）。
// states 是本轮批量取回的「sha → 就绪态」快照（缺失即无元数据行）。
func (c *DeliveryBlobCleaner) purgeOrphan(sha string, states map[string]string) (int, int64) {
	path := c.svc.blobPath(sha)
	info, statErr := os.Stat(path)
	state, hasRow := states[sha]
	// 形态①：文件在、元数据缺或非 ready → 删文件，防磁盘泄漏。
	if statErr == nil {
		if !hasRow || state != model.DeliveryBlobStateReady {
			if os.Remove(path) == nil {
				return 1, info.Size()
			}
		}
		return 0, 0
	}
	// 形态②：文件缺失、元数据标 ready → 删元数据行：让 Head 回到「未就绪」而不是
	// 「以为就绪、直到目标下载时才 404」——后者的代价是目标侧推送失败且无告警。
	if errors.Is(statErr, os.ErrNotExist) && hasRow && state == model.DeliveryBlobStateReady {
		if c.svc.blobs.Delete(sha) == nil {
			// 同步快照，避免后续重复处理同一 sha。
			delete(states, sha)
			return 1, 0
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

// purgeBlob 删除单个 blob：**先删元数据行，行删成功才删盘**（顺序不可换）。
//
// 行是保护判定的真源——「先删盘」会开一个 TOCTOU 窗口：并发上传者在保护集查询之后、
// 删盘之前提交了引用行，盘已删而行还在，`Head` 按「元数据 ready 但磁盘缺失即未就绪」
// 返回 404，目标下载失败且无告警。这正是 FR-261 要修的病，只是换成补偿删除路径复发。
//
// 以「行删成功」作为删盘的门票：行删失败（并发下引用行已插入等）即不碰盘，
// 该 blob 下一轮重新参与判定。盘删失败不回滚行——行已删，重传会重建，不会留下幽灵态。
func (c *DeliveryBlobCleaner) purgeBlob(sha string) bool {
	if c.svc.blobs.Delete(sha) != nil {
		return false
	}
	_ = os.Remove(c.svc.blobPath(sha))
	return true
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
