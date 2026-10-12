//go:build e2e && loadtest

// 上报队列「为何 4 万事件打不满」的实测探针：在真实链路上找出连接上报队列的真实饱和点。
//
// 背景：既有 probeIngestQueueHeadroom 的默认参数是「200 轮 × 批量 200 × 并发 16」。名字里的
// 「4 万事件」= 200×200，但**入队的计数单位是批、不是行**——AsyncDailyWriter.EnqueueRows 无论
// 批内多少行，都只占队列 1 个槽位（入队单元是 writeBatch「一批」）。故那组参数对队列的压力是
// 200 个槽位 / 4096 容量 = 4.9%，而不是「4 万行 / 4096」。
//
// 本探针要给出的是：在真实链路（真实进程 / 真实 sqlite / 真实 worker 消费）上，队列到底在什么
// 请求数下开始回 429。做法是并发 1（消除并发请求之间的相互掩护）+ 每请求大批量，
// 按「每请求 1 槽位」线性加压，观察 429 首次出现的请求序号。
//
// 运行：
//
//	export E2E_ADMIN_PASS='...' E2E_AUTH_SECRET='...'
//	go test -tags "e2e loadtest" -timeout=20m -v -run '^TestBackpressure429IngestQueueCeiling' ./apps/server/test/loadtest/
//
// 隔离：独立库 beacon-bp429-ingest.db、独立日志前缀、独立二进制与 namespace。
package loadtest_e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"
)

const (
	// bp429IngestMaxRequests 是最多发送的请求数（每请求占 1 个队列槽位）。
	// 取 4096+512：足够越过「队列容量 4096」这条线并留出观测余量。
	bp429IngestMaxRequests = 4608
	// bp429IngestBatch 是每请求携带的事件数（连接事件上限 500）。
	// 用大批量是为了让「入队快 + worker 落库慢」的差值最大化——每槽位的行数越多，消费越吃力。
	bp429IngestBatch = 500
	// bp429IngestSustainProbe 是首次 429 之后再多打几个请求，用于确认 429 会持续（不是偶发一次）。
	bp429IngestSustainProbe = 8
	// bp429IngestRecoverTimeout 是「停止加压后队列被消费并重新接纳」的观测上限。
	bp429IngestRecoverTimeout = 30 * time.Second
	// bp429DefaultQueueCapacity 是生产默认队列容量（与 service.defaultDailyWriteQueueCapacity 同值）。
	// 此处刻意独立声明而不 import service 包：本包只经 HTTP 观测生产行为，不链接内部实现，
	// 以免「用被测实现自己的常量」给结论背书。该值若有变动，日志里的比值会立刻暴露不一致。
	bp429DefaultQueueCapacity = 4096
)

