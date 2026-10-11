//go:build e2e && loadtest

// 长轮询 / SSE 挂起探测（压测 harness 的「挂起点怎么量」部分）。
//
// 为什么长轮询不能用 RPS 衡量：driver.go 的固定 worker 池模型假设「请求很快返回、可以反复打」，
// 而 /beacon/v1/agent/config/effective 这类端点在无变更时会一直挂到超时才返回（服务端上限是
// longpoll.max-hold-ms，默认 30s，见 config.example.yml）。对它算 RPS 只会得到一个由挂起时长决定的
// 倒数（单连接 ≈ 0.033 RPS），既不反映服务端能力、也无法横向比较；更要紧的是「能同时挂住多少连接」
// 这个真正的容量维度在 RPS 里完全不可见。所以这里刻意没有 RPS 字段，也没有「越快越好」。
//
// 改量三个指标：
//  1. 能同时挂多少（Held / Connected / SuspendedObserved）——并发挂起容量。Connected 是客户端成功
//     建立并进入挂起的连接数；SuspendedObserved 是服务端自观测（GET /admin/v1/system/observability
//     的长轮询挂起计数）看到的峰值，用来交叉验证「服务端真的为这些连接登记了 waiter」。
//  2. 挂得稳不稳（Errors / ErrorKinds / CompletedByTimeout）——挂起期间是否被莫名断开、是否如期
//     自然超时（长轮询无变更到超时返回 304）。
//  3. 变更多久能推下去（WakeLatency）——配置热更的端到端推送时延：从变更请求发出，到各挂起连接
//     实际返回。这是「发布 → NotifyConfigChange → Hub.Notify → waiter 重算比对」这条链路唯一的
//     端到端口径，也是长轮询最该看的数字。
//
// 状态码语义（以 apps/server/internal/handler/agent_handler.go 的 Effective 实测为准，不猜）：
//   - 200：请求带的 md5 与服务端重算结果不同。两种来路——「一进来就不同」（agent 的 md5 已过期）
//     与「挂起中被唤醒、重算后真的变了」；响应体带最新 md5 与全部有效配置项。
//   - 304：挂满超时窗口仍无变更（service.WaitEffective 的 changed=false 分支）。
//
// 因此「被唤醒返回」与「超时返回」靠状态码即可区分：200 = 有变更（唤醒），304 = 自然超时。
//
// 唤醒做法：WakeAt 时刻发一次真实配置变更（POST /admin/v1/configs，global scope，管理员令牌）。
// global scope 经 service.ChangeNotifier.notifyScope → longpoll.Hub.NotifyNamespace 唤醒该 namespace
// 下全部 waiter，故一次变更应唤醒探测内全部连接。唤醒语义是逐 dataId / scope 的（窄 scope 只唤醒
// 受影响集合），若接入方换成 zone / server scope，WakeLatency 的样本数会小于 Held——那本身就是结论，
// 这里不粉饰：样本数与连接数对不上时，先怀疑唤醒范围而不是唤醒速度。
// 刻意用「新建一条 global 层配置项」而非 PUT 发布新版本：PUT 走审批流水线（RequestPublish →
// approval-requests → 审批执行器），多一环与挂起时延无关的调度不确定性；而新建即首次发布
// （version=1），与 PUT 发布共用同一条 notify → Hub 唤醒路径。每次唤醒换一个 dataId，保证有效 md5 真变。
package loadtest_e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// suspendedDefaultHoldMs 是服务端长轮询挂起上限的默认值（config.example.yml：longpoll.max-hold-ms）。
	suspendedDefaultHoldMs = 30_000
	// suspendedPrimeTimeoutMs 是「探当前 md5」用的短超时：只为拿一次立即返回的 md5，不给它挂起的机会。
	suspendedPrimeTimeoutMs = 1_000
	// suspendedClientSlack 是客户端请求超时在挂起窗口之外留的余量。
	// 必须显著大于服务端挂起上限：否则客户端先超时，量到的就是客户端自己而不是服务端行为。
	suspendedClientSlack = 15 * time.Second
	// suspendedSettlePoll 是入挂阶段的观测粒度：waiter 在 handler 入口就注册（WaitEffective 先
	// Register 再 Resolve），5ms 粒度足以看清「从发出到真正挂住」的量级，又不至于把自观测端点打成压测负载。
	suspendedSettlePoll = 5 * time.Millisecond
	// suspendedSettleTimeout 是单条连接等待服务端登记为 waiter 的上限（超过只记探测异常，不断开连接）。
	suspendedSettleTimeout = 15 * time.Second
	// suspendedObserveInterval 是挂起峰值的常规采样间隔（粗采样：只为抓峰值，不为精确时序）。
	suspendedObserveInterval = time.Second
	// suspendedObserveTimeout 是单次自观测请求的上限（观测失败不影响主流程）。
	suspendedObserveTimeout = 10 * time.Second
	// suspendedChangeTimeout 是唤醒配置变更请求的上限（admin 写端点，含审计落库）。
	suspendedChangeTimeout = 30 * time.Second
	// suspendedBodyLimit 是长轮询响应体读取上限（变更时下发全量有效配置，与 /metrics 同口径 8MiB）。
	suspendedBodyLimit = 8 << 20
	// suspendedSnippetLimit 是失败回带的响应片段上限（够定位问题，不刷屏）。
	suspendedSnippetLimit = 512
	// suspendedGlobalGroup 是配置覆盖链最高层的固定 group 码（model.GlobalGroupCode）。
	suspendedGlobalGroup = "__GLOBAL__"
	// suspendedGlobalScope 是 global 层的 scopeLevel 字面值（model.ScopeGlobal）。
	suspendedGlobalScope = "global"
	// suspendedWakeFormat 是唤醒配置项的格式（在 merge.IsValidFormat 白名单内）。
	suspendedWakeFormat = "yaml"
	// suspendedWakeDataIDPrefix 是唤醒配置项的 dataId 前缀（每次唤醒换一个，保证有效 md5 真变）。
	suspendedWakeDataIDPrefix = "loadtest-suspended-wake"
	// suspendedSSEDataIDPrefix 是 SSE 唤醒配置项的 dataId 前缀。
	suspendedSSEDataIDPrefix = "loadtest-sse-wake"
	// suspendedPrimeMD5 是探当前 md5 用的假值：必然与服务端重算结果不同，从而立即返回 200。
	suspendedPrimeMD5 = "loadtest-probe-prime-md5"
	// longpollSettingKey 是服务端长轮询挂起上限的设置 key（FR-61 热改项）。
	longpollSettingKey = "longpoll.max-hold-ms"
)

