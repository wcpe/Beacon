# 功能规格：可观测性（Prometheus 指标 + 审计查询增强）

> 状态：开发中　·　关联 PRD：FR-30（增强 FR-7）　·　分支：feature/fr-30-observability

## 1. 背景与目标

控制面已有审计落库（FR-7）与注册/健康内存真源，但缺少两类可观测能力：
- 运维侧无法用标准监控体系（Prometheus）拉取控制面运行指标（注册数、健康分布、配置发布与推送量）。
- 审计虽落库且可查，但 FR-7 的查询缺少「按操作者」检索维度，无法回答「某人做过哪些操作」。

本功能属 P2，在不引入重型中间件的前提下补齐上述两点，对齐架构不变量（简单优先、可移植、分层）。

## 2. 需求（要什么）

### 2.1 Prometheus 运行指标
- 控制面 HTTP 暴露 `GET /metrics`（Prometheus 文本格式），用标准 prometheus client（`prometheus/client_golang`）。
- 指标覆盖：
  - 注册实例数（按 namespace、role 维度）。
  - 健康状态分布（online/degraded/lost/offline 各计数）。
  - 配置发布累计次数。
  - 推送（长轮询唤醒）累计次数。
- 注册/健康类为 gauge，发布时从内存注册表实时采集（pull 模型，不在写路径埋点）。
- 发布/推送类为 counter，在事件发生处自增。

### 2.2 审计查询增强（按操作者）
- `GET /admin/v1/audits` 新增 `operator` 过滤参数，与既有 `namespace/action/targetType/targetRef/from/to` 过滤、分页、时间倒序并存。

- 范围内：上述两点。
- 不做（范围外）：Grafana 面板/告警规则、指标持久化、链路追踪（Tracing）、`/metrics` 鉴权（内网信任，与现有运行约定一致）。

## 3. 设计（怎么做）

### 3.1 指标包 `internal/metrics`
- 持私有 `*prometheus.Registry`（独立注册表，不污染全局 default），集中注册全部 collector。
- 注册/健康 gauge 用自定义 `prometheus.Collector`，`Collect` 时读取 `runtime.Registry` 快照即时计数（无写路径耦合）。
- `config_publish_total`、`push_notify_total` 为 counter，由 `metrics.Metrics` 暴露自增方法。
- `Handler()` 返回 `promhttp.HandlerFor(reg, ...)`，由 router 挂到 `/metrics`（agent/admin 两套之外的运维端点，不挂鉴权中间件）。

### 3.2 计数解耦
- service 层不直接依赖 metrics 具体类型：在 service 包定义窄接口（发布计数器 / 推送计数器），`*metrics.Metrics` 实现之，启动时可选注入（未注入即 no-op，保持单测无依赖）。
- 配置发布在 `ConfigService.Publish` 自增发布计数；推送在 `ChangeNotifier` 唤醒处自增推送计数（推送唯一汇聚点）。

### 3.3 审计查询
- `repository.AuditFilter` 增 `Operator` 字段，`List` 增 `operator = ?` 过滤。
- handler 从 query 读 `operator` 透传。service 层不变（仅透传）。

引入标准 Prometheus client 属架构决策，见 [ADR-0020](../adr/0020-prometheus-metrics-observability.md)；本规格不重复决策正文。

## 4. 任务拆分
- [x] ADR-0020：引入 prometheus client 暴露 /metrics 的决策
- [x] `internal/metrics`：registry + collector + counters + Handler（含单测）
- [x] 审计 `Operator` 过滤（repo/handler + 单测/集成测）
- [x] 装配：main 注入 metrics、router 挂 /metrics、service 可选注入计数器
- [x] 文档同步：API、ARCHITECTURE、CHANGELOG

## 5. 验收标准
- `GET /metrics` 返回 200 且文本含 `beacon_instances_registered`、`beacon_instances_status`、`beacon_config_publish_total`、`beacon_push_notify_total`。
- 注册一个实例后采集，`beacon_instances_registered` 对应 namespace/role 计数 +1；状态 gauge 反映 online。
- `GET /admin/v1/audits?operator=alice` 仅返回 alice 的审计；与其它过滤可叠加。
- 控制面 `go test ./...` 全绿；集成测试（带 DSN）审计 operator 过滤通过。

## 6. 风险 / 待定
- 新增第三方依赖 `prometheus/client_golang`（FR-30 明确要求「标准 prometheus client」）。
- /metrics 不鉴权：与 agent 端点同属内网信任面，若后续要求鉴权再走新 ADR。