// TestBackpressure429IngestQueueCeiling 实测连接上报队列的饱和点，回答「为什么 4 万事件打不满队列」。
func TestBackpressure429IngestQueueCeiling(t *testing.T) {
	env := bp429SetupIngest(t)

	// 预热：确认鉴权 / 路由 / 请求体形状都通。任一不 202 即前置失败，
	// 避免把「根本打不通」误读成「没有背压」（假阴性比测不到更危险）。
	if status, body := bp429FireConnBatch(t, env, 0); status != http.StatusAccepted {
		t.Fatalf("预热请求应回 202，实际 %d；响应片段：%s", status, bp429Snippet(body))
	}
	t.Logf("预热通过：单个连接上报请求回 202（每请求 %d 条事件）", bp429IngestBatch)

	// —— 串行加压（并发 1）——
	// 串行下每请求的「入队」是微秒级、而 worker 按攒批（200 行 / 500ms）消费，故压力会真实累积在队列上。
	// 这正是不搞并发的原因：并发请求之间会互相「掩护」（先到的先被 worker 取走），
	// 让首次 429 的位置变得随机；串行才能把「第几个槽位被拒」这条线量出来。
	first429 := -1
	counts := map[int]int{}
	start := time.Now()
	sent := 0
	for i := 1; i <= bp429IngestMaxRequests; i++ {
		status, body := bp429FireConnBatch(t, env, i)
		sent++
		counts[status]++
		if status == http.StatusTooManyRequests {
			if code := bp429ErrorCode(t, body); code != "conn_ingest_busy" {
				t.Fatalf("第 %d 个请求回 429 但业务码为 %q（期望 conn_ingest_busy）", i, code)
			}
			if first429 < 0 {
				first429 = i
				t.Logf("【首次背压】第 %d 个请求（≈第 %d 个队列槽位）回 HTTP 429 conn_ingest_busy", i, i)
				t.Logf("  429 响应体：%s", bp429Snippet(body))
			}
		}
		if first429 > 0 && i >= first429+bp429IngestSustainProbe {
			break
		}
	}
	elapsed := time.Since(start)

	t.Logf("=== 实测结果（串行 %d 请求 × 每请求 %d 条事件，耗时 %s）===",
		sent, bp429IngestBatch, elapsed.Round(time.Millisecond))
	t.Logf("状态码分布：%s", bp429StatusCountsText(counts))
	if first429 < 0 {
		t.Fatalf("串行 %d 个请求（每请求占 1 槽位，容量 %d）仍未触到队列满——"+
			"说明消费速率足以跟上串行入队，或入队并未真正占槽", sent, bp429DefaultQueueCapacity)
	}
	t.Logf("【实测饱和点】队列容量 %d 槽位，串行下第 %d 个请求首次被拒（占容量 %.1f%%）",
		bp429DefaultQueueCapacity, first429, float64(first429)/float64(bp429DefaultQueueCapacity)*100)
	t.Logf("【量纲澄清】入队计数单位是**批**不是行：每请求无论带 %d 条还是 1 条事件，都只占 1 个槽位。", bp429IngestBatch)
	t.Logf("   故既有的「4 万事件」若按 200 批发出（probeIngestQueueHeadroom 默认 200 轮 × 批量 200）,")
	t.Logf("   对队列的压力只有 200/4096 = 4.9%%，与「4 万行 / 4096 槽位」相差 200 倍——")
	t.Logf("   这不是「背压不存在」，而是施压方式与队列容量的量纲不同。")
	t.Logf("   同一参数（200 轮 × 批量 200 × 并发 16）即使在并发下也只能提供 200 个槽位的压力，")
	t.Logf("   与本次实测的饱和点（约 %d 个请求）相差一个数量级。", first429)

	// —— 恢复证据：队列被消费后重新接纳 ——
	// 不再发新请求，等 worker 把积压 flush 掉，再发一个请求应回 202。
	if !bp429PollIngestRecovered(t, env) {
		t.Fatalf("停止加压后队列应被消费并重新接纳，但 %s 内仍为 429", bp429IngestRecoverTimeout)
	}
	t.Log("【恢复证据】停止加压后队列被 worker 消费，再次上报恢复 202——429 是瞬时背压，非永久拒绝。")
}

// bp429SetupIngest 装配队列实测所需的真实链路（含一个 v2 proxy 身份：连接上报要求 proxy kind）。
func bp429SetupIngest(t *testing.T) *bp429Env {
	t.Helper()
	return bp429Setup(t, bp429EnvConfig{
		DBName: "beacon-bp429-ingest.db", LogPrefix: "beacon-bp429-ingest",
		BinName: "beacon-bp429-ingest", Namespace: "loadtest-bp429-ingest",
	}, [][]byte{bytes.Repeat([]byte("I"), 128)})
}