// suspendedConfig 是长轮询挂起探测的参数。
type suspendedConfig struct {
	BaseURL string
	Token   string // v1 共享 token（X-Beacon-Token）
	// AdminToken 是管理员登录令牌（Authorization: Bearer）。
	// 自观测（/admin/v1/system/observability）与唤醒用的配置变更都在 admin 面，v1 共享 token 过不了
	// 这两道鉴权，故单独一个字段；只做 agent 面长轮询的调用方可以留空（此时只丢了 SuspendedObserved）。
	AdminToken string
	// Target 是被挂起的长轮询端点（含查询参数），
	// 例：/beacon/v1/agent/config/effective?namespace=prod&serverId=<id>&md5=<当前md5>&timeoutMs=30000
	Target string
	Held   int           // 同时保持的挂起连接数
	WakeAt time.Duration // 开始后多久触发一次变更唤醒；0 表示不唤醒
	// Duration 是探测总时长：到点即取消在挂请求并收尾，不等它们自然超时。
	Duration time.Duration
}

// suspendedResult 是挂起探测的结果。注意：这里没有 RPS，也没有「越快越好」——
// 挂起端点衡量的是「能同时挂多少」「挂得稳不稳」「变更多久能推下去」。
type suspendedResult struct {
	Held               int            `json:"held"`
	Connected          int            `json:"connected"`          // 成功建立并进入挂起的连接数
	CompletedByWake    int            `json:"completedByWake"`    // 因变更被唤醒而返回的连接数
	CompletedByTimeout int            `json:"completedByTimeout"` // 挂满 timeoutMs 自然超时返回的连接数
	Errors             int            `json:"errors"`
	ErrorKinds         map[string]int `json:"errorKinds"`        // 错误类型 → 次数（连接被拒 / EOF / 超时等）
	HoldLatency        latencyStats   `json:"holdLatency"`       // 每条连接「从发出到真正挂住」的耗时
	WakeLatency        latencyStats   `json:"wakeLatency"`       // 唤醒信号到连接实际返回的耗时（核心指标）
	SuspendedObserved  int            `json:"suspendedObserved"` // 服务端观测到的并发挂起数峰值
	// PingInterval 是 SSE 探测的扩展字段：保活注释帧（`: ping`）的到达间隔。长轮询探测不填（nil）。
	PingInterval *latencyStats `json:"pingInterval,omitempty"`
}

// suspendedAttempt 是一次长轮询尝试的结果（发出时刻用于唤醒归因）。
type suspendedAttempt struct {
	start   time.Time
	status  int
	md5     string
	elapsed time.Duration
	err     error
}

// runSuspendedProbe 保持 Held 个长轮询连接，观测挂起形态与唤醒时延。
// 整体服从 ctx：调用方取消（或 Duration 到点）即在挂请求全部中断、goroutine 收尾退出，不泄漏。
func runSuspendedProbe(ctx context.Context, cfg suspendedConfig) (*suspendedResult, error) {
	if cfg.Held < 1 {
		return nil, errors.New("held（同时保持的挂起连接数）必须 ≥ 1")
	}
	if cfg.Duration <= 0 {
		return nil, errors.New("Duration（探测时长）必须为正")
	}
	if cfg.Target == "" {
		return nil, errors.New("Target（被挂起的长轮询端点）不能为空")
	}
	if cfg.AdminToken == "" {
		return nil, errors.New("AdminToken 不能为空：SuspendedObserved 与唤醒变更都在 admin 面，需要管理员令牌")
	}

	// 到点即整体取消：挂起中的请求随之中断，worker 不等自然超时（挂满 30s 会把整个 harness 拖死）。
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(cfg.Duration, cancel)
	defer timer.Stop()

	holdWindow := suspendedHoldWindow(cfg.BaseURL, cfg.AdminToken, cfg.Target)
	client := newSuspendedClient(cfg.Held)
	defer client.CloseIdleConnections()

	probe := newSuspendedProbe(cfg, client, holdWindow)
	observer := newSuspendedObserver(cfg.BaseURL, cfg.AdminToken)
	defer observer.stop()

	// 先用假 md5 打一发：立即返回 200 并带当前有效 md5。不知道当前 md5 就发挂起请求，
	// 每条连接都会「一进来就不同」地立即返回，量到的就不是挂起而是立即返回。
	current, err := probe.primeMD5(runCtx)
	if err != nil {
		return nil, err
	}
	probe.setMD5(current)

	// 观测起点：别的探测或残留连接也可能挂着，不能把它们的计数算进本次的挂起增量。
	baseline := max(observer.sample(runCtx), 0)
	probe.settle(runCtx, observer, baseline)
	observer.start(runCtx, suspendedObserveInterval)
	if cfg.WakeAt > 0 {
		probe.wg.Add(1)
		go probe.wakeLoop(runCtx)
	}
	probe.wg.Wait()

	res := probe.result
	res.SuspendedObserved = suspendedPeak(observer, res)
	res.HoldLatency = summarizeLatency(probe.holdSamples)
	res.WakeLatency = summarizeLatency(probe.wakeSamples)
	return res, nil
}

