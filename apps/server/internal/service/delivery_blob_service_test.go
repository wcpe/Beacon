package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// fakeBlobSettings 是数据面运维设置的假实现（各键返回可配置值）。
type fakeBlobSettings struct {
	capacity      int
	upload        int
	download      int
	retentionDays int
	cleanupMin    int
}

func (f *fakeBlobSettings) GetInt(key string) int {
	switch key {
	case SettingDeliveryBlobCapacityBytes:
		return f.capacity
	case SettingDeliveryUploadConcurrency:
		return f.upload
	case SettingDeliveryDownloadConcurrency:
		return f.download
	case SettingDeliveryBlobRetentionDays:
		return f.retentionDays
	case SettingDeliveryCleanupIntervalMinutes:
		return f.cleanupMin
	default:
		return 0
	}
}

// newBlobTestSvc 装配 blob 数据面（内存 sqlite + 临时 blob 根 + 宽松默认设置）。
func newBlobTestSvc(t *testing.T, settings *fakeBlobSettings) (*DeliveryBlobService, *gorm.DB) {
	t.Helper()
	name := "file:blob_" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(name), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
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
		&model.ChangeOrderItem{}, &model.ChangeTarget{}, &model.AgentCommand{}, &model.Server{},
		&model.AgentIdentity{}, &model.AuditLog{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	svc := NewDeliveryBlobService(db, repository.NewDeliveryBlobRepository(db),
		repository.NewChangeOrderRepository(db), repository.NewAgentCommandRepository(db), settings)
	svc.SetRoot(t.TempDir())
	return svc, db
}

func shaOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func looseBlobSettings() *fakeBlobSettings {
	return &fakeBlobSettings{capacity: 1 << 30, upload: 8, download: 64, retentionDays: 7, cleanupMin: 60}
}

// TestDeliveryBlobStoreDedupAndHashGuard 存储去重幂等 + 内容哈希守卫（spec §4.5.2）。
func TestDeliveryBlobStoreDedupAndHashGuard(t *testing.T) {
	svc, _ := newBlobTestSvc(t, looseBlobSettings())
	content := []byte("hello delivery blob payload")
	sha := shaOf(content)

	if err := svc.Store(sha, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("首次上传应成功: %v", err)
	}
	size, err := svc.Head(sha)
	if err != nil || size != int64(len(content)) {
		t.Fatalf("Head 应返回就绪 + 正确大小，实际 size=%d err=%v", size, err)
	}
	// 秒传：同 sha 已 ready，再存幂等成功、不报错。
	if err := svc.Store(sha, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("同 sha 重复上传应幂等成功: %v", err)
	}
	// 哈希守卫：声明 sha 与实际内容不符 → 丢弃 422，不落就绪。
	wrongSha := shaOf([]byte("different content entirely"))
	err = svc.Store(wrongSha, int64(len(content)), bytes.NewReader(content))
	if !errors.Is(err, apperr.ErrDeliveryBlobHashMismatch) {
		t.Fatalf("哈希不符应返回 blob_hash_mismatch，实际 %v", err)
	}
	if _, err := svc.Head(wrongSha); !errors.Is(err, apperr.ErrDeliveryBlobNotFound) {
		t.Fatalf("哈希不符的 blob 不应就绪，实际 %v", err)
	}
}

// TestDeliveryBlobCapacityReject 容量上限预检拒绝超限上传（spec §4.5.2）。
func TestDeliveryBlobCapacityReject(t *testing.T) {
	svc, _ := newBlobTestSvc(t, &fakeBlobSettings{capacity: 8, upload: 8, download: 64, retentionDays: 7, cleanupMin: 60})
	content := []byte("this exceeds eight bytes")
	err := svc.Store(shaOf(content), int64(len(content)), bytes.NewReader(content))
	if err == nil {
		t.Fatal("超容量上传应被拒绝")
	}
}

// TestDeliveryBlobHeadOpenAndMissing Head/Open 往返 + 磁盘缺失视同不就绪。
func TestDeliveryBlobHeadOpenAndMissing(t *testing.T) {
	svc, _ := newBlobTestSvc(t, looseBlobSettings())
	content := []byte("openable blob content")
	sha := shaOf(content)
	if err := svc.Store(sha, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	file, size, closeFn, err := svc.Open(sha)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	got, _ := io.ReadAll(file)
	_ = file.Close()
	closeFn()
	if size != int64(len(content)) || !bytes.Equal(got, content) {
		t.Fatalf("Open 内容 / 大小不符：size=%d got=%q", size, got)
	}
	// 磁盘文件被外部删除后，元数据虽 ready 也应判不就绪（Head 404）。
	if err := deleteBlobFile(svc, sha); err != nil {
		t.Fatalf("删磁盘文件失败: %v", err)
	}
	if _, err := svc.Head(sha); !errors.Is(err, apperr.ErrDeliveryBlobNotFound) {
		t.Fatalf("磁盘缺失应判不就绪，实际 %v", err)
	}
}

// TestDeliveryBlobCleanerTerminalAndProtect 清理器删终态超保留期 blob、保护被非终态单引用的 blob。
func TestDeliveryBlobCleanerTerminalAndProtect(t *testing.T) {
	settings := looseBlobSettings()
	svc, db := newBlobTestSvc(t, settings)
	audit := repository.NewAuditLogRepository(db)
	cleaner := NewDeliveryBlobCleaner(svc, audit)

	// blob A：被 completed（终态）单引用 → 可清；blob B：被 rolling（活动）单引用 → 保护。
	contentA, contentB := []byte("terminal-referenced"), []byte("active-referenced")
	shaA, shaB := shaOf(contentA), shaOf(contentB)
	mustStore(t, svc, shaA, contentA)
	mustStore(t, svc, shaB, contentB)
	seedOrderWithFileItem(t, db, model.ChangeOrderStatusCompleted, shaA)
	seedOrderWithFileItem(t, db, model.ChangeOrderStatusRolling, shaB)
	// 两 blob 的 last_referenced_at 都推到保留期之外。
	old := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if err := db.Model(&model.DeliveryBlob{}).Where("1 = 1").Update("last_referenced_at", old).Error; err != nil {
		t.Fatalf("回拨引用时间失败: %v", err)
	}

	cleaner.SweepOnce()

	if _, err := svc.Head(shaA); !errors.Is(err, apperr.ErrDeliveryBlobNotFound) {
		t.Fatalf("终态单引用且超期的 blob A 应被清理，实际 %v", err)
	}
	if _, err := svc.Head(shaB); err != nil {
		t.Fatalf("活动单引用的 blob B 应被保护，实际被删 %v", err)
	}
	// 有删除 → 记一条系统清理审计。
	var auditCount int64
	db.Model(&model.AuditLog{}).Where("action = ?", model.ActionDeliveryOrderBlobCleanup).Count(&auditCount)
	if auditCount != 1 {
		t.Fatalf("应记 1 条 blob 清理审计，实际 %d", auditCount)
	}
}

// deleteBlobFile 删除 blob 的磁盘文件（模拟元数据与磁盘不一致）。
func deleteBlobFile(svc *DeliveryBlobService, sha string) error {
	return os.Remove(svc.blobPath(sha))
}

func mustStore(t *testing.T, svc *DeliveryBlobService, sha string, content []byte) {
	t.Helper()
	if err := svc.Store(sha, int64(len(content)), bytes.NewReader(content)); err != nil {
		t.Fatalf("上传 %s 失败: %v", sha[:8], err)
	}
}

// seedOrderWithFileItem 建一张指定状态的单 + 一个引用该 sha 的 file_diff 变更项。
func seedOrderWithFileItem(t *testing.T, db *gorm.DB, status, sha string) {
	t.Helper()
	order := model.ChangeOrder{NamespaceID: 1, Title: "t", Status: status, SourceServerID: "src"}
	if err := db.Create(&order).Error; err != nil {
		t.Fatalf("建单失败: %v", err)
	}
	shaCopy := sha
	action := model.ChangeItemActionAdd
	path := "plugins/x/" + sha[:6] + ".jar"
	if err := db.Create(&model.ChangeOrderItem{
		OrderID: order.ID, Kind: model.ChangeItemKindFileDiff,
		Path: &path, Action: &action, SHA256: &shaCopy,
	}).Error; err != nil {
		t.Fatalf("建变更项失败: %v", err)
	}
}
