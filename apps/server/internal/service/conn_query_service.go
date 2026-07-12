package service

import (
	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// connStatsBucketMs1m / connStatsBucketMs5m 是连接流时间桶粒度（spec §5.2 bucket:1m|5m）。
const (
	connStatsBucketMs1m = 60_000
	connStatsBucketMs5m = 5 * 60_000
	// maxStatsBuckets 单次 stats 响应桶数上限（防超大窗口 × 细粒度产生过多桶，对齐 devmock 300）。
	maxStatsBuckets = 300
)

// ConnQueryService 是连接明细的管理面查询服务（FR-145，见 spec §5.2）：
// 精确 connId 直查 / 条件游标分页 / 时间桶聚合。只读、查询侧不隐式建日表；条件查询强制查询防护（spec §4.3）。
type ConnQueryService struct {
	repo *repository.ConnDetailRepository
}

// NewConnQueryService 构造查询服务。
func NewConnQueryService(repo *repository.ConnDetailRepository) *ConnQueryService {
	return &ConnQueryService{repo: repo}
}

// ListConnectionsParams 是连接列表查询入参（connId 直查免防护；否则走 §4.3 条件查询防护）。
type ListConnectionsParams struct {
	ConnID      string
	ServerID    string
	PlayerUUID  string
	Status      string
	CloseKind   string
	NamespaceID uint
	FromMs      int64
	ToMs        int64
	Cursor      int
	Limit       int
}

// ConnPage 是连接明细游标分页结果（NextCursor 空串表示无下一页，handler 映射为 null）。
type ConnPage struct {
	Items      []model.ConnDetail
	NextCursor string
}

// List 查询连接明细：connId 精确直查（免时间范围/过滤），否则校验查询防护后跨日并表游标分页（opened_at 降序）。
func (s *ConnQueryService) List(p ListConnectionsParams) (ConnPage, error) {
	if p.ConnID != "" {
		row, err := s.repo.FindByConnID(p.ConnID)
		if err != nil {
			return ConnPage{}, err
		}
		items := make([]model.ConnDetail, 0, 1)
		if row != nil {
			items = append(items, *row)
		}
		return ConnPage{Items: items, NextCursor: ""}, nil
	}
	if err := validateRangeFilter(p.ServerID != "" || p.PlayerUUID != "", p.FromMs, p.ToMs); err != nil {
		return ConnPage{}, err
	}
	limit := clampLimit(p.Limit)
	offset := clampOffset(p.Cursor)
	rows, hasMore, err := s.repo.QueryConnections(repository.ConnQuery{
		ServerID: p.ServerID, PlayerUUID: p.PlayerUUID, Status: p.Status,
		CloseKind: p.CloseKind, NamespaceID: p.NamespaceID,
		FromMs: p.FromMs, ToMs: p.ToMs, Offset: offset, Limit: limit,
	})
	if err != nil {
		return ConnPage{}, err
	}
	return ConnPage{Items: rows, NextCursor: nextCursorOf(offset, limit, hasMore)}, nil
}

// Detail 按 connId 查单条连接：conn_id 内嵌时间直定日表，未命中 404 connection_not_found。
func (s *ConnQueryService) Detail(connID string) (model.ConnDetail, error) {
	if connID == "" || len(connID) > 36 {
		return model.ConnDetail{}, apperr.ErrInvalidParam
	}
	row, err := s.repo.FindByConnID(connID)
	if err != nil {
		return model.ConnDetail{}, err
	}
	if row == nil {
		return model.ConnDetail{}, apperr.ErrConnectionNotFound
	}
	return *row, nil
}

// ConnStatsParams 是连接流时间桶聚合入参（serverId 可空按 proxy 过滤；bucket 仅 1m/5m）。
type ConnStatsParams struct {
	ServerID string
	FromMs   int64
	ToMs     int64
	Bucket   string // 1m / 5m（非法回退 1m）
}

// ConnStatsBucket 是一个时间桶的连接流聚合（对齐 contracts ConnStatsBucket）。
type ConnStatsBucket struct {
	StartMs        int64
	Opens          int
	Closes         int
	AbnormalCloses int
	EstimatedOpen  int
}

// Stats 聚合连接流时间桶（open/close 数、异常断开数、存量估算）。
// 宽松语义（不 400）：窗口非法回空、跨度超 168h 收敛到 168h、桶数超上限截断；只扫窗口命中日表（spec §4.5）。
func (s *ConnQueryService) Stats(p ConnStatsParams) ([]ConnStatsBucket, error) {
	bucketMs := int64(connStatsBucketMs1m)
	if p.Bucket == "5m" {
		bucketMs = connStatsBucketMs5m
	}
	fromMs, toMs := p.FromMs, p.ToMs
	if fromMs <= 0 || toMs <= 0 || fromMs >= toMs {
		return []ConnStatsBucket{}, nil
	}
	if toMs-fromMs > maxConnMsgRangeMs {
		fromMs = toMs - maxConnMsgRangeMs
	}
	rows, err := s.repo.ScanConnStats(p.ServerID, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	return bucketConnStats(rows, fromMs, toMs, bucketMs), nil
}

// bucketConnStats 在 Go 侧把连接会话投影按时间桶聚合（禁方言日期函数，保可移植；口径对齐 devmock）：
// opens=桶内建立、closes=桶内断开、abnormalCloses=桶内异常断开（timeout/error）、estimatedOpen=桶末仍在线存量。
func bucketConnStats(rows []repository.ConnStatRow, fromMs, toMs, bucketMs int64) []ConnStatsBucket {
	count := int((toMs - fromMs) / bucketMs)
	if count > maxStatsBuckets {
		count = maxStatsBuckets
	}
	buckets := make([]ConnStatsBucket, 0, count)
	for i := 0; i < count; i++ {
		start := fromMs + int64(i)*bucketMs
		end := start + bucketMs
		b := ConnStatsBucket{StartMs: start}
		for j := range rows {
			openedMs := rows[j].OpenedAt.UnixMilli()
			if openedMs >= start && openedMs < end {
				b.Opens++
			}
			if rows[j].ClosedAt != nil {
				closedMs := rows[j].ClosedAt.UnixMilli()
				if closedMs >= start && closedMs < end {
					b.Closes++
					if rows[j].CloseKind == model.ConnCloseKindTimeout || rows[j].CloseKind == model.ConnCloseKindError {
						b.AbnormalCloses++
					}
				}
			}
			if openedMs <= end && (rows[j].ClosedAt == nil || rows[j].ClosedAt.UnixMilli() > end) {
				b.EstimatedOpen++
			}
		}
		buckets = append(buckets, b)
	}
	return buckets
}
