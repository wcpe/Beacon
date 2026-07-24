package repository

import (
	"testing"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// mkBlob 构造一条最小合法 blob 元数据。
func mkBlob(sha string, size int64, state string) *model.DeliveryBlob {
	return &model.DeliveryBlob{
		SHA256:           sha,
		SizeBytes:        size,
		State:            state,
		LastReferencedAt: time.Now().UTC(),
	}
}

// TestDeliveryBlobCreateAndFindBySHA256 建后可按 sha256 查回，字段往返一致；不存在返回 (nil,nil)。
func TestDeliveryBlobCreateAndFindBySHA256(t *testing.T) {
	repo := NewDeliveryBlobRepository(newDeliveryTestDB(t))
	blob := mkBlob("a1b2", 4096, model.DeliveryBlobStateReady)
	if err := repo.Create(blob); err != nil {
		t.Fatalf("建 blob 失败: %v", err)
	}

	got, err := repo.FindBySHA256("a1b2")
	if err != nil || got == nil {
		t.Fatalf("按 sha256 查 blob 失败: %v / %v", err, got)
	}
	if got.SizeBytes != 4096 || got.State != model.DeliveryBlobStateReady {
		t.Fatalf("blob 字段不一致: %+v", got)
	}

	miss, err := repo.FindBySHA256("nope")
	if err != nil || miss != nil {
		t.Fatalf("不存在 blob 应返回 (nil,nil)，实际 %v / %v", miss, err)
	}
}

// TestDeliveryBlobSHA256Unique sha256 为内容寻址主键，重复插入被挡。
func TestDeliveryBlobSHA256Unique(t *testing.T) {
	repo := NewDeliveryBlobRepository(newDeliveryTestDB(t))
	if err := repo.Create(mkBlob("dup", 1, model.DeliveryBlobStateUploading)); err != nil {
		t.Fatalf("首个 blob 应建成功: %v", err)
	}
	if err := repo.Create(mkBlob("dup", 2, model.DeliveryBlobStateReady)); err == nil {
		t.Fatal("同 sha256 blob 应被主键唯一性挡下")
	}
}

// TestDeliveryBlobList 分页查询与总数。
func TestDeliveryBlobList(t *testing.T) {
	repo := NewDeliveryBlobRepository(newDeliveryTestDB(t))
	for _, sha := range []string{"s1", "s2", "s3"} {
		if err := repo.Create(mkBlob(sha, 10, model.DeliveryBlobStateReady)); err != nil {
			t.Fatalf("建 blob 失败: %v", err)
		}
	}
	items, total, err := repo.List(1, 2)
	if err != nil || total != 3 || len(items) != 2 {
		t.Fatalf("应 total=3 且当页 2 条，实际 total=%d len=%d err=%v", total, len(items), err)
	}
	page2, _, err := repo.List(2, 2)
	if err != nil || len(page2) != 1 {
		t.Fatalf("第 2 页应 1 条，实际 len=%d err=%v", len(page2), err)
	}
}
