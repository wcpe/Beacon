package repository

import (
	"errors"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// newDeliveryTestDB 打开内存 sqlite 并迁移交付编排五张表，供仓库单测（不依赖 MySQL/DSN）。
// TranslateError 打开以便唯一约束冲突翻译为 gorm.ErrDuplicatedKey（此处只断言出错、不细分类型）。
func newDeliveryTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger:         logger.Default.LogMode(logger.Silent),
		TranslateError: true,
	})
	if err != nil {
		t.Fatalf("打开内存 sqlite 失败: %v", err)
	}
	if err := db.AutoMigrate(
		&model.ChangeOrder{}, &model.ChangeOrderItem{},
		&model.ChangeBatch{}, &model.ChangeTarget{}, &model.DeliveryBlob{}, &model.DeliveryConfigArtifact{},
	); err != nil {
		t.Fatalf("迁移交付编排表失败: %v", err)
	}
	// 清表隔离（shared cache 下多测试共享同一内存库）。
	for _, tbl := range []string{"change_order", "change_order_item", "change_batch", "change_target", "delivery_blob", "delivery_config_artifact"} {
		if err := db.Exec("DELETE FROM " + tbl).Error; err != nil {
			t.Fatalf("清表 %s 失败: %v", tbl, err)
		}
	}
	return db
}

// errForceRollback 是仅用于触发事务回滚的哨兵错误。
var errForceRollback = errors.New("强制回滚")

// ptrOf 返回入参的指针，简化可空字段构造。
func ptrOf[T any](v T) *T { return &v }

// mkOrder 构造一条最小合法变更单（填齐 not-null 业务字段）。
func mkOrder(ns uint, status string) *model.ChangeOrder {
	return &model.ChangeOrder{
		NamespaceID:      ns,
		Title:            "发布 area1 插件",
		Status:           status,
		BatchMode:        model.BatchModePercent,
		BatchSizes:       "[5,20,75]",
		ActivationMethod: model.ActivationMethodRestart,
		PayloadState:     model.PayloadStatePending,
		CreatedBy:        "admin",
	}
}

// TestChangeOrderCreateAndFindByID 建后可按主键查回，字段往返一致；不存在返回 (nil,nil)。
func TestChangeOrderCreateAndFindByID(t *testing.T) {
	repo := NewChangeOrderRepository(newDeliveryTestDB(t))
	order := mkOrder(1, model.ChangeOrderStatusDraft)
	if err := repo.Create(order); err != nil {
		t.Fatalf("建变更单失败: %v", err)
	}
	if order.ID == 0 {
		t.Fatal("建后应回填自增 ID")
	}

	got, err := repo.FindByID(order.ID)
	if err != nil || got == nil {
		t.Fatalf("按 ID 查变更单失败: %v / %v", err, got)
	}
	if got.Title != "发布 area1 插件" || got.Status != model.ChangeOrderStatusDraft ||
		got.NamespaceID != 1 || got.CreatedBy != "admin" {
		t.Fatalf("变更单字段不一致: %+v", got)
	}
	// not-null INT 默认值随 DDL 落库（observe=120 / activate=300）。
	if got.ObserveWindowSec != 120 || got.ActivateTimeoutSec != 300 {
		t.Fatalf("观察窗 / 生效超时默认值不符: observe=%d activate=%d", got.ObserveWindowSec, got.ActivateTimeoutSec)
	}

	miss, err := repo.FindByID(99999)
	if err != nil || miss != nil {
		t.Fatalf("不存在变更单应返回 (nil,nil)，实际 %v / %v", miss, err)
	}
}

