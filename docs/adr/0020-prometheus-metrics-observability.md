# ADR-0020：控制面用标准 Prometheus client 暴露运行指标

**状态**：已接受

## 背景

FR-30 要求控制面导出运行指标（注册数、健康状态分布、配置发布与推送量）供运维监控。控制面已有 slog 中文分级日志与审计落库，但日志面向人读、审计面向操作回溯，二者都不便于被监控系统按时间序列拉取与告警。需要一条标准化的指标出口。

## 决策

控制面引入 **`github.com/prometheus/client_golang`**（事实标准的 Go Prometheus 客户端），在自身 HTTP 同端口暴露 **`GET /metrics`**（Prometheus 文本格式）。

- 指标注册集中在 `internal/metrics` 包，持**独立** `*prometheus.Registry`（不挂全局 default registry，避免隐式全局态）。
- 注册/健康类为 **gauge**，用自定义 `Collector` 在抓取时读取内存注册表快照即时计数——**不在注册/心跳/扫描写路径埋点**，保持运行态写路径零侵入。
- 配置发布、推送（长轮询唤醒）为 **counter**，在事件处自增；service 层经窄接口可选注入计数器，未注入即 no-op。
- `/metrics` 与 agent 端点同属内网信任面，**不挂管理台鉴权中间件**。

## 理由

- Prometheus 文本协议是运维监控的事实标准，直接被 Prometheus/VictoriaMetrics/Grafana 等消费，无需自造格式。
- pull 模型让 gauge 在抓取时现算，注册/健康的内存真源（[ADR-0003](0003-no-redis-in-mvp.md)）天然适配，避免在三把运行态锁的写路径里加指标副作用。
- 独立 registry + 包内集中注册，避免 `prometheus.MustRegister` 全局副作用导致的测试间串扰与隐式耦合。
- 仅一个纯客户端库、无新增运行期组件（不违反「简单优先/禁重型件」：未引入 Pushgateway、Exporter 进程、Redis/MQ）。

## 后果

- 新增一个第三方依赖 `prometheus/client_golang`（及其传递依赖）。这是 FR-30「用标准 prometheus client」的直接要求。
- `/metrics` 无鉴权：暴露的是聚合运行指标、不含配置内容或凭据；若后续要求受控访问，另写新 ADR 取代本条的鉴权约定。
- 指标不持久化，控制面重启计数清零——符合 counter 语义（监控侧按 rate/increase 计算），无需持久化。

## 备选方案

- **自造 JSON 状态端点**：要运维侧自写采集与解析，偏离监控生态标准。被否。
- **OpenTelemetry SDK**：功能更全（含 tracing/metrics 统一），但对「仅导出几个运行指标」而言依赖与心智负担过重，属过度工程。被否（如未来需要分布式追踪再评估）。
- **挂全局 default registry + promhttp.Handler()**：省事但引入全局可变态、测试相互串扰。被否，改用独立 registry。
