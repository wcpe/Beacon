package service

import (
	"log/slog"
	"sync/atomic"

	"github.com/wcpe/Beacon/apps/server/internal/apperr"
	"github.com/wcpe/Beacon/apps/server/internal/model"
	"github.com/wcpe/Beacon/apps/server/internal/repository"
)

// RouteKindMCPInvocation 是 MCP 工具调用流水在异步日表写入通道中的路由键（FR-240，spec §3.5）。
const RouteKindMCPInvocation = "mcp_invocation"

// MCP 工具调用流水查询的服务端规整参数（spec §3.7）。
const (
	// mcpInvocationDefaultLimit / mcpInvocationMaxLimit 列表分页大小默认与上限（≤0 取默认、>100 取 100）。
	mcpInvocationDefaultLimit = 20
	mcpInvocationMaxLimit     = 100
)

// MCPInvocationService 是 MCP 工具调用流水的写入方与查询服务（FR-240）。
//
// 写入不阻塞主路径（spec §3.5）：Record 只做「打包一行 + 非阻塞入队」，DB IO 全在后台写入协程；
// 队列满即丢弃该行并 WARN（携带累计丢弃数），**绝不阻塞、绝不让调用失败**。
type MCPInvocationService struct {
	// Writer 是泛化异步日表写入通道（须已注册 RouteKindMCPInvocation 路由）。
	Writer *AsyncDailyWriter
	repo   *repository.MCPInvocationRepository
	// dropped 累计入队失败（队列满 / 路由未注册）丢弃的行数，供日志与自观测（原子读写，请求路径只加不减）。
	dropped int64
}

// NewMCPInvocationService 构造流水服务：writer 供写入（可为 nil，此时 Record 记一次 WARN 后丢弃），
// repo 供列表与详情查询。
func NewMCPInvocationService(writer *AsyncDailyWriter, repo *repository.MCPInvocationRepository) *MCPInvocationService {
	return &MCPInvocationService{Writer: writer, repo: repo}
}

// Record 实现 server.MCPInvocationRecorder：非阻塞投递一行流水到异步写入通道。
//
// 队列满 / 路由未注册（EnqueueRows 返回 false）→ 丢弃该行 + WARN（含累计丢弃数），不改调用结果。
func (s *MCPInvocationService) Record(row model.MCPInvocation) {
	if s == nil {
		return
	}
	// Writer 为 nil（未装配 / 通道未就绪）与队列满同语义：丢弃该行 + WARN，绝不让调用失败。
	if s.Writer == nil || !EnqueueRows(s.Writer, RouteKindMCPInvocation, []model.MCPInvocation{row}) {
		dropped := atomic.AddInt64(&s.dropped, 1)
		slog.Warn("MCP 工具调用流水入队失败，已丢弃该行",
			"工具", row.ToolName, "客户端", row.ClientID, "结果", row.Result,
			"traceId", row.TraceID, "累计丢弃行数", dropped)
	}
}

// Dropped 返回累计入队失败丢弃的行数（供自观测 / 测试断言）。
func (s *MCPInvocationService) Dropped() int64 {
	if s == nil {
		return 0
	}
	return atomic.LoadInt64(&s.dropped)
}

// mcpInvocationEpochMs 是「不限起始时间」时的下界（1970-01-01，覆盖全部已存在日表）。
const mcpInvocationEpochMs int64 = 0

// mcpInvocationNoUpperBoundMs 是「不限结束时间」时的上界（远未来，仍落在 time.Time 可表达范围内）。
const mcpInvocationNoUpperBoundMs int64 = 4102444800000 // 2100-01-01T00:00:00Z

// MCPInvocationListParams 是流水列表查询入参（spec §3.7；六维过滤 + 游标分页）。
type MCPInvocationListParams struct {
	ToolName  string
	ClientID  string
	Result    string
	RiskLevel string
	Reason    string
	FromMs    int64
	ToMs      int64
	Cursor    int
	Limit     int
}

// MCPInvocationPage 是流水列表的游标分页结果（NextCursor 空串表示无下一页）。
type MCPInvocationPage struct {
	Items      []model.MCPInvocation
	NextCursor string
}

// List 按六维过滤跨日并表分页查询工具调用流水（created_at 降序）。
//
// 枚举维度的非法值一律 400 INVALID_PARAM；from > to 亦 400（spec §3.7）。
// 不接 ObservationScope：MCP 主体是全局机器身份（无 namespace 归属），流水行不可能有权威 namespace。
func (s *MCPInvocationService) List(p MCPInvocationListParams) (MCPInvocationPage, error) {
	if err := validateMCPInvocationQuery(p); err != nil {
		return MCPInvocationPage{}, err
	}
	fromMs, toMs := p.FromMs, p.ToMs
	if fromMs <= 0 {
		fromMs = mcpInvocationEpochMs
	}
	if toMs <= 0 {
		toMs = mcpInvocationNoUpperBoundMs
	}
	limit := clampMCPInvocationLimit(p.Limit)
	offset := clampOffset(p.Cursor)
	rows, hasMore, err := s.repo.QueryInvocations(repository.MCPInvocationQuery{
		ToolName: p.ToolName, ClientID: p.ClientID, Result: p.Result,
		RiskLevel: p.RiskLevel, Reason: p.Reason,
		FromMs: fromMs, ToMs: toMs, Offset: offset, Limit: limit,
	})
	if err != nil {
		return MCPInvocationPage{}, err
	}
	return MCPInvocationPage{Items: rows, NextCursor: nextCursorOf(offset, limit, hasMore)}, nil
}

// Detail 按 invocationId 查单条流水：UUIDv7 内嵌时间直定日表，未命中 / 非法 ID 一律 404 mcp_invocation_not_found
// （不区分「非法」与「不存在」，防探测；已归档日同样 404——冷查询不在本 FR）。
func (s *MCPInvocationService) Detail(invocationID string) (model.MCPInvocation, error) {
	if invocationID == "" || len(invocationID) > 36 {
		return model.MCPInvocation{}, apperr.ErrMCPInvocationNotFound
	}
	row, err := s.repo.FindByInvocationID(invocationID)
	if err != nil {
		return model.MCPInvocation{}, err
	}
	if row == nil {
		return model.MCPInvocation{}, apperr.ErrMCPInvocationNotFound
	}
	return *row, nil
}

// validateMCPInvocationQuery 校验列表查询的枚举维度与时间窗（spec §3.7）。
func validateMCPInvocationQuery(p MCPInvocationListParams) error {
	if p.Result != "" && !model.IsValidMCPInvocationResult(p.Result) {
		return apperr.ErrInvalidParam
	}
	if p.RiskLevel != "" && !model.IsValidMCPInvocationRiskLevel(p.RiskLevel) {
		return apperr.ErrInvalidParam
	}
	if p.Reason != "" && !model.IsValidMCPInvocationReason(p.Reason) {
		return apperr.ErrInvalidParam
	}
	if p.FromMs > 0 && p.ToMs > 0 && p.FromMs > p.ToMs {
		return apperr.ErrInvalidParam
	}
	return nil
}

// clampMCPInvocationLimit 规整流水列表分页大小：≤0 取默认 20，>100 收敛到 100（spec §3.7）。
func clampMCPInvocationLimit(limit int) int {
	if limit <= 0 {
		return mcpInvocationDefaultLimit
	}
	if limit > mcpInvocationMaxLimit {
		return mcpInvocationMaxLimit
	}
	return limit
}
