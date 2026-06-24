package service

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/wcpe/Beacon/internal/apperr"
	"github.com/wcpe/Beacon/internal/model"
	"github.com/wcpe/Beacon/internal/repository"
)

// seedAuditDetail 追加一条带指定 detail 的审计（导出测试播种）。
func seedAuditDetail(t *testing.T, db *gorm.DB, operator, detail string, at time.Time) {
	t.Helper()
	if err := db.Create(&model.AuditLog{
		NamespaceCode: "prod", Operator: operator, Action: model.ActionConfigPublish,
		TargetType: model.TargetTypeConfig, TargetRef: "prod/__GLOBAL__/app.yml@global:",
		Detail: detail, Result: model.ResultOK, CreatedAt: at,
	}).Error; err != nil {
		t.Fatalf("写审计失败: %v", err)
	}
}

// TestExportCSV 验证 FR-84：Export(csv) 输出表头 + 全量命中行（时间倒序），复用过滤（含 detailKeyword）。
func TestExportCSV(t *testing.T) {
	svc, db := newAuditAnalyticsTestService(t)
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seedAuditDetail(t, db, "alice", `{"dataId":"mysql.yml"}`, base)
	seedAuditDetail(t, db, "bob", `{"dataId":"redis.yml"}`, base.Add(time.Minute))

	var buf bytes.Buffer
	if err := svc.Export(repository.AuditFilter{DetailKeyword: "mysql"}, "csv", &buf); err != nil {
		t.Fatalf("Export csv 失败: %v", err)
	}
	records, err := csv.NewReader(strings.NewReader(buf.String())).ReadAll()
	if err != nil {
		t.Fatalf("解析 CSV 失败: %v", err)
	}
	// 表头 + 1 条命中（detailKeyword=mysql 仅 alice）
	if len(records) != 2 {
		t.Fatalf("CSV 应表头 + 1 行，实际 %d 行：%v", len(records), records)
	}
	if records[0][0] != "id" {
		t.Fatalf("CSV 首列表头应为 id，实际 %q", records[0][0])
	}
	if !strings.Contains(strings.Join(records[1], ","), "alice") {
		t.Fatalf("CSV 数据行应含 alice，实际 %v", records[1])
	}
}

// TestExportJSON 验证 FR-84：Export(json) 输出 JSON 数组（全量命中），字段为小驼峰对外视图。
func TestExportJSON(t *testing.T) {
	svc, db := newAuditAnalyticsTestService(t)
	base := time.Date(2026, 6, 19, 10, 0, 0, 0, time.UTC)
	seedAuditDetail(t, db, "alice", "d1", base)
	seedAuditDetail(t, db, "bob", "d2", base.Add(time.Minute))

	var buf bytes.Buffer
	if err := svc.Export(repository.AuditFilter{}, "json", &buf); err != nil {
		t.Fatalf("Export json 失败: %v", err)
	}
	var arr []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil {
		t.Fatalf("解析 JSON 失败: %v（原文 %s）", err, buf.String())
	}
	if len(arr) != 2 {
		t.Fatalf("JSON 应 2 条，实际 %d", len(arr))
	}
	// 时间倒序：首条应为 bob（较晚）
	if arr[0]["operator"] != "bob" {
		t.Fatalf("JSON 首条应为最新 bob，实际 %v", arr[0]["operator"])
	}
	if _, ok := arr[0]["createdAt"]; !ok {
		t.Fatalf("JSON 条目应含 createdAt 字段：%v", arr[0])
	}
}

// TestExportInvalidFormat 验证 FR-84：非 csv/json 的 format → ErrInvalidParam（handler 转 400）。
func TestExportInvalidFormat(t *testing.T) {
	svc, _ := newAuditAnalyticsTestService(t)
	var buf bytes.Buffer
	err := svc.Export(repository.AuditFilter{}, "xml", &buf)
	if !errors.Is(err, apperr.ErrInvalidParam) {
		t.Fatalf("非法 format 应返回 ErrInvalidParam，实际 %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("非法 format 不应写出任何内容，实际写了 %d 字节", buf.Len())
	}
}