// suspendedProbe 是单次长轮询挂起探测的运行态（结果汇聚 + 取消传播 + 唤醒记账）。
// 不把 observer 放进这里：自观测读取器由 runSuspendedProbe 持有并显式 stop，
// 挂在探测结构上容易出现「探测结束但采样 goroutine 还在跑」的所有权歧义。
type suspendedProbe struct {
	cfg       suspendedConfig
	client    *http.Client
	deadline  time.Time
	reqTimout time.Duration

	mu          sync.Mutex
	md5Value    string
	wakeAt      time.Time // 变更请求发出的时刻；wakeIssued 为 false 时无意义
	wakeIssued  bool
	holdSamples []int64 // 每条连接「从发出到服务端登记 waiter」的耗时（纳秒）
	wakeSamples []int64 // 每条被唤醒连接「变更发出 → 实际返回」的耗时（纳秒）
	result      *suspendedResult
	wg          sync.WaitGroup
}

// newSuspendedProbe 构造探测运行态（deadline / 请求超时都在这里一次性算好）。
// holdWindow 是本次实际挂起窗口（min(客户端 timeoutMs, 服务端上限)），请求超时必须在它之外。
func newSuspendedProbe(cfg suspendedConfig, client *http.Client, holdWindow time.Duration) *suspendedProbe {
	return &suspendedProbe{
		cfg: cfg, client: client,
		deadline:  time.Now().Add(cfg.Duration),
		reqTimout: holdWindow + suspendedClientSlack,
		result:    &suspendedResult{Held: cfg.Held},
	}
}

// md5 返回当前已知的有效配置 md5（worker 重挂时用，avoid 用过期 md5 打出立即返回）。
func (p *suspendedProbe) md5() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.md5Value
}

// setMD5 记下当前有效配置 md5。
func (p *suspendedProbe) setMD5(value string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.md5Value = value
}

// outOfTime 判断探测是否已到点（到点后不再发起新的挂起，只收尾）。
func (p *suspendedProbe) outOfTime() bool {
	return !time.Now().Before(p.deadline)
}

// primeMD5 用一个必然不同的假 md5 打一发长轮询，立即拿到服务端当前有效 md5。
// 长轮询只在「请求 md5 == 服务端 md5」时才挂起；不知道当前 md5 就发挂起请求，每条都会立即返回 200，
// 于是 Connected 数字好看却一条都没挂住——那测的是立即返回而不是挂起。故探测前先探一次当前 md5。
func (p *suspendedProbe) primeMD5(ctx context.Context) (string, error) {
	target := suspendedTargetWithMD5(p.cfg.Target, suspendedPrimeMD5)
	attempt := p.holdOnceWith(ctx, target, suspendedPrimeTimeoutMs*time.Millisecond)
	if attempt.err != nil {
		return "", fmt.Errorf("探当前 md5 失败：%w", attempt.err)
	}
	if attempt.status != http.StatusOK {
		return "", fmt.Errorf("探当前 md5 期望 %d，实际 %d（假 md5 未命中当前值时不会挂起，只应 200）",
			http.StatusOK, attempt.status)
	}
	if attempt.md5 == "" {
		return "", errors.New("探当前 md5 的响应缺少 md5 字段，无法构造挂起请求")
	}
	return attempt.md5, nil
}

// settle 逐条发起挂起连接，并把「从发出到服务端真的登记为 waiter」的耗时记进 HoldLatency。
// 逐条发而非一次性并发发：waiter 在 handler 入口就注册（WaitEffective 先 Register 再 Resolve），
// 自观测的挂起计数 +1 即代表该连接已被服务端挂住，逐条发才能把这次 +1 归因到具体连接。
func (p *suspendedProbe) settle(ctx context.Context, observer *suspendedObserver, baseline int) {
	last := baseline
	for range p.cfg.Held {
		if ctx.Err() != nil {
			return
		}
		issued := time.Now()
		p.wg.Add(1)
		go p.workerLoop(ctx)
		observed := observer.waitRise(ctx, last, suspendedSettleTimeout)
		if observed <= last {
			// 自观测没看到计数上涨：请求可能还在路上，也可能已经失败。记一次探测异常但不打断连接。
			p.absorbKind("anomaly:suspend_not_observed")
		}
		last = observed
		p.mu.Lock()
		p.holdSamples = append(p.holdSamples, int64(time.Since(issued)))
		p.mu.Unlock()
	}
}

