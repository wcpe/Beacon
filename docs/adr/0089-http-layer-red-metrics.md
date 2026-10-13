# ADR-0089：控制面 HTTP 层 RED 指标（路由模板分维的请求计数与时延直方图）

**状态**：已接受；扩展 [ADR-0020](0020-prometheus-metrics-observability.md)（独立 registry 约定沿用，不改其正文）。**前言的「同端口 / 不挂鉴权」两条后被 [ADR-0090](0090-metrics-endpoint-dedicated-listener.md) 取代**（抓取端点改独立监听、默认回环，并可选用 Bearer 鉴权）——本 ADR 的全部决策（两个 RED 指标、显式分桶、`route` 取路由模板、中间件顺序、窄接口注入）仍然有效且不受影响。

> 本 ADR **扩展 [ADR-0020](0020-prometheus-metrics-observability.md)、不取代其任何决策**：独立 registry 一条沿用；**「同端口」与「不挂鉴权」两条当时沿用的约定，后由 [ADR-0090](0090-metrics-endpoint-dedicated-listener.md) 取代**（抓取端点在本次 RED 指标落地之后迁到独立监听端口、默认只绑回环，并新增可选 Bearer 鉴权）——那只改变抓取端点的挂载位置与访问控制，不改本 ADR 新增的指标与统计口径。ADR-0020 的正文保持不变（其状态由 ADR-0090 更新）。

## 背景

FR-30 引入的 `/metrics`（[ADR-0020](0020-prometheus-metrics-observability.md)）导出的全是「平台内部发生了什么」的聚合事实：`beacon_instances_registered` / `beacon_instances_status` 反映注册与健康（抓取时读内存注册表现算），`beacon_config_publish_total` / `beacon_push_notify_total` 反映配置发布与推送量。

**这套指标里没有任何 HTTP 请求维度**，于是运维看得到「有多少实例在线」，看不到「接口有多慢、错多少」：

- 算不出 p95 / p99，也就无法对延迟设告警——「控制面变慢」只能靠管理台体感或事后翻日志，且分不清是某个端点退化还是整体退化。
- 看不到 5xx 计数与错误率：接口在报错这一事实在指标上完全没有出口，只能与平台内部事件（实例失联、配置漂移）混在同一处排查。
- 控制面本身承载**最长 30s 的长轮询**（`/beacon/v1/agent/config/effective`、`/files/manifest`、`/override-sets`，挂起上限取 `longpoll.max-hold-ms`）与**更长的流式传输**（`/beacon/v1/agent/stream`、管理台 SSE），整体耗时口径天然偏大——只有按路由分维才读得出有意义的结论。

## 决策

在既有独立 registry、既有 `GET /metrics` 端点（**不新增端口、不改鉴权**）上新增 HTTP 层 RED 指标，由全局中间件逐请求观察：

1. **请求计数**：`beacon_http_requests_total{method,route,status}`（counter）。
2. **请求时延**：`beacon_http_request_duration_seconds{method,route}`（histogram，单位秒），**显式分桶** `0.001 / 0.005 / 0.01 / 0.025 / 0.05 / 0.1 / 0.25 / 0.5 / 1 / 2.5 / 5 / 10 / 30 / 60`。
3. **`route` 标签一律取 chi 路由模板**（如 `/admin/v2/config-files/{id}`），**不得取真实请求路径**；未命中任何模板时（404 / 405 / SPA 回退）归一成固定值 `unmatched`。
4. **中间件置于 `recoverMiddleware` 外层**：`httpMetricsMiddleware → recoverMiddleware → traceMiddleware → accessLog`。这是硬约束而非风格偏好——`recoverMiddleware` 捕获 panic 后用**它自己那一层**的 `w` 写 500，只有位于它外层的包装器才看得到这次状态码写入；顺序反了会让 panic 产生的 500 静默漏计（指标看着平平稳稳、服务实际在 500）。有单测锁定该顺序。
5. **关联方式沿用 ADR-0020 的窄接口可选注入**：server 包只依赖 `HTTPMetricsObserver`（不 import metrics 包，保持分层单向），未注入即 no-op；路由模板在 `next.ServeHTTP` **返回之后**读取（chi 是在匹配过程中把 `RoutePattern` 写进 `RouteContext` 的）。