// bp429FireConnBatch 发一个连接批量上报请求，返回状态码与响应体。
// seq 仅用于日志定位，不再参与事件时间戳构造（见 bp429ConnEvents 的说明）。
func bp429FireConnBatch(t *testing.T, env *bp429Env, seq int) (int, []byte) {
	t.Helper()
	_ = seq
	body, err := json.Marshal(map[string]any{
		"bootId":       env.ident.BootID,
		"droppedCount": 0,
		"events":       bp429ConnEvents(t, seq),
	})
	if err != nil {
		t.Fatalf("编码连接上报请求体失败：%v", err)
	}
	req, err := http.NewRequest(http.MethodPost, env.baseURL+"/beacon/v2/agent/connections/batch", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("构造连接上报请求失败：%v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	bp429SetAgentHeaders(req, env.ident)
	resp, raw := bp429Do(t, env.cp, req)
	return resp.StatusCode, raw
}

// bp429ConnEvents 造一批连接 open 事件：conn_id 必须是 UUIDv7（服务端从中取内嵌时间做偏移护栏）。
//
// 内嵌时间必须贴住**当前时刻**：控制面对 |now-内嵌时间| >5min 记 WARN、>24h 直接丢弃该事件
// （connClockSkewWarn / connClockSkewReject）。这里只用「当前毫秒 + 批内序号」错开同批事件
// （最大偏移 499ms，远在护栏内），**不要**按请求序号累加秒数——串行打几千个请求会一路
// 超过 5min 阈值，把日志刷成几百万行 WARN 并干扰对结果的判读。
// 跨请求唯一性由 crypto/rand 填充的随机段保证（批内去重只在单次 Ingest 内生效，不跨请求）。
func bp429ConnEvents(t *testing.T, _ int) []map[string]any {
	t.Helper()
	baseMs := time.Now().UTC().UnixMilli()
	events := make([]map[string]any, 0, bp429IngestBatch)
	for i := range bp429IngestBatch {
		events = append(events, map[string]any{
			"kind":       "open",
			"connId":     bp429UUIDv7(t, baseMs+int64(i)),
			"playerUuid": fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
			"playerName": "loadtest",
			"clientIp":   "127.0.0.1",
		})
	}
	return events
}

// bp429UUIDv7 造一个内嵌指定毫秒时间戳的 UUIDv7（服务端 TimeMsFromUUIDv7 从中取时间）。
// 前一字段 48 位毫秒时间戳，后段用 crypto/rand 填（同毫秒内也必须两两不同，否则会被批内去重吃掉）。
func bp429UUIDv7(t *testing.T, ms int64) string {
	t.Helper()
	raw := make([]byte, 16)
	// 前 6 字节 = 48 位毫秒时间戳（大端）
	for i := range 6 {
		raw[i] = byte(ms >> (40 - 8*i))
	}
	if _, err := io.ReadFull(rand.Reader, raw[6:]); err != nil {
		t.Fatalf("生成 UUIDv7 随机段失败：%v", err)
	}
	raw[6] = 0x70 | (raw[6] & 0x0f) // 版本 7
	raw[8] = 0x80 | (raw[8] & 0x3f) // RFC 4122 变体
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
}

// bp429PollIngestRecovered 轮询直到连接上报重新被接纳（202），返回是否恢复。
func bp429PollIngestRecovered(t *testing.T, env *bp429Env) bool {
	t.Helper()
	deadline := time.Now().Add(bp429IngestRecoverTimeout)
	for i := 0; ; i++ {
		if err := env.cp.CheckEarlyExit(); err != nil {
			t.Fatalf("等队列恢复期间控制面已早退：%v", err)
		}
		status, _ := bp429FireConnBatch(t, env, 100000+i)
		switch status {
		case http.StatusAccepted:
			return true
		case http.StatusTooManyRequests:
			// 仍在背压中：继续等 worker 消费。
		default:
			t.Fatalf("轮询队列恢复时遇到非预期状态 %d", status)
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// bp429StatusCountsText 把状态码分布渲染成稳定排序的文本（便于日志比对）。
func bp429StatusCountsText(counts map[int]int) string {
	keys := make([]int, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d: %d 次", k, counts[k]))
	}
	return strings.Join(parts, "，")
}