// workerLoop 单条长轮询连接的生命周期：挂起 → 归因返回 → 若还有剩余时长就按最新 md5 重挂。
// 重挂是真实 agent 的行为：被唤醒后它会带着新 md5 再来一轮，否则「唤醒后还能不能继续挂住」测不到。
func (p *suspendedProbe) workerLoop(ctx context.Context) {
	defer p.wg.Done()
	for {
		if ctx.Err() != nil || p.outOfTime() {
			return
		}
		attempt := p.holdOnce(ctx)
		if attempt.err != nil {
			// 探测到点取消导致的失败不算连接问题：那是我们自己的收尾动作。
			if ctx.Err() != nil {
				return
			}
			p.absorbKind(suspendedErrorKind(attempt.err))
			continue
		}
		p.absorb(attempt)
		if ctx.Err() != nil || p.outOfTime() {
			return
		}
	}
}

// holdOnce 发一次长轮询请求并把响应读尽，返回状态码 / 响应体里的最新 md5 / 耗时。
// 请求超时取「服务端挂起窗口 + 余量」：小于窗口会先被客户端判超时，测的就是客户端自己。
func (p *suspendedProbe) holdOnce(ctx context.Context) suspendedAttempt {
	return p.holdOnceWith(ctx, suspendedTargetWithMD5(p.cfg.Target, p.md5()), p.reqTimout)
}

// holdOnceWith 对给定目标发一次长轮询请求；timeout 覆盖单次请求上限（探 md5 用短超时，挂起用窗口+余量）。
func (p *suspendedProbe) holdOnceWith(ctx context.Context, target string, timeout time.Duration) suspendedAttempt {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return suspendedAttempt{start: time.Now(), err: err}
	}
	req.Header.Set("X-Beacon-Token", p.cfg.Token)
	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return suspendedAttempt{start: start, elapsed: time.Since(start), err: err}
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, suspendedBodyLimit))
	closeErr := resp.Body.Close()
	attempt := suspendedAttempt{
		start: start, status: resp.StatusCode, md5: suspendedResponseMD5(body),
		elapsed: time.Since(start),
	}
	switch {
	case readErr != nil:
		attempt.err = readErr
	case closeErr != nil:
		attempt.err = closeErr
	}
	return attempt
}

// absorb 归因一次连接返回。口径：
//   - 200 = 有变更（被唤醒，或 md5 已过期）；
//   - 304 = 挂满超时窗口仍无变更；
//   - 其它状态码 / 传输错误 = Errors（连接根本没能按长轮询语义走完）。
func (p *suspendedProbe) absorb(attempt suspendedAttempt) {
	p.mu.Lock()
	defer p.mu.Unlock()
	res := p.result
	if attempt.status != http.StatusOK && attempt.status != http.StatusNotModified {
		res.Errors++
		res.addKind("http_" + strconv.Itoa(attempt.status))
		return
	}
	res.Connected++
	if attempt.status == http.StatusNotModified {
		res.CompletedByTimeout++
		return
	}
	if attempt.md5 != "" {
		p.md5Value = attempt.md5 // 按服务端最新 md5 重挂，避免下一轮立即返回
	}
	if !p.wakeIssued || attempt.start.After(p.wakeAt) {
		// 没有唤醒却拿到 200，或连接是在变更发出之后才建立的：都没法归因到本次唤醒。
		// 记成探测异常而不是 CompletedByWake——否则「唤醒时延 5ms」会是一次假阳性。
		res.Errors++
		res.addKind("anomaly:change_return_not_attributed")
		return
	}
	res.CompletedByWake++
	p.wakeSamples = append(p.wakeSamples, int64(attempt.start.Add(attempt.elapsed).Sub(p.wakeAt)))
}

// absorbKind 记一次失败 / 探测异常（ErrorKinds 的细分口径，anomaly: 前缀表示非传输错误的探测异常）。
func (p *suspendedProbe) absorbKind(kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.result.Errors++
	p.result.addKind(kind)
}

// wakeLoop 在 WakeAt 时刻触发一次变更唤醒（只触发一次；失败只记账，不让整个探测陪葬）。
func (p *suspendedProbe) wakeLoop(ctx context.Context) {
	defer p.wg.Done()
	timer := time.NewTimer(p.cfg.WakeAt)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if err := p.triggerWake(ctx, suspendedWakeDataIDPrefix); err != nil {
		// 失败必须留痕：没有痕迹的话「零唤醒」会被误读成唤醒语义问题而非 harness 问题。
		p.absorbKind("anomaly:wake_change_failed")
		fmt.Fprintf(os.Stderr, "[loadtest] 长轮询唤醒变更失败：%v\n", err)
	}
}

