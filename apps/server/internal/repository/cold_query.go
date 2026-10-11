package repository

import (
	"context"
	"encoding/base64"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 冷查询（includeArchived）应用层归并原语（FR-152，spec §4.4，见 ADR-0066 决策 5）。
//
// 机制：对热连接与归档连接执行**同构查询**（同过滤 / 同 ORDER BY 时间 DESC, id DESC / 同 limit），
// 应用层有序归并取前 N、主键去重保留热侧（归档进行中两侧同存）。禁跨库 JOIN/UNION——热连接读、
// 归档连接读，全在 Go 侧归并（保可移植）。游标令牌编码 (时间, id) 边界，热 / 冷两侧统一应用。

// coldCursor 冷查询 keyset 游标：主时间毫秒 + 全局唯一业务主键 id。
// 对外编码为 base64("<timeMs>|<id>") 作 nextCursor 令牌。
type coldCursor struct {
	TimeMs int64
	ID     string
}

// isZero 判空游标（首页，无边界）。
func (c coldCursor) isZero() bool {
	return c.TimeMs == 0 && c.ID == ""
}

// encode 把游标编码为对外 nextCursor 令牌；空游标返回空串（表示无下一页）。
func (c coldCursor) encode() string {
	if c.isZero() {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.TimeMs, 10) + "|" + c.ID))
}

// decodeColdCursor 解析 nextCursor 令牌为游标；空 / 非法一律回零值（首页），不报错
// （容前端在热 / 冷之间切换时传入的旧格式游标，退化为首页而非 500）。
func decodeColdCursor(token string) coldCursor {
	if token == "" {
		return coldCursor{}
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return coldCursor{}
	}
	s := string(raw)
	i := strings.IndexByte(s, '|')
	if i < 0 {
		return coldCursor{}
	}
	ms, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return coldCursor{}
	}
	return coldCursor{TimeMs: ms, ID: s[i+1:]}
}

// coldLessStringDesc 归并 / 排序比较器（时间降序、同刻按字符串主键降序）：供 UUID / trace_id 等字符串键域。
func coldLessStringDesc(a, b coldCursor) bool {
	if a.TimeMs != b.TimeMs {
		return a.TimeMs > b.TimeMs
	}
	return a.ID > b.ID
}

// coldLessNumericDesc 归并 / 排序比较器（时间降序、同刻按数值主键降序）：供 audit 自增 id 等数值键域。
func coldLessNumericDesc(a, b coldCursor) bool {
	if a.TimeMs != b.TimeMs {
		return a.TimeMs > b.TimeMs
	}
	return coldParseNumericID(a.ID) > coldParseNumericID(b.ID)
}

// coldParseNumericID 把数值主键字符串解析为无符号整数（解析失败按 0，仅用于同刻并列的确定序）。
func coldParseNumericID(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// fetchColdSide 在给定表集合（单表即一元素、日表新→旧）上取至多 want 行，逐表短路凑满即停。
// apply 须已叠加过滤 / 时间窗 / keyset 边界；order 为 "<时间列> DESC, <主键列> DESC"。
// coldQueryArchiveTimeout 是冷查询在**归档侧**单次调用的期限。
//
// 为什么需要它（不可省）：归档连接自 store.OpenArchive 起带**运行期** call 预算（秒级，与主库
// 同口径）——那对「取连接」「小查询」合适，但冷查询是**无 LIMIT 的全区间读**（逐日表扫描、
// 单服行数上限 ≈ archive.cold-query-max-days × 桶数、多服倍增），秒级预算会把「慢查询」变成
// 「不可用查询」，而 v2-hot-cold-archive §4.4 明确向前端承诺「本次查询包含归档库，可能较慢」。
// 归档搬运早已按同一理由自绑长期限（service.archiveLongQueryTimeout），冷查询须对齐。
//
// 为什么绑在 repo 而非装配处：`WithContext` 绑的是**一次性** ctx——若在 main 装配期绑定，
// 进程启动 N 分钟后该 ctx 永久过期，冷查询此后全部失败。期限必须**按次**创建，
// 故在真正发起查询的这里派生（每次调用独立计时、随调用结束释放）。
//
// 为何不叫 archiveColdQueryTimeout：与 cmd 层可能出现的同名常量区分，避免误以为可全局共享一份。
const coldQueryArchiveTimeout = 30 * time.Minute

// withColdArchiveBudget 给冷查询的**归档侧**连接按次绑定 coldQueryArchiveTimeout。
//
// 为什么单独抽出来：有两个 repo（metric / health）的冷查询走的是「热冷共用同一取数函数」的形态
// （queryRangeOn 同时服务热侧与归档侧），不能在函数内统一绑定——那会连热侧一起放宽（热查本就不该慢）。
// 故在调用点只包归档侧连接。返回的 cancel 必须由调用方 defer。
func withColdArchiveBudget(db *gorm.DB) (*gorm.DB, context.CancelFunc) {
	if db == nil {
		return nil, func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), coldQueryArchiveTimeout)
	return db.WithContext(ctx), cancel
}

