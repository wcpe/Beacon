package service

import (
	"encoding/json"
	"time"

	"github.com/wcpe/Beacon/apps/server/internal/model"
)

// 归档域对外视图（与 packages/contracts archive.ts 逐字对齐，供 P6b handler 直接序列化）。

// ArchiveTargetView 归档目标（overview.target）。
type ArchiveTargetView struct {
	Mode      string `json:"mode"` // same-instance / external
	Database  string `json:"database"`
	DSNMasked string `json:"dsnMasked"`
	Reachable bool   `json:"reachable"`
}

// ArchiveJobBriefView 域最近一次任务摘要（overview.domains[].lastJob）。
type ArchiveJobBriefView struct {
	ID         uint    `json:"id"`
	Mode       string  `json:"mode"`
	Status     string  `json:"status"`
	FinishedAt *string `json:"finishedAt"`
}

// ArchiveDomainOverviewView 归档总览的域行（overview.domains[]）。
type ArchiveDomainOverviewView struct {
	Domain        string               `json:"domain"`
	RetentionDays int                  `json:"retentionDays"`
	HotRows       int64                `json:"hotRows"`
	ArchiveRows   int64                `json:"archiveRows"`
	ExpiredRows   int64                `json:"expiredRows"`
	LastJob       *ArchiveJobBriefView `json:"lastJob"`
}

// ArchiveOverviewView 归档总览响应（GET /admin/v2/archive/overview）。
type ArchiveOverviewView struct {
	Target  ArchiveTargetView           `json:"target"`
	Domains []ArchiveDomainOverviewView `json:"domains"`
}

// ArchiveJobView 归档任务对外视图。
type ArchiveJobView struct {
	ID         uint     `json:"id"`
	Mode       string   `json:"mode"`
	Trigger    string   `json:"trigger"`
	Status     string   `json:"status"`
	Domains    []string `json:"domains"`
	Operator   string   `json:"operator"`
	Error      *string  `json:"error"`
	StartedAt  *string  `json:"startedAt"`
	FinishedAt *string  `json:"finishedAt"`
	CreatedAt  string   `json:"createdAt"`
}

// ArchiveJobItemView 任务工作项对外视图。
type ArchiveJobItemView struct {
	ID                uint    `json:"id"`
	Domain            string  `json:"domain"`
	TableName         string  `json:"tableName"`
	RangeTo           *string `json:"rangeTo"`
	Phase             string  `json:"phase"`
	Cursor            *string `json:"cursor"`
	RowsExpected      int64   `json:"rowsExpected"`
	RowsCopied        int64   `json:"rowsCopied"`
	RowsDeleted       int64   `json:"rowsDeleted"`
	VerifyRowsHot     *int64  `json:"verifyRowsHot"`
	VerifyRowsArchive *int64  `json:"verifyRowsArchive"`
	VerifySampleSize  *int    `json:"verifySampleSize"`
	VerifyHashHot     *string `json:"verifyHashHot"`
	VerifyHashArchive *string `json:"verifyHashArchive"`
	VerifyPassed      *bool   `json:"verifyPassed"`
	Error             *string `json:"error"`
}

// ArchiveJobDetailView 任务详情（job 全字段 + items）。
type ArchiveJobDetailView struct {
	ArchiveJobView
	Items []ArchiveJobItemView `json:"items"`
}

// ArchiveJobListView 任务列表分页响应（{items,total}）。
type ArchiveJobListView struct {
	Items []ArchiveJobView `json:"items"`
	Total int64            `json:"total"`
}

// toArchiveJobView 把任务模型转对外视图（domains 解析 json、空字符串字段转 null、时间转 RFC3339）。
func toArchiveJobView(job model.ArchiveJob) ArchiveJobView {
	return ArchiveJobView{
		ID: job.ID, Mode: job.Mode, Trigger: job.Trigger, Status: job.Status,
		Domains:    parseDomainList(job.Domains),
		Operator:   job.Operator,
		Error:      strPtrOrNil(job.Error),
		StartedAt:  timePtrToStr(job.StartedAt),
		FinishedAt: timePtrToStr(job.FinishedAt),
		CreatedAt:  job.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// toArchiveItemView 把工作项模型转对外视图。
func toArchiveItemView(item model.ArchiveJobItem) ArchiveJobItemView {
	return ArchiveJobItemView{
		ID: item.ID, Domain: item.Domain, TableName: item.TargetTable,
		RangeTo:           timePtrToStr(item.RangeTo),
		Phase:             item.Phase,
		Cursor:            strPtrOrNil(item.Cursor),
		RowsExpected:      item.RowsExpected,
		RowsCopied:        item.RowsCopied,
		RowsDeleted:       item.RowsDeleted,
		VerifyRowsHot:     item.VerifyRowsHot,
		VerifyRowsArchive: item.VerifyRowsArchive,
		VerifySampleSize:  item.VerifySampleSize,
		VerifyHashHot:     strPtrOrNil(item.VerifyHashHot),
		VerifyHashArchive: strPtrOrNil(item.VerifyHashArchive),
		VerifyPassed:      item.VerifyPassed,
		Error:             strPtrOrNil(item.Error),
	}
}

// toArchiveJobBrief 把任务模型转域摘要（overview.lastJob）。
func toArchiveJobBrief(job model.ArchiveJob) ArchiveJobBriefView {
	return ArchiveJobBriefView{
		ID: job.ID, Mode: job.Mode, Status: job.Status,
		FinishedAt: timePtrToStr(job.FinishedAt),
	}
}

// parseDomainList 解析 domains json 文本为字符串切片（空 / 解析失败恒返回非 nil 空切片，序列化为 []）。
func parseDomainList(raw string) []string {
	out := []string{}
	if raw == "" {
		return out
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// strPtrOrNil 空串转 nil（供对外 null 语义），非空取地址。
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	v := s
	return &v
}

// timePtrToStr 可空时间转 *RFC3339 字符串（nil 保持 nil）。
func timePtrToStr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
