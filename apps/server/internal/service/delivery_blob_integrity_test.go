package service

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// —— FR-261：blob 引用保护与清理一致性（红→绿）——

// newIntegrityDB 打开一段独立内存 sqlite 并迁移数据面相关表（与 newBlobTestSvc 分离，便于只迁所需表）。
func newIntegrityDB(t *testing.T, name string) *gorm.DB {
	t.Helper()
	dsn := "file:integrity_" + name + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("取连接池失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&model.DeliveryBlob{}, &model.DeliveryConfigArtifact{}, &model.ChangeOrder{},
		&model.ChangeOrderItem{}, &model.ChangeTarget{}, &model.AgentCommand{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	return db
}

// newIntegritySvc 装配 blob 数据面（临时 blob 根），返回服务、DB 与清理器。
func newIntegritySvc(t *testing.T, name string) (*DeliveryBlobService, *gorm.DB, *DeliveryBlobCleaner) {
	t.Helper()
	db := newIntegrityDB(t, name)
	svc := NewDeliveryBlobService(db, repository.NewDeliveryBlobRepository(db),
		repository.NewChangeOrderRepository(db), repository.NewAgentCommandRepository(db), looseBlobSettings())
	svc.SetRoot(t.TempDir())
	return svc, db, NewDeliveryBlobCleaner(svc, repository.NewAuditLogRepository(db))
}

// TestMarkReadyZeroRowsFails MarkReady 影响 0 行（占位行已被清理器回收）时必须报错，不得静默假就绪。
func TestMarkReadyZeroRowsFails(t *testing.T) {
	db := newIntegrityDB(t, "markready")
	repo := repository.NewDeliveryBlobRepository(db)
	sha := hex.EncodeToString(bytes.Repeat([]byte{0xab}, 32))

	// 未建占位行即落账：UPDATE 影响 0 行。旧实现静默返回 nil，制造「元数据无行却以为成功」的幽灵态。
	if err := repo.MarkReady(sha, 128, time.Now().UTC()); !errors.Is(err, apperr.ErrDeliveryBlobSlotLost) {
		t.Fatalf("MarkReady 影响 0 行应返回 blob_upload_slot_lost，实际 %v", err)
	}

	// 有占位行时正常落账。
	if err := repo.UpsertUploading(sha, 128, time.Now().UTC()); err != nil {
		t.Fatalf("建占位行失败: %v", err)
	}
	if err := repo.MarkReady(sha, 128, time.Now().UTC()); err != nil {
		t.Fatalf("有占位行时 MarkReady 应成功，实际 %v", err)
	}
	// 重复落账且值未变：MySQL 的 RowsAffected 计「真正被修改」的行数（值未变即 0），
	// sqlite 计「匹配」的行数（计 1）。只看 RowsAffected 会在 MySQL 上把重复落账误报成丢槽。
	// 故重复落账必须幂等成功（行在即成功）。
	at := time.Now().UTC()
	if err := repo.MarkReady(sha, 128, at); err != nil {
		t.Fatalf("重复落账（值未变）应幂等成功，不得误报丢槽，实际 %v", err)
	}
}

// TestPersistBlobSlotLostSurfaces 上传链路把 MarkReady 的行数校验结果透到调用方（不吞成成功）。
func TestPersistBlobSlotLostSurfaces(t *testing.T) {
	svc, db, _ := newIntegritySvc(t, "slotlost")
	content := []byte("slot lost content")
	sha := shaOf(content)

	// 抢在落账前把占位行删掉（模拟清理器并发回收 uploading 残留）。
	if err := db.Model(&model.DeliveryBlob{}).Where("sha256 = ?", sha).Delete(&model.DeliveryBlob{}).Error; err != nil {
		t.Fatalf("预清失败: %v", err)
	}
	// 直接用仓库调用验证错误可透出（服务层的秒传 / 容量分支会先拦截，故此处锁仓库层契约）。
	repo := repository.NewDeliveryBlobRepository(db)
	if err := repo.MarkReady(sha, int64(len(content)), time.Now().UTC()); !errors.Is(err, apperr.ErrDeliveryBlobSlotLost) {
		t.Fatalf("落账丢槽应返回 blob_upload_slot_lost，实际 %v", err)
	}
	if _, err := svc.Head(sha); !errors.Is(err, apperr.ErrDeliveryBlobNotFound) {
		t.Fatalf("丢槽的 blob 不应就绪，实际 %v", err)
	}
}

// TestCleanerPurgesOrphanFile 孤儿文件回收：磁盘有文件但元数据无行 → 删文件（防磁盘泄漏）。
func TestCleanerPurgesOrphanFile(t *testing.T) {
	svc, _, cleaner := newIntegritySvc(t, "orphanfile")
	sha := hex.EncodeToString(bytes.Repeat([]byte{0xcd}, 32))
	// 手动铺一个「只有文件、没有元数据行」的孤儿（不建占位行，直接写字落盘）。
	if err := os.MkdirAll(filepath.Dir(svc.blobPath(sha)), 0o755); err != nil {
		t.Fatalf("建 blob 目录失败: %v", err)
	}
	if err := os.WriteFile(svc.blobPath(sha), []byte("orphan payload"), 0o644); err != nil {
		t.Fatalf("写孤儿文件失败: %v", err)
	}

	cleaner.SweepOnce()

	if _, err := os.Stat(svc.blobPath(sha)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("无元数据行的孤儿文件应被回收，实际 stat err=%v", err)
	}
}

// TestCleanerPurgesOrphanMetadata 孤儿元数据回收：元数据标 ready 但磁盘文件缺失 → 删元数据行，
// 使 Head 与真实盘面一致（不再「以为就绪、打开才 404」）。
func TestCleanerPurgesOrphanMetadata(t *testing.T) {
	svc, _, cleaner := newIntegritySvc(t, "orphanmeta")
	sha := hex.EncodeToString(bytes.Repeat([]byte{0xef}, 32))
	if err := svc.blobs.UpsertUploading(sha, 32, time.Now().UTC()); err != nil {
		t.Fatalf("建占位行失败: %v", err)
	}
	// 元数据置 ready 但从不落盘文件（模拟落盘中断 / 外部删除）。
	if err := svc.blobs.MarkReady(sha, 32, time.Now().UTC().AddDate(0, 0, -30)); err != nil {
		t.Fatalf("置 ready 失败: %v", err)
	}

	cleaner.SweepOnce()

	got, err := svc.blobs.FindBySHA256(sha)
	if err != nil {
		t.Fatalf("查元数据失败: %v", err)
	}
	if got != nil {
		t.Fatalf("磁盘缺失的 ready 元数据行应被回收，实际仍存在 %+v", got)
	}
}

// TestCleanerKeepsForeignFile 孤儿扫描不得误删非本域产物（文件名不是 64 位小写 hex 的文件）。
func TestCleanerKeepsForeignFile(t *testing.T) {
	svc, _, cleaner := newIntegritySvc(t, "foreign")
	foreign := filepath.Join(svc.root, "blobs", "ab", "README.txt")
	if err := os.MkdirAll(filepath.Dir(foreign), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	if err := os.WriteFile(foreign, []byte("not a blob"), 0o644); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}

	cleaner.SweepOnce()

	if _, err := os.Stat(foreign); err != nil {
		t.Fatalf("非 sha256 命名的文件不应被清理器删除，实际 err=%v", err)
	}
}

// TestCleanerStaleUploadingRemovesFile 上传残留清理：除删元数据行外一并删该 sha 的磁盘文件（FR-261）。
func TestCleanerStaleUploadingRemovesFile(t *testing.T) {
	svc, _, cleaner := newIntegritySvc(t, "stalefile")
	sha := hex.EncodeToString(bytes.Repeat([]byte{0x9a}, 32))
	// 铺一个 uploading 残留的盘面文件（哈希不符被丢弃或 rename 前中断的残留形态）。
	if err := os.MkdirAll(filepath.Dir(svc.blobPath(sha)), 0o755); err != nil {
		t.Fatalf("建 blob 目录失败: %v", err)
	}
	if err := os.WriteFile(svc.blobPath(sha), []byte("stale uploading"), 0o644); err != nil {
		t.Fatalf("写残留文件失败: %v", err)
	}
	stale := time.Now().UTC().Add(-48 * time.Hour)
	if err := svc.blobs.UpsertUploading(sha, 32, stale); err != nil {
		t.Fatalf("建 uploading 残留失败: %v", err)
	}

	cleaner.SweepOnce()

	if _, err := os.Stat(svc.blobPath(sha)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("uploading 残留的磁盘文件应随元数据一并清除，实际 stat err=%v", err)
	}
}

// TestTouchReferencesProtectsActiveOrderBlob 引用刷新补全：活动单引用的 blob 即便超保留期也不被删。
// 覆盖「刷新发生在启动 / 上传回执之外的路径」这一缺口——只要刷新被调用，清理就不得删。
func TestTouchReferencesProtectsActiveOrderBlob(t *testing.T) {
	svc, db, cleaner := newIntegritySvc(t, "touch")
	content := []byte("active order referenced")
	sha := shaOf(content)
	mustStore(t, svc, sha, content)
	order := model.ChangeOrder{NamespaceID: 1, Title: "t", Status: model.ChangeOrderStatusRolling, SourceServerID: "src"}
	mustCreate(t, db, &order)
	shaCopy := sha
	action := model.ChangeItemActionAdd
	path := "plugins/demo.jar"
	mustCreate(t, db, &model.ChangeOrderItem{
		OrderID: order.ID, Kind: model.ChangeItemKindFileDiff,
		Path: &path, Action: &action, SHA256: &shaCopy,
	})
	// 引用时间回拨到保留期外，若不刷新引用就会被误删。
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if err := db.Model(&model.DeliveryBlob{}).Where("sha256 = ?", sha).Update("last_referenced_at", old).Error; err != nil {
		t.Fatalf("回拨引用时间失败: %v", err)
	}

	// 启动 / 下发前刷新引用（FR-261 补全的调用点语义）。
	if err := svc.TouchReferences(order.ID); err != nil {
		t.Fatalf("刷新引用失败: %v", err)
	}
	cleaner.SweepOnce()

	if _, err := svc.Head(sha); err != nil {
		t.Fatalf("刷新过引用的活动单 blob 不应被清理，实际 %v", err)
	}
}

// TestCleanerReclaimsUnreferencedBlob 无人引用的 blob 不受保留期拖延（FR-261 补偿删除）：
// 既孤儿盘 footprint 又滞留到保留期满是双重浪费——引用集合以外的 blob 下轮即回收。
func TestCleanerReclaimsUnreferencedBlob(t *testing.T) {
	svc, db, cleaner := newIntegritySvc(t, "unreferenced")
	content := []byte("blob nobody references anymore")
	sha := shaOf(content)
	mustStore(t, svc, sha, content)
	// 该 blob 不被任何变更单引用（配置项 / 文件项都没有相关行）。
	var itemCount int64
	db.Model(&model.ChangeOrderItem{}).Count(&itemCount)
	if itemCount != 0 {
		t.Fatalf("本用例前提是无任何变更项，实际 %d 条", itemCount)
	}
	// 同批次放一个「有引用」的对照 blob：它必须活下来。
	// 没有这条反例的话，一个「无条件删光」的错误实现也能骗过本用例。
	kept := []byte("referenced sibling stays")
	keptSHA := shaOf(kept)
	mustStore(t, svc, keptSHA, kept)
	seedOrderWithFileItem(t, db, model.ChangeOrderStatusApproved, keptSHA)
	// 两个 blob 都推过宽限（否则对照不成立——新鲜 blob 本就受宽限保护）。
	stale := time.Now().UTC().Add(-2 * time.Hour)
	if err := db.Model(&model.DeliveryBlob{}).Where("sha256 IN ?", []string{sha, keptSHA}).
		Update("last_referenced_at", stale).Error; err != nil {
		t.Fatalf("回拨引用时间失败: %v", err)
	}

	// 前提校验（必须在 SweepOnce 之前）：对照 blob 必须真的是「有引用且已过宽限」的候选，
	// 否则「有引用的 blob 活下来」这条反例形同虚设——无条件删光的错误实现也能骗过本用例。
	var refCount int64
	db.Model(&model.ChangeOrderItem{}).Where("sha256 = ?", keptSHA).Count(&refCount)
	if refCount == 0 {
		t.Fatal("前提不成立：对照 blob 的引用行未建出来")
	}
	var candidate int64
	db.Model(&model.DeliveryBlob{}).Where("sha256 = ? AND state = ? AND last_referenced_at < ?",
		keptSHA, model.DeliveryBlobStateReady, time.Now().UTC().Add(-time.Hour)).Count(&candidate)
	if candidate == 0 {
		t.Fatal("前提不成立：对照 blob 不是「已过宽限的 ready 候选」，反例不构成对照")
	}

	cleaner.SweepOnce()

	if _, err := svc.Head(sha); !errors.Is(err, apperr.ErrDeliveryBlobNotFound) {
		t.Fatalf("无人引用的 blob 应被回收，实际仍在: %v", err)
	}
	if _, err := os.Stat(svc.blobPath(sha)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("无人引用的 blob 磁盘文件应一并删除，实际 stat err=%v", err)
	}
	// 反例：同批次有引用的 blob 必须还在（证明回收是「按引用判定」而非「无条件删光」）。
	if _, err := svc.Head(keptSHA); err != nil {
		t.Fatalf("有引用的对照 blob 不应被回收，实际 %v", err)
	}
	if _, err := os.Stat(svc.blobPath(keptSHA)); err != nil {
		t.Fatalf("有引用的对照 blob 磁盘文件不应被删，实际 stat err=%v", err)
	}
}

// TestPurgeBlobDeletesRowBeforeFile 删除顺序：必须先删元数据行、行删成功才删盘（P0-1）。
//
// 反序（先删盘）会开一个窗口：并发上传者在保护集查询之后、删盘之前提交了引用行，
// 盘已删而行还在 → Head 按「元数据 ready 但磁盘缺失即未就绪」返回 404，目标下载失败。
// 行是保护判定的真源，故以「行删成功」作为删盘的门票。
func TestPurgeBlobDeletesRowBeforeFile(t *testing.T) {
	svc, db, _ := newIntegritySvc(t, "purgeorder")
	content := []byte("purge ordering probe")
	sha := shaOf(content)
	mustStore(t, svc, sha, content)

	// 让「删行」必然失败（模拟并发下引用行已插入、DELETE 被挡或事务回滚）。
	repo := repository.NewDeliveryBlobRepository(db)
	svc.blobs = repo
	if err := db.Exec("CREATE TRIGGER IF NOT EXISTS block_blob_delete " +
		"BEFORE DELETE ON delivery_blob BEGIN SELECT RAISE(ABORT, 'blocked'); END").Error; err != nil {
		t.Fatalf("建拦截触发器失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Exec("DROP TRIGGER IF EXISTS block_blob_delete").Error })

	cleaner := NewDeliveryBlobCleaner(svc, repository.NewAuditLogRepository(db))
	if cleaner.purgeBlob(sha) {
		t.Fatal("行删失败时不应计为成功")
	}
	// 关键断言：行没删掉，盘上的文件就必须还在（否则就是先删盘的反序实现）。
	if _, err := os.Stat(svc.blobPath(sha)); err != nil {
		t.Fatalf("行删失败时不得先删盘（TOCTOU 复发），实际 stat err=%v", err)
	}
	// 反向对照：放行删行后，盘上文件应随之消失。
	_ = db.Exec("DROP TRIGGER IF EXISTS block_blob_delete")
	if !cleaner.purgeBlob(sha) {
		t.Fatal("放行后删行应成功")
	}
	if _, err := os.Stat(svc.blobPath(sha)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("行删成功后应删盘，实际 stat err=%v", err)
	}
}

// TestCleanerProtectsApprovedOrderBlob approved 单引用的 blob 必须被持续刷新保护（P0-1）。
//
// approved 单在「审批通过 → 等模板源上传」的准备期可能停留远超 1 小时，若引用只在启动时
// 刷新一次，宽限一过补偿删除就会误删这张单马上要消费的 blob（目标侧 404 且无告警）。
func TestCleanerProtectsApprovedOrderBlob(t *testing.T) {
	svc, db, _ := newIntegritySvc(t, "approved")
	content := []byte("approved order pending upload")
	sha := shaOf(content)
	mustStore(t, svc, sha, content)
	seedOrderWithFileItem(t, db, model.ChangeOrderStatusApproved, sha)

	// 引用刷新集合必须覆盖 approved（推进器每轮刷新的单集合）。
	statuses := deliveryBlobReferenceRefreshStatuses
	if !containsBlobStatus(statuses, model.ChangeOrderStatusApproved) {
		t.Fatalf("引用刷新状态集应含 approved，实际 %v", statuses)
	}
	// 该状态必须能被「按状态取单」的接口选中（即刷新真能作用到它）。
	orders, err := repository.NewChangeOrderRepository(db).ListActiveOrders(statuses)
	if err != nil {
		t.Fatalf("按刷新状态集取单失败: %v", err)
	}
	if len(orders) != 1 || orders[0].Status != model.ChangeOrderStatusApproved {
		t.Fatalf("approved 单应被引用刷新集合选中，实际 %+v", orders)
	}
	// 刷新后引用时间应被推到当下（宽限从「最后一次刷新」起算才保护得住）。
	if err := db.Model(&model.DeliveryBlob{}).Where("sha256 = ?", sha).
		Update("last_referenced_at", time.Now().UTC().Add(-2*time.Hour)).Error; err != nil {
		t.Fatalf("回拨引用时间失败: %v", err)
	}
	ids := make([]uint, 0, len(orders))
	for i := range orders {
		ids = append(ids, orders[i].ID)
	}
	if err := svc.TouchReferencesForOrders(ids); err != nil {
		t.Fatalf("刷新引用失败: %v", err)
	}
	var fresh model.DeliveryBlob
	if err := db.Where("sha256 = ?", sha).First(&fresh).Error; err != nil {
		t.Fatalf("读 blob 失败: %v", err)
	}
	if time.Since(fresh.LastReferencedAt) > 5*time.Minute {
		t.Fatalf("刷新后引用时间应回到当下，实际 %v", fresh.LastReferencedAt)
	}
}

// containsBlobStatus 判定字符串切片是否含某值（测试小工具）。
func containsBlobStatus(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// TestCleanerGraceProtectsFreshUpload 宽限内的新鲜 blob 即便暂无人引用也不被回收——
// 写入 blob 与「引用它的变更项落库」不在同一事务，零宽限会误删刚上传、马上要被消费的 blob。
func TestCleanerGraceProtectsFreshUpload(t *testing.T) {
	svc, _, cleaner := newIntegritySvc(t, "freshgrace")
	content := []byte("just uploaded, references not committed yet")
	sha := shaOf(content)
	mustStore(t, svc, sha, content) // last_referenced_at = 现在

	cleaner.SweepOnce()

	if _, err := svc.Head(sha); err != nil {
		t.Fatalf("宽限内的新鲜 blob 不应被回收，实际 %v", err)
	}
}

// TestCleanerKeepsReferencedBlobBeforeRetention 受引用保护的反面对照：仍在保留期内且被引用 → 不删。
func TestCleanerKeepsReferencedBlobBeforeRetention(t *testing.T) {
	svc, db, cleaner := newIntegritySvc(t, "protected")
	content := []byte("referenced and fresh")
	sha := shaOf(content)
	mustStore(t, svc, sha, content)
	seedOrderWithFileItem(t, db, model.ChangeOrderStatusRolling, sha)

	cleaner.SweepOnce()

	if _, err := svc.Head(sha); err != nil {
		t.Fatalf("保留期内且被活动单引用的 blob 不应被清理，实际 %v", err)
	}
}

// TestTouchReferencesForOrdersNoop 批量刷新：空集合与不存在的单都不得报错（调用点可无条件挂载）。
func TestTouchReferencesForOrdersNoop(t *testing.T) {
	svc, _, _ := newIntegritySvc(t, "touchnoop")
	if err := svc.TouchReferencesForOrders(nil); err != nil {
		t.Fatalf("空集合刷新应 no-op，实际 %v", err)
	}
	if err := svc.TouchReferencesForOrders([]uint{99999}); err != nil {
		t.Fatalf("不存在的单刷新应 no-op，实际 %v", err)
	}
}