// triggerWake 发起一次真实配置变更（新建 global 层配置项 = 首次发布），
// 记下「请求发出」的时刻作为唤醒时延的计时起点，随后由 absorb 按连接归因。
func (p *suspendedProbe) triggerWake(ctx context.Context, dataIDPrefix string) error {
	ns := suspendedQueryValue(p.cfg.Target, "namespace")
	if ns == "" {
		return errors.New("目标地址缺少 namespace 查询参数，无法发起唤醒变更")
	}
	marker := strconv.FormatInt(time.Now().UnixNano(), 36)
	body, err := json.Marshal(map[string]any{
		"namespace": ns, "group": suspendedGlobalGroup,
		"dataId": dataIDPrefix + "-" + marker, "scopeLevel": suspendedGlobalScope, "scopeTarget": "",
		"format": suspendedWakeFormat, "content": "marker: " + marker + "\n",
		"comment": "长轮询挂起探测唤醒（loadtest）",
	})
	if err != nil {
		return fmt.Errorf("编码唤醒配置失败：%w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, suspendedChangeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		p.cfg.BaseURL+"/admin/v1/configs", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造唤醒配置请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.AdminToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", dataIDPrefix+"-"+marker)
	sent := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("唤醒配置请求失败：%w", err)
	}
	snippet, readErr := io.ReadAll(io.LimitReader(resp.Body, suspendedSnippetLimit))
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("读取唤醒响应失败：read=%v close=%w", readErr, closeErr)
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("唤醒配置期望 %d，实际 %d；响应片段：%s",
			http.StatusCreated, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wakeAt = sent
	p.wakeIssued = true
	return nil
}

// newSuspendedClient 构造挂起探测专用客户端：连接池按 Held 调优。
// 长轮询不能用共享连接池的连接：连接在响应返回前一直处于 in-use，池里凑不出 Held 条并发连接时会退化成
// 「新建连接 → 挂起 → 关闭」，量到的就是本机 TCP 握手速率而不是控制面的挂起容量。
// 两点与 newLoadClient（请求-响应型压测）刻意不同：不设 ResponseHeaderTimeout（挂起端点要几十秒才有
// 响应头，那个字段会把所有挂起判成超时），IdleConnTimeout 取 90s 让重挂能复用同一条连接。
func newSuspendedClient(held int) *http.Client {
	return &http.Client{Transport: &http.Transport{
		// Proxy 显式置 nil：忽略环境里的 HTTP_PROXY / NO_PROXY，避免本地压测被绕到代理上。
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns:        held*2 + 2,
		MaxIdleConnsPerHost: held + 2,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
		ForceAttemptHTTP2:   false,
	}}
}

// suspendedHoldWindow 从目标地址取本次挂起窗口：min(客户端 timeoutMs, 服务端上限)。
// 服务端上限经 GET /admin/v1/settings 读 longpoll.max-hold-ms（热改项，读不到就用默认 30s）。
// 只有拿到真实窗口，客户端超时才能放在窗口之外——否则客户端先超时，量到的是客户端自己。
func suspendedHoldWindow(baseURL, adminToken, target string) time.Duration {
	window := time.Duration(suspendedDefaultHoldMs) * time.Millisecond
	if server := suspendedServerHold(baseURL, adminToken); server > 0 {
		window = server
	}
	if clientMs, ok := suspendedTimeoutMs(target); ok && time.Duration(clientMs)*time.Millisecond < window {
		window = time.Duration(clientMs) * time.Millisecond
	}
	return window
}

// suspendedServerHold 读服务端长轮询挂起上限（读失败按 0 返回，调用方回退默认值）。
func suspendedServerHold(baseURL, adminToken string) time.Duration {
	if baseURL == "" || adminToken == "" {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), suspendedObserveTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/admin/v1/settings", nil)
	if err != nil {
		return 0
	}
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0
	}
	var view struct {
		Items []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"items"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, suspendedBodyLimit)).Decode(&view); err != nil {
		return 0
	}
	for _, item := range view.Items {
		if item.Key != longpollSettingKey {
			continue
		}
		ms, err := strconv.Atoi(strings.TrimSpace(item.Value))
		if err != nil || ms <= 0 {
			return 0
		}
		return time.Duration(ms) * time.Millisecond
	}
	return 0
}

// suspendedTimeoutMs 取目标地址上的 timeoutMs 查询参数（服务端按 min(客户端值, 上限) 挂起）。
func suspendedTimeoutMs(target string) (int, bool) {
	parsed, err := url.Parse(target)
	if err != nil {
		return 0, false
	}
	raw := parsed.Query().Get("timeoutMs")
	if raw == "" {
		return 0, false
	}
	ms, err := strconv.Atoi(raw)
	if err != nil || ms <= 0 {
		return 0, false
	}
	return ms, true
}

// suspendedQueryValue 取目标地址上的某个查询参数值（取不到返回空串）。
func suspendedQueryValue(target, key string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return ""
	}
	return parsed.Query().Get(key)
}

// suspendedTargetWithMD5 把目标地址的 md5 查询参数替换为给定值（其余参数原样保留）。
func suspendedTargetWithMD5(target, md5 string) string {
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	query := parsed.Query()
	query.Set("md5", md5)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

// suspendedResponseMD5 从长轮询 200 响应体里取最新有效 md5（解析不到返回空串，调用方按「没有新 md5」处理）。
func suspendedResponseMD5(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var view struct {
		MD5 string `json:"md5"`
	}
	if err := json.Unmarshal(body, &view); err != nil {
		return ""
	}
	return view.MD5
}

// suspendedErrorKind 把一次失败归类成稳定的错误类型。
// 只做粗分类：错误文本可变长，塞进 map key 会把 ErrorKinds 撑成一堆几乎只出现一次的键。
func suspendedErrorKind(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "client_timeout"
	}
	msg := err.Error()
	for _, candidate := range []struct {
		needle string
		kind   string
	}{
		{"connection refused", "connection_refused"},
		{"connection reset", "connection_reset"},
		{"broken pipe", "broken_pipe"},
		{"EOF", "eof"},
		{"Client.Timeout", "client_timeout"},
		{"no route to host", "network_unreachable"},
		{"network is unreachable", "network_unreachable"},
		{"too many open files", "fd_exhausted"},
	} {
		if strings.Contains(msg, candidate.needle) {
			return candidate.kind
		}
	}
	return "other"
}