// fetchColdSide 在归档连接上逐日表查询（冷查询的归档侧入口）。
// 内部按次绑定 coldQueryArchiveTimeout —— 见该常量的说明（归档侧预算是秒级、此处需长期限）。
func fetchColdSide[T any](db *gorm.DB, tables []string, order string, want int, apply func(*gorm.DB) *gorm.DB) ([]T, error) {
	// 按次派生长期限 ctx：只作用于本次冷查询的归档侧，不污染热侧与其它调用。
	if db != nil {
		ctx, cancel := context.WithTimeout(context.Background(), coldQueryArchiveTimeout)
		defer cancel()
		db = db.WithContext(ctx)
	}
	out := make([]T, 0, want)
	for _, tbl := range tables {
		if len(out) >= want {
			break // 已凑满一页（含判 hasMore 的 +1 行），停止再扫更旧表
		}
		var chunk []T
		if err := apply(db.Table(tbl)).Order(order).Limit(want - len(out)).Find(&chunk).Error; err != nil {
			return nil, err
		}
		out = append(out, chunk...)
	}
	return out, nil
}

// mergeColdPage 归并热 / 冷两侧各自已按 less 序排好且各至多 limit+1 的行：
// 主键（ID）去重保热侧、按 less 归并取前 limit，返回（页行, 下一游标, 是否还有下一页）。
//
// 正确性：全局前 (limit+1) 名的行必在各自来源的前 (limit+1) 名内，故两侧各取 limit+1 足以定出
// 归并后的前 limit 名与「是否还有下一页」。归并后行数 > limit 即有下一页，下一游标取本页末行键。
func mergeColdPage[T any](hot, archive []T, limit int, keyOf func(T) coldCursor, less func(a, b coldCursor) bool) ([]T, coldCursor, bool) {
	hotIDs := make(map[string]struct{}, len(hot))
	merged := make([]T, 0, len(hot)+len(archive))
	for i := range hot {
		hotIDs[keyOf(hot[i]).ID] = struct{}{}
		merged = append(merged, hot[i])
	}
	for i := range archive {
		if _, dup := hotIDs[keyOf(archive[i]).ID]; dup {
			continue // 归档进行中（已 copy 未 delete）两侧同存：去重保留热侧（spec §4.4）
		}
		merged = append(merged, archive[i])
	}
	sort.SliceStable(merged, func(i, j int) bool { return less(keyOf(merged[i]), keyOf(merged[j])) })
	hasMore := len(merged) > limit
	if hasMore {
		merged = merged[:limit]
	}
	var next coldCursor
	if hasMore && len(merged) > 0 {
		next = keyOf(merged[len(merged)-1])
	}
	return merged, next, hasMore
}

// unionColdRows 合并热 + 冷两侧全部行、按 dedupKey 去重保热侧（先加热侧）；顺序不保证（调用方自行聚合 / 排序）。
// 供区间聚合类冷查询（指标时序 / 健康快照回放）用——它们无分页，整段并表后交既有聚合逻辑。
func unionColdRows[T any](hot, archive []T, dedupKey func(T) string) []T {
	seen := make(map[string]struct{}, len(hot))
	out := make([]T, 0, len(hot)+len(archive))
	for i := range hot {
		seen[dedupKey(hot[i])] = struct{}{}
		out = append(out, hot[i])
	}
	for i := range archive {
		if _, dup := seen[dedupKey(archive[i])]; dup {
			continue // 去重保热侧（归档进行中两侧同存）
		}
		out = append(out, archive[i])
	}
	return out
}