## 理由

- **事实标准，无需自造统计口径**：`histogram_quantile` 是 Prometheus 生态算分位数的既有做法，告警表达式也直接复用同一套 PromQL，不必由控制面自己实现（也就不用为近似分位数算法背书）。
- **显式分桶到 60s 是必需的**：默认分桶止于 10s，而控制面存在 30s 长轮询与更长的流式传输——这些观测值会全部堆进 `+Inf` 桶，`histogram_quantile` 算出的 p95 / p99 被上界拉平、完全失真。分桶自 1ms 起步到 60s 收尾，兼顾毫秒级内存读与挂起级端点两种形态。
- **路由模板而非真实路径**：真实路径含 `id` / `serverId` / `sha256` 等高基数字段，每个取值都会生成一条新时序，会瞬间打爆 Prometheus 的时序数；`unmatched` 兜底让「未匹配」本身也是一个**有界**取值，避免它退化成往真实路径上兜。
- **与 ADR-0020 的「运行态写路径零侵入」不冲突**：那条针对的是注册 / 心跳 / 扫描这类**运行态内存写路径**（抓取时现算即可，无需埋点）；HTTP 请求指标天然只能在请求发生的那一刻观测——状态码与耗时事后无从还原，属**新增的指标类别**，不是对既有 gauge 写路径的埋点。ADR-0020 的 pull 模型与 counter 语义均不受影响。

## 后果

- **抓取流量会被自记**：`/metrics` 的 scrape 本身也是一次 HTTP 请求，会记进 `beacon_http_requests_total{route="/metrics"}`。做 QPS 结论时必须剔除它，否则读出的「业务流量」里含监控自身的开销。
- **鉴权 / 只读拒写类请求的 `route` 是「组通配」**：chi 在组内中间件（鉴权、只读拒写裁决）短路时路由尚未完全匹配，`route` 会停在 `/admin/v2/*`、`/beacon/v1/agent/*` 这类**组级模板**。基数仍有界，但看板上会多出这类 series——这是中间件短路的正常表现，别误判成路由模板漂移。
- **无子样本时不输出**：vec 型指标在产生第一个子样本前不会出现在 `/metrics` 里，故进程刚启动、尚无请求时看不到这两个 HTTP 指标（首个请求之后才出现）。
- **每请求常数开销**：一次标签查找 + 一次直方图观察；未注入 observer 时中间件直接放行、零开销。本批同落的 Go 微基准与端到端压测 harness 用于量化该开销，原始报告落 `.tmp/`（不入库）。
- 长轮询 / SSE 端点的耗时是**真实挂起时长**（最长 30s、流式更长），会抬高整体分位数——做接口 SLA 时按 route 过滤掉它们（读法提醒已写入 [OPERATIONS](../OPERATIONS.md) §3.2）。

## 备选方案

- **自造 JSON 时延统计端点**：要监控侧另写采集与解析，分位数改由控制面自己算（口径不可控，且不能与其它被监控服务共用同一套 PromQL / 告警表达式），偏离监控生态标准。否决。
- **用 `promhttp.InstrumentHandlerDuration` 包一层**：它接受调用方传入的 `ObserverVec`（分桶确实可自定义），但**自带的动态标签只有 `code` / `method`**——`route` 得再挂 `WithLabelFromCtx` 一类的 option 从上下文间接取值；而本决策真正要自己写的那段（路由模板取值 + `unmatched` 兜底归一）它并不代劳，等于同一件事拆到 helper 与 option 两处装配；中间件顺序（必须在 `recoverMiddleware` 外层）这个真正的坑它一点没兜住，标签形态不合规还会在启动期以 panic 形式暴露（`checkLabels` 借临时 registry `MustRegister` 校验）。收益为负。否决。
- **给每个 handler 手工埋点**：需在每个 handler 内自行写开始 / 结束与状态码，写路径侵入，且新增端点极易漏埋——漏埋不报错、指标静默缺口最难发现。否决。
- **`route` 标签直接用 `r.URL.Path`（真实路径）**：省掉读路由模板这一步，但 id / serverId / sha256 每个取值生成一条时序，基数随业务数据增长而无界膨胀，且无法与路由清单对齐。否决。