// addKind 累加一个错误类型计数（map 惰性分配）。
func (r *suspendedResult) addKind(kind string) {
	if r.ErrorKinds == nil {
		r.ErrorKinds = map[string]int{}
	}
	r.ErrorKinds[kind]++
}

// suspendedObserver 旁路读 /admin/v1/system/observability 的长轮询挂起计数。
// 这是「服务端真的看到了这么多挂起」的唯一外部证据：没有它，Held 只是客户端以为自己挂住了。
type suspendedObserver struct {
	baseURL string
	token   string
	client  *http.Client

	mu       sync.Mutex
	peak     int
	samples  int
	failed   int
	running  bool
	stopChan chan struct{}
	stopped  chan struct{}
}

// newSuspendedObserver 构造自观测读取器（独立客户端：观测请求不该占用挂起连接池的连接）。
func newSuspendedObserver(baseURL, token string) *suspendedObserver {
	return &suspendedObserver{
		baseURL:  baseURL,
		token:    token,
		client:   &http.Client{Timeout: suspendedObserveTimeout},
		stopChan: make(chan struct{}),
		stopped:  make(chan struct{}),
	}
}

// sample 读一次自观测，返回长轮询挂起总数；失败返回 -1（调用方据此跳过，不当成 0）。
func (o *suspendedObserver) sample(ctx context.Context) int {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		o.baseURL+"/admin/v1/system/observability", nil)
	if err != nil {
		return -1
	}
	req.Header.Set("Authorization", "Bearer "+o.token)
	resp, err := o.client.Do(req)
	if err != nil {
		return -1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return -1
	}
	var view struct {
		Longpoll struct {
			Total int `json:"total"`
		} `json:"longpoll"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, suspendedBodyLimit)).Decode(&view); err != nil {
		return -1
	}
	return view.Longpoll.Total
}

// record 收下一个样本：失败只计次，成功则更新峰值与样本数。
func (o *suspendedObserver) record(total int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if total < 0 {
		o.failed++
		return
	}
	o.samples++
	o.peak = max(o.peak, total)
}

// waitRise 细粒度轮询自观测，直到挂起计数超过 above（即新连接被服务端登记为 waiter）。
// 返回观测到的计数值；超时返回最后一次观测值（可能仍等于 above），由调用方判读。
// 5ms 粒度会让 HoldLatency 带上最多一个采样间隔的正偏（真实登记更快），这是观测成本的代价。
func (o *suspendedObserver) waitRise(ctx context.Context, above int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	last := above
	for {
		if ctx.Err() != nil {
			return last
		}
		observed := o.sample(ctx)
		o.record(observed)
		if observed > above {
			return observed
		}
		last = observed
		if !time.Now().Before(deadline) {
			return last
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(suspendedSettlePoll):
		}
	}
}

// start 起后台粗采样：只抓挂起峰值（SuspendedObserved），不做时序分析。
func (o *suspendedObserver) start(ctx context.Context, interval time.Duration) {
	o.mu.Lock()
	if o.running {
		o.mu.Unlock()
		return
	}
	o.running = true
	o.mu.Unlock()
	go func() {
		defer close(o.stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-o.stopChan:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				o.record(o.sample(ctx))
			}
		}
	}()
}

// stop 停掉后台采样并等它退出（幂等；未启动时是空操作）。不等待就会在测试收尾时留野 goroutine。
func (o *suspendedObserver) stop() {
	o.mu.Lock()
	if !o.running {
		o.mu.Unlock()
		return
	}
	o.running = false
	o.mu.Unlock()
	close(o.stopChan)
	<-o.stopped
}

// peakOf 返回观测到的挂起峰值与自观测失败次数。
// 失败次数要回填进 ErrorKinds：否则「SuspendedObserved=0」会被读成「服务端没挂住」，
// 而真相可能只是自观测端点读不到（令牌失效 / 网络抖动），两者结论完全相反。
func (o *suspendedObserver) peakOf() (peak, failed int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.peak, o.failed
}

// runSSEProbe 观测 /beacon/v1/agent/stream 单条 SSE 长连接的保持能力与保活帧间隔。
// 与长轮询探测的差别：SSE 不返回，量的就是「能保持多久不被动断」与「保活注释帧（`: ping`）间隔是否
// 符合 docs/OPERATIONS.md §3.1 的描述（beacon 无变更时按 longpoll.max-hold-ms 间隔发 `: ping`，
// 见 apps/server/internal/sse 的 Encode 与 service.StreamService.Run）。
// 字段口径与 suspendedResult 对齐：HoldLatency = 从发出到收到首帧（ready）的耗时；
// WakeLatency = 变更请求发出到收到 config-changed 的耗时；CompletedByTimeout = 保持到探测结束
// 仍未被动断（SSE 没有「自然返回」这回事，保持住就是成功）。
func runSSEProbe(ctx context.Context, cfg suspendedConfig) (*suspendedResult, error) {
	if cfg.Held < 1 {
		return nil, errors.New("held（同时保持的 SSE 连接数）必须 ≥ 1")
	}
	if cfg.Duration <= 0 {
		return nil, errors.New("Duration（探测时长）必须为正")
	}
	if cfg.Target == "" {
		return nil, errors.New("Target（SSE 端点）不能为空")
	}
	if cfg.AdminToken == "" {
		return nil, errors.New("AdminToken 不能为空：SuspendedObserved 与唤醒变更都在 admin 面，需要管理员令牌")
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	timer := time.AfterFunc(cfg.Duration, cancel)
	defer timer.Stop()

	client := newSuspendedClient(cfg.Held)
	defer client.CloseIdleConnections()
	holdWindow := suspendedHoldWindow(cfg.BaseURL, cfg.AdminToken, cfg.Target)
	probe := newSSEProbe(cfg, client, holdWindow)
	observer := newSuspendedObserver(cfg.BaseURL, cfg.AdminToken)
	defer observer.stop()

	// SSE 连上即对账：不带 configMd5 时首轮会把当前有效配置当增量补发（config-changed + ready）。
	// 这恰好给出「连接建立 → 首帧」的耗时，也就是 SSE 的 HoldLatency。
	baseline := max(observer.sample(runCtx), 0)
	probe.settle(runCtx, observer, baseline)
	observer.start(runCtx, suspendedObserveInterval)
	if cfg.WakeAt > 0 {
		probe.wg.Add(1)
		go probe.wakeLoop(runCtx)
	}
	probe.wg.Wait()

	res := probe.result
	res.SuspendedObserved = suspendedPeak(observer, res)
	res.HoldLatency = summarizeLatency(probe.firstFrame)
	res.WakeLatency = summarizeLatency(probe.wakeSamples)
	if ping := summarizeLatency(probe.pingSamples); ping.Count > 0 {
		// 无保活帧时留 nil：PingInterval 是 SSE 扩展字段，长轮询探测与短时 SSE 都不该出现空统计。
		res.PingInterval = &ping
	}
	return res, nil
}

// suspendedPeak 取自观测峰值；自观测失败次数回填进 ErrorKinds，避免「0 挂起」与「读不到」被混为一谈。
func suspendedPeak(observer *suspendedObserver, res *suspendedResult) int {
	peak, failed := observer.peakOf()
	for range failed {
		res.addKind("anomaly:observability_sample_failed")
	}
	res.Errors += failed
	return peak
}

// sseProbe 是 SSE 探测的运行态。
type sseProbe struct {
	cfg        suspendedConfig
	client     *http.Client
	deadline   time.Time
	reqTimeout time.Duration

	mu          sync.Mutex
	wakeAt      time.Time
	wakeIssued  bool
	firstFrame  []int64 // 每条连接「从发出到收到首帧（ready）」的耗时
	wakeSamples []int64 // 每条连接「变更发出 → config-changed 到达」的耗时
	pingSamples []int64 // 相邻两条 `: ping` 保活帧的间隔（每条连接各自成对，见 connLoop）
	result      *suspendedResult
	wg          sync.WaitGroup
}

// newSSEProbe 构造 SSE 探测运行态。
// 第三个参数保持与 newSuspendedProbe 同形（实际是挂起窗口，SSE 不返回故未使用）：
// 两个构造器签名一致，调用方换探测种类时不用改调用点。
func newSSEProbe(cfg suspendedConfig, client *http.Client, _ time.Duration) *sseProbe {
	return &sseProbe{
		cfg: cfg, client: client, deadline: time.Now().Add(cfg.Duration),
		reqTimeout: cfg.Duration + suspendedClientSlack,
		result:     &suspendedResult{Held: cfg.Held},
	}
}

// settle 逐条建立 SSE 连接。
// 逐条发：一条 SSE 连接会在四个 Hub 各登记一个 waiter，自观测计数一次跳 4，
// 逐条发才能把每次跳变归因到具体连接（也顺带验证「一条流 = 四个 waiter」这个实现细节）。
// 首帧耗时不在这里采样——那由 ready 事件自己报（见 absorbEvent），在两边都采会把每条连接算两遍。
func (p *sseProbe) settle(ctx context.Context, observer *suspendedObserver, baseline int) {
	last := baseline
	for range p.cfg.Held {
		if ctx.Err() != nil {
			return
		}
		p.wg.Add(1)
		go p.connLoop(ctx)
		observed := observer.waitRise(ctx, last, suspendedSettleTimeout)
		if observed <= last {
			p.absorbKind("anomaly:suspend_not_observed")
		}
		last = observed
	}
}

// connLoop 单条 SSE 连接的生命周期：建连 → 逐行读流 → 收尾归因。
func (p *sseProbe) connLoop(ctx context.Context) {
	defer p.wg.Done()
	reqCtx, cancel := context.WithTimeout(ctx, p.reqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, p.cfg.Target, nil)
	if err != nil {
		p.absorbKind("request_build_failed")
		return
	}
	req.Header.Set("X-Beacon-Token", p.cfg.Token)
	start := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return // 探测到点取消：收尾动作，不算连接失败
		}
		p.absorbKind(suspendedErrorKind(err))
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		p.absorbKind("http_" + strconv.Itoa(resp.StatusCode))
		return
	}
	p.mu.Lock()
	p.result.Connected++
	p.mu.Unlock()

	reader := bufio.NewReader(io.LimitReader(resp.Body, suspendedBodyLimit))
	// lastPing 按连接各自记：保活间隔是单连接的属性，多条连接共享一个「上一帧」会量出 0ms 假间隔。
	var lastPing time.Time
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			// 探测到点取消才算「保持成功」：SSE 没有自然返回，被我们主动收尾即达成目标。
			// 非取消的读失败就是连接被动断（EOF / 被代理切断），那才是失败。
			if ctx.Err() == nil {
				p.absorbKind(suspendedErrorKind(readErr))
				return
			}
			p.mu.Lock()
			p.result.CompletedByTimeout++
			p.mu.Unlock()
			return
		}
		p.consumeLine(strings.TrimRight(line, "\r\n"), start, &lastPing)
		if ctx.Err() != nil {
			p.mu.Lock()
			p.result.CompletedByTimeout++
			p.mu.Unlock()
			return
		}
	}
}

// consumeLine 解析一行 SSE 报文：`event:` 行走事件归因，`:` 开头是保活注释帧（不构成事件）。
func (p *sseProbe) consumeLine(line string, start time.Time, lastPing *time.Time) {
	switch {
	case strings.HasPrefix(line, ":"):
		p.absorbPing(lastPing)
	case strings.HasPrefix(line, "event:"):
		p.absorbEvent(strings.TrimSpace(strings.TrimPrefix(line, "event:")), start)
	}
}

// absorbEvent 归因一条 SSE 事件：ready 是首轮对账完成（首帧），config-changed 是配置热更推送。
func (p *sseProbe) absorbEvent(kind string, start time.Time) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	switch kind {
	case "ready":
		p.firstFrame = append(p.firstFrame, int64(now.Sub(start)))
	case "config-changed":
		if !p.wakeIssued || start.After(p.wakeAt) {
			// 没有唤醒却收到变更：可能是首轮对账补发（未带 configMd5 时正常），不算唤醒时延样本。
			return
		}
		p.result.CompletedByWake++
		p.wakeSamples = append(p.wakeSamples, int64(now.Sub(p.wakeAt)))
	}
}

// absorbPing 记一条保活帧的到达间隔（间隔应≈longpoll.max-hold-ms，见 docs/OPERATIONS.md §3.1）。
// 注意实测口径：stream 的保活定时器在每个事件后重新起算（StreamService.Run 的 select 每次新建
// pingTimer），所以「从上一条事件起算」才是真实间隔，发生唤醒后第一个保活帧会被推后一个周期。
func (p *sseProbe) absorbPing(lastPing *time.Time) {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	if !lastPing.IsZero() {
		p.pingSamples = append(p.pingSamples, int64(now.Sub(*lastPing)))
	}
	*lastPing = now
}

// absorbKind 记一次失败 / 探测异常。
func (p *sseProbe) absorbKind(kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.result.Errors++
	p.result.addKind(kind)
}

// wakeLoop 在 WakeAt 时刻触发一次配置变更，用于测「变更 → SSE 推送」的端到端时延。
func (p *sseProbe) wakeLoop(ctx context.Context) {
	defer p.wg.Done()
	timer := time.NewTimer(p.cfg.WakeAt)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return
	case <-timer.C:
	}
	if err := p.triggerWake(ctx, suspendedSSEDataIDPrefix); err != nil {
		p.absorbKind("anomaly:wake_change_failed")
		fmt.Fprintf(os.Stderr, "[loadtest] SSE 唤醒变更失败：%v\n", err)
	}
}

// triggerWake 发起一次真实配置变更（global scope，见 runSuspendedProbe 的同名方法说明），
// 记下「请求发出」时刻作为 SSE 唤醒时延的计时起点。
func (p *sseProbe) triggerWake(ctx context.Context, dataIDPrefix string) error {
	ns := suspendedQueryValue(p.cfg.Target, "namespace")
	if ns == "" {
		return errors.New("SSE 目标地址缺少 namespace 查询参数，无法发起唤醒变更")
	}
	marker := strconv.FormatInt(time.Now().UnixNano(), 36)
	body, err := json.Marshal(map[string]any{
		"namespace": ns, "group": suspendedGlobalGroup,
		"dataId": dataIDPrefix + "-" + marker, "scopeLevel": suspendedGlobalScope, "scopeTarget": "",
		"format": suspendedWakeFormat, "content": "marker: " + marker + "\n",
		"comment": "SSE 挂起探测唤醒（loadtest）",
	})
	if err != nil {
		return fmt.Errorf("编码唤醒配置失败：%w", err)
	}
	reqCtx, cancel := context.WithTimeout(ctx, suspendedChangeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		p.cfg.BaseURL+"/admin/v1/configs", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("构造唤醒配置请求失败：%w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.cfg.AdminToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", dataIDPrefix+"-"+marker)
	sent := time.Now()
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("唤醒配置请求失败：%w", err)
	}
	snippet, readErr := io.ReadAll(io.LimitReader(resp.Body, suspendedSnippetLimit))
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		return fmt.Errorf("读取唤醒响应失败：read=%v close=%w", readErr, closeErr)
	}
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("唤醒配置期望 %d，实际 %d；响应片段：%s",
			http.StatusCreated, resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.wakeAt = sent
	p.wakeIssued = true
	return nil
}
