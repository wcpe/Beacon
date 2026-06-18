# 功能规格：健康分级 + 失联告警（FR-28）

> 状态：开发中　·　关联 PRD：FR-28（增强 FR-5）　·　分支：feature/fr-28-health-alert

## 1. 背景与目标
FR-5 的健康状态机只有 `online → lost → offline`，对"心跳开始变慢但还没失联"的亚健康阶段无感知，且实例失联/状态异常时控制面**不主动告警**——运维只能靠轮询管理台发现问题（见 `docs/OPERATIONS.md` §3 提到的失联告警诉求）。

本 FR（P2，增强 FR-5）在状态机中引入 **degraded（亚健康）** 判定，并在实例状态异常（进入 degraded/lost/offline）时**主动告警**；告警通道做成可扩展抽象，第一版落 **站内信 + webhook** 两种，后续新通道只需实现接口即可接入。所有阈值可配。

## 2. 需求（要什么）
- 在 `online / lost / offline` 之外引入 **degraded**：心跳已变陈旧但尚未达 TTL → degraded（介于 online 与 lost 之间）。
- 状态机推进顺序（纯按心跳年龄）：`online → degraded → lost → offline`；收到心跳即回 online（任意状态恢复）。
- 阈值可配：`degraded-after-sec < ttl-sec < offline-grace-sec`，走控制面配置（`HealthConfig`），不硬编码。
- 实例进入异常状态（degraded/lost/offline）时主动告警；恢复（→online）不告警（避免噪音）。
- 告警通道抽象为接口 `Alerter`；第一版实现两种：
  - **站内信（inbox）**：进程内环形缓存，管理台可读（`GET /admin/v1/alerts`）。
  - **webhook**：HTTP POST 告警 JSON 到运维预设 URL。
- 多通道**扇出分发**：任一通道失败不影响其他通道，不阻断健康扫描。
- 范围内：状态机增 degraded、告警触发判定、`Alerter` 抽象 + 站内信 + webhook、扇出分发、阈值/通道配置、站内信只读 API。
- 不做（范围外）：告警去重/收敛/静默窗口、邮件/IM 等更多通道（留接口）、告警持久化落库（站内信仅进程内）、前端告警页 UI（仅后端 API，前端增强属 FR-18 系）、`canary`/灰度等 P2/P3 镀金能力。

## 3. 设计（怎么做）

### 3.1 健康状态机（`internal/runtime/registry.go`）
- 新增常量 `StatusDegraded = "degraded"`。
- `SweepExpired` 增 `degradedAfter time.Duration` 入参，按心跳年龄分档（**从严到宽**判定）：
  - `age > offlineGrace` → offline
  - `age > ttl` → lost
  - `age > degradedAfter` → degraded
  - 否则保持（online）
- 仍仅做"陈旧推进 + 恢复"，offline 条目保留不删（与 FR-5 一致）。锁内只改内存状态、返回变更快照；**告警分发在锁外**（由扫描器在 `SweepExpired` 返回后做）。

### 3.2 告警通道抽象（`internal/runtime/alert`）
- `Alert`：告警事件值对象（命名空间/serverId/旧态→新态/地址/时间/级别）。
- `Alerter` 接口：`Notify(ctx, Alert) error` —— 单一职责"投递一条告警到某通道"。新通道只实现此接口。
- `Dispatcher`：持有 `[]Alerter`，`Dispatch` 顺序扇出到各通道，**逐通道兜错**（某通道 error 仅 WARN 日志、继续下一个），不返回错误、不阻断扫描。
- `InboxAlerter`（站内信）：进程内固定容量环形缓存 + 独立 `RWMutex`（与三大运行态锁不嵌套），`List()` 返回快照供管理台读。
- `WebhookAlerter`：HTTP POST 告警 JSON，带超时；IO 在任何注册表锁之外（扫描器循环里调用）。

### 3.3 触发与装配
- `HealthScanner` 持有 `*alert.Dispatcher`；每轮 `SweepExpired` 返回的变更实例中，凡新态属异常集合（degraded/lost/offline）即 `Dispatch` 一条告警；恢复到 online 不告警。
- `cmd/beacon/main.go` 按配置构造站内信 + webhook（webhook URL 空则不挂该通道），注入扫描器；新增 `AlertHandler` + `GET /admin/v1/alerts`。

### 3.4 配置（`internal/config`）
- `HealthConfig` 增 `degraded-after-sec`（默认 15，介于心跳周期与 TTL 之间）。
- 新增 `AlertConfig`：`inbox-capacity`（站内信容量，默认 200）、`webhook`（`url` 空=禁用、`timeout-ms`）。
- 同步 `config.example.yml` 字段 + 中文注释。

> 本 FR 引入"控制面告警通道可扩展抽象"这一新扩展点，记 [ADR-0019](../adr/0019-health-alert-channel-abstraction.md)。

## 4. 任务拆分
- [ ] 测试先行：registry 增 online→degraded→lost→offline 流转用例（红）
- [ ] 测试先行：alert 包扇出/站内信/webhook/触发判定用例（红）
- [ ] 实现：`StatusDegraded` + `SweepExpired` 分档
- [ ] 实现：`alert` 包（Alert/Alerter/Dispatcher/InboxAlerter/WebhookAlerter）
- [ ] 实现：`HealthScanner` 接告警分发
- [ ] 实现：配置项 + 装配 + `AlertHandler` + 路由
- [ ] 文档同步：ARCHITECTURE §7、API.md、ADR-0019、CHANGELOG、config.example.yml

## 5. 验收标准
- `go test ./internal/runtime/... ./internal/config/...` 全绿，覆盖：
  - 状态机 online→degraded→lost→offline 全程流转 + 心跳从任意异常态恢复 online。
  - 告警仅在进入异常态触发、恢复 online 不触发。
  - 多通道扇出：一个通道失败不影响其他通道、不 panic。
  - 站内信环形容量上限生效、`List` 返回快照不被外部篡改。
- 受影响组件（控制面 Go）编译通过、`go vet` 干净。

## 6. 风险 / 待定
- degraded 当前仅按"心跳陈旧度"判定；TPS/玩家数等运行指标参与亚健康判定属后续增强（FR-30 可观测性方向），本期不做（指标仅展示、不参与决策，守架构不变量）。
- 告警无去重/静默，频繁抖动可能多次告警；本期接受（运维可后续加收敛通道），不提前镀金。