// TestChangeOrderList 分页与过滤：按 namespace / status 过滤，分页切片，总数正确。
func TestChangeOrderList(t *testing.T) {
	repo := NewChangeOrderRepository(newDeliveryTestDB(t))
	// ns=1 三条（两 draft 一 rolling），ns=2 一条 draft（不应串到 ns=1 查询）。
	for _, o := range []*model.ChangeOrder{
		mkOrder(1, model.ChangeOrderStatusDraft),
		mkOrder(1, model.ChangeOrderStatusDraft),
		mkOrder(1, model.ChangeOrderStatusRolling),
		mkOrder(2, model.ChangeOrderStatusDraft),
	} {
		if err := repo.Create(o); err != nil {
			t.Fatalf("建单失败: %v", err)
		}
	}

	// ns=1 全部：total=3。
	items, total, err := repo.List(ChangeOrderListQuery{NamespaceID: 1, Page: 1, Size: 10})
	if err != nil {
		t.Fatalf("List 失败: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("ns=1 应 total=3 且当页 3 条，实际 total=%d len=%d", total, len(items))
	}

	// ns=1 + status=draft：total=2。
	items, total, err = repo.List(ChangeOrderListQuery{NamespaceID: 1, Status: model.ChangeOrderStatusDraft, Page: 1, Size: 10})
	if err != nil {
		t.Fatalf("List 过滤失败: %v", err)
	}
	if total != 2 || len(items) != 2 {
		t.Fatalf("ns=1 draft 应 total=2，实际 total=%d len=%d", total, len(items))
	}

	// 分页：size=2，第 1 页 2 条 / 第 2 页 1 条，total 仍为 3。
	page1, total, err := repo.List(ChangeOrderListQuery{NamespaceID: 1, Page: 1, Size: 2})
	if err != nil || total != 3 || len(page1) != 2 {
		t.Fatalf("第 1 页应 2 条 total=3，实际 total=%d len=%d err=%v", total, len(page1), err)
	}
	page2, _, err := repo.List(ChangeOrderListQuery{NamespaceID: 1, Page: 2, Size: 2})
	if err != nil || len(page2) != 1 {
		t.Fatalf("第 2 页应 1 条，实际 len=%d err=%v", len(page2), err)
	}

	// 创建人 + 标题关键字过滤。
	byCreator, total, err := repo.List(ChangeOrderListQuery{NamespaceID: 1, CreatedBy: "admin", Page: 1, Size: 10})
	if err != nil || total != 3 || len(byCreator) != 3 {
		t.Fatalf("创建人过滤应命中 3 条，实际 total=%d err=%v", total, err)
	}
	_, total, err = repo.List(ChangeOrderListQuery{NamespaceID: 1, Keyword: "插件", Page: 1, Size: 10})
	if err != nil || total != 3 {
		t.Fatalf("关键字过滤应命中 3 条，实际 total=%d err=%v", total, err)
	}
	_, total, err = repo.List(ChangeOrderListQuery{NamespaceID: 1, Keyword: "不存在的标题", Page: 1, Size: 10})
	if err != nil || total != 0 {
		t.Fatalf("无命中关键字应 total=0，实际 total=%d err=%v", total, err)
	}
}

// TestChangeOrderWithTx 事务副本用的是事务连接：回滚后写入不落库。
// 若 WithTx 误返回基础 db，则事务内的写会自动提交、回滚后仍可见，测试即失败。
func TestChangeOrderWithTx(t *testing.T) {
	db := newDeliveryTestDB(t)
	repo := NewChangeOrderRepository(db)

	// 提交事务：落库。
	if err := db.Transaction(func(tx *gorm.DB) error {
		return repo.WithTx(tx).Create(mkOrder(1, model.ChangeOrderStatusDraft))
	}); err != nil {
		t.Fatalf("事务提交失败: %v", err)
	}
	_, total, err := repo.List(ChangeOrderListQuery{NamespaceID: 1, Page: 1, Size: 10})
	if err != nil || total != 1 {
		t.Fatalf("提交后应有 1 条，实际 total=%d err=%v", total, err)
	}

	// 回滚事务：不落库。
	_ = db.Transaction(func(tx *gorm.DB) error {
		if e := repo.WithTx(tx).Create(mkOrder(1, model.ChangeOrderStatusDraft)); e != nil {
			return e
		}
		return errForceRollback
	})
	_, total, err = repo.List(ChangeOrderListQuery{NamespaceID: 1, Page: 1, Size: 10})
	if err != nil || total != 1 {
		t.Fatalf("回滚后仍应只有 1 条，实际 total=%d err=%v", total, err)
	}
}

// TestChangeOrderItemUniqueConstraints 校验变更项两条唯一索引靠 NULL 互不相等而互不干扰：
// 文件项按 (order,kind,path) 唯一、配置项按 (order,kind,scope_kind,scope_id) 唯一。
func TestChangeOrderItemUniqueConstraints(t *testing.T) {
	db := newDeliveryTestDB(t)

	// 文件项：同 (order,path) 重复被挡，异 path 放行。
	fileA := &model.ChangeOrderItem{
		OrderID: 1, Kind: model.ChangeItemKindFileDiff,
		Path: ptrOf("plugins/a.yml"), Action: ptrOf(model.ChangeItemActionUpdate),
		SHA256: ptrOf("aa"), SizeBytes: ptrOf(int64(10)),
	}
	if err := db.Create(fileA).Error; err != nil {
		t.Fatalf("首个文件项应建成功: %v", err)
	}
	dupFile := &model.ChangeOrderItem{
		OrderID: 1, Kind: model.ChangeItemKindFileDiff, Path: ptrOf("plugins/a.yml"),
	}
	if err := db.Create(dupFile).Error; err == nil {
		t.Fatal("同 (order,kind,path) 文件项应被唯一索引挡下")
	}
	fileB := &model.ChangeOrderItem{
		OrderID: 1, Kind: model.ChangeItemKindFileDiff, Path: ptrOf("plugins/b.yml"),
	}
	if err := db.Create(fileB).Error; err != nil {
		t.Fatalf("异 path 文件项应放行（证 path 非空互不相等）: %v", err)
	}

	// 配置项：同 (order,scope) 重复被挡，异 scope 放行；两配置项 path 均 NULL 不触发文件唯一索引。
	cfgA := &model.ChangeOrderItem{
		OrderID: 1, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: ptrOf(model.ConfigScopeRegion), ConfigScopeID: ptrOf(uint(3)),
		ConfigFromVersionID: ptrOf(uint(7)), ConfigToVersionID: ptrOf(uint(8)),
	}
	if err := db.Create(cfgA).Error; err != nil {
		t.Fatalf("首个配置项应建成功: %v", err)
	}
	cfgB := &model.ChangeOrderItem{
		OrderID: 1, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: ptrOf(model.ConfigScopeZone), ConfigScopeID: ptrOf(uint(5)),
	}
	if err := db.Create(cfgB).Error; err != nil {
		t.Fatalf("异 scope 配置项应放行（证 path NULL 互不相等、不撞文件唯一索引）: %v", err)
	}
	dupCfg := &model.ChangeOrderItem{
		OrderID: 1, Kind: model.ChangeItemKindConfigChange,
		ConfigScopeKind: ptrOf(model.ConfigScopeRegion), ConfigScopeID: ptrOf(uint(3)),
	}
	if err := db.Create(dupCfg).Error; err == nil {
		t.Fatal("同 (order,kind,scope_kind,scope_id) 配置项应被唯一索引挡下")
	}
}

// TestChangeBatchUnique 校验批次 (order_id, batch_no) 唯一。
func TestChangeBatchUnique(t *testing.T) {
	db := newDeliveryTestDB(t)
	first := &model.ChangeBatch{OrderID: 1, BatchNo: 1, Status: model.ChangeBatchStatusPending}
	if err := db.Create(first).Error; err != nil {
		t.Fatalf("首个批次应建成功: %v", err)
	}
	if err := db.Create(&model.ChangeBatch{OrderID: 1, BatchNo: 1, Status: model.ChangeBatchStatusPending}).Error; err == nil {
		t.Fatal("同 (order_id, batch_no) 批次应被唯一约束挡下")
	}
	if err := db.Create(&model.ChangeBatch{OrderID: 1, BatchNo: 2, Status: model.ChangeBatchStatusPending}).Error; err != nil {
		t.Fatalf("异 batch_no 应放行: %v", err)
	}
}

// TestChangeTargetUnique 校验目标 (order_id, server_id) 唯一。
func TestChangeTargetUnique(t *testing.T) {
	db := newDeliveryTestDB(t)
	first := &model.ChangeTarget{OrderID: 1, BatchID: 1, ServerID: "lobby-1", Status: model.ChangeTargetStatusPending}
	if err := db.Create(first).Error; err != nil {
		t.Fatalf("首个目标应建成功: %v", err)
	}
	if err := db.Create(&model.ChangeTarget{OrderID: 1, BatchID: 2, ServerID: "lobby-1", Status: model.ChangeTargetStatusPending}).Error; err == nil {
		t.Fatal("同 (order_id, server_id) 目标应被唯一约束挡下")
	}
	if err := db.Create(&model.ChangeTarget{OrderID: 1, BatchID: 1, ServerID: "lobby-2", Status: model.ChangeTargetStatusPending}).Error; err != nil {
		t.Fatalf("异 server_id 应放行: %v", err)
	}
}

// TestDeliveryTableNames 校验五张表 TableName 返回值（单数 snake_case）。
func TestDeliveryTableNames(t *testing.T) {
	if got := (model.ChangeOrder{}).TableName(); got != "change_order" {
		t.Errorf("change_order 表名错误：%s", got)
	}
	if got := (model.ChangeOrderItem{}).TableName(); got != "change_order_item" {
		t.Errorf("change_order_item 表名错误：%s", got)
	}
	if got := (model.ChangeBatch{}).TableName(); got != "change_batch" {
		t.Errorf("change_batch 表名错误：%s", got)
	}
	if got := (model.ChangeTarget{}).TableName(); got != "change_target" {
		t.Errorf("change_target 表名错误：%s", got)
	}
	if got := (model.DeliveryBlob{}).TableName(); got != "delivery_blob" {
		t.Errorf("delivery_blob 表名错误：%s", got)
	}
}
