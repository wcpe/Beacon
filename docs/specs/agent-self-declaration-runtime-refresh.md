# 功能规格：节点自声明的运行期刷新与读回

> 状态：开发中（代码已实现并通过验证门；待合并与真机验收）　·　关联 PRD：FR-243　·　分支：feature/agent-self-declaration　·　依赖：ADR-0086（决策）/ FR-227（标签约束）/ FR-228（容量口径）　·　纯后端（无 UX 段）

## 1. 背景与目标

节点的声明类事实（容量、自定义键值标签）**目前只能在注册那一刻写入一次**：`AgentIdentity` 的 `capacity` / `weight` / `metadata` 在 agent-core 里是 `val`，bukkit 壳在 enable 期反射读 `max-players` 注入；服务端 `registry.Register` 是整条覆盖，`Report` 刷新 9 个指标字段却不含它们，`Heartbeat` 只刷 `LastHeartbeat`。

接入方需要一个**运行期**通道：在不重启、不重新注册的前提下，刷新**自己节点**的声明，并能拿到**可区分**的结论（"通道不可用"与"声明被拒"处置完全不同）。本规格把这条路做成**通用的窄写入面**——Beacon 只承载"节点声明了哪些 key=value"，**不解释任何 key 的业务语义**（区 / 模式 / 放量范围等一律由接入方自行解释，见 ADR-0086 §5）。

## 2. 需求（要什么）

### 2.1 做什么

1. **运行期声明刷新**：节点可刷新 ① 容量（`capacity`）② 自定义键值标签（`labels`），二者**各自可选、缺键即不刷新**。
2. **幂等**：重复上报 = 刷新，**不产生第二条记录**（不新增实例、不追加行、不叠加）。
3. **结论可区分**：`通道不可用`（可重试）与 `声明被拒`（稳定事实，改正后重报）在**取值层面**分开；agent 门面同构，不得用同一个返回值承载两类。
4. **可读回**：刷新后立即可在实例视图读到新值（复用既有字段 `capacity` / `metadata`，**不新增读端点**）。
5. **有界**：标签沿用 FR-227 的既有约束（`key ≤ 32` / `value ≤ 128` / 单节点 `≤ 20`），超界即"声明被拒"。
6. **向后兼容**：不下发该端点的旧 agent 行为与今天**逐字一致**（注册时的 `capacity` / `metadata` 语义不变）。

### 2.2 明确不做（范围外）

- **不做业务语义**：不定义任何标签 key 的含义，不做"区列表 / 玩法模式 / 灰度放量"这类业务概念（ADR-0086 §5）。
- **不碰 zone 权威**：声明里不含"我在哪个区"；zone 仍由控制面 DB 权威指派（ADR-0004 / 不变量 §6）。
- **不开放改配置 / 写他人**：配置内容与 zone 分配不可经此写入；只能写**自己**（请求体自报的 `namespace` / `serverId` 只用于定位，身份以中间件与在册判定为准）。
- **不合并 `server_tag`**：控制面标签（FR-227，管理面写、`?tag.*=` 过滤真源）与"节点自声明"是**两个不同事实**，本次不做同步、不做双写。
- **不新增第二真源**：声明**不进入**调度决策、健康打分与发现侧标签过滤；控制面只存不判。
- **不承诺跨重启持久**：声明是内存事实，随注册重建（与控制面"注册/健康 = 进程内存"的真源切分一致，不变量 §3）。
- **不做前端页面**：本次只交付端点与门面能力，管理台展示沿用既有实例视图字段。

## 3. 设计（怎么做）

### 3.1 端点契约（服务端）

`POST /beacon/v1/agent/declaration`——挂在既有 `/beacon/v1/agent` 组内（继承 `agentTokenMiddleware` 与受信内部标记），**不新增中间件、不新增路由组**。

请求体（两个声明字段均可缺省）：

```json
{
  "namespace": "string",
  "serverId": "string",
  "capacity": 100,
  "labels": { "k": "v" }
}
```

- `capacity`：缺键 = 不刷新；提供即覆盖（`0` 是合法值，须与"缺键"区分——用指针）。
- `labels`：缺键 = 不刷新；提供即**整体替换**（空对象 `{}` = 清空全部标签）。
- 两个字段**都缺** → 拒绝（无意义的空操作）。

响应体（200，回带**生效后**的值自证）：

```json
{ "ok": true, "capacity": 100, "labels": { "k": "v" } }
```

契约真源：本规格 §3.1 与 §3.4；`docs/API-v1-legacy.md` 的 agent 面章节加索引与错误码表。

### 3.2 数据模型与部分刷新语义（服务端）

- `runtime.Registry` 新增写方法（照 `SetBackends` 的锁纪律与深拷贝）：
  `SetDeclaration(ns, serverID string, capacity *int, labels map[string]string) bool`——未注册返回 `false`；只动 `Capacity` 与 `Metadata` 两个字段，**不碰** `Status` / `Resolved*` / `LastHeartbeat` / `Backends` / 指标字段。
- 不新增表、不落 DB（真源切分不变）。
- `service.InstanceService` 新增 `Declare`（流程照 `Report`）：身份与格式校验 → 在册校验（归档 / 失活 / 未注册一律按 §3.4 拒绝）→ `registry.SetDeclaration` → 返回生效值。
- **不写审计**（与 `Report` 同口径：这是节点自身的运行期事实，不是管理面写操作）；拒绝路径记 WARN 日志（含 serverId 与原因）。

### 3.3 agent 侧（agent-api / agent-core）

**契约层（`agent-api`，纯 Java 8、零依赖）**：

- `NodeDeclaration`（值对象）：`Optional<Integer> capacity()`、`Map<String,String> labels()`（不可变）。
- `DeclarationOutcome`（结果，能区分三类）：`Status { APPLIED, REJECTED, UNAVAILABLE }` + `applied()`（`APPLIED` 时回带生效值）+ `rejectReason()`（`REJECTED` 时给原因）。
- `SelfDeclaration`（门面）：`DeclarationOutcome declare(NodeDeclaration declaration)`。
- `BeaconAgent` 新增 `SelfDeclaration declaration()`（始终非 null；未就绪时返回降级实现，返回 `UNAVAILABLE`——与 `messaging()` / `scheduling()` 的既有范式一致）。

**实现层（`agent-core`）**：

- `NodeDeclarationHolder`（照 `RosterDirectoryHolder`）：装配期注入降级实现，**注册成功后**由生命周期钩子换为真实实现，停机 `reset()`。
- `BeaconApiDeclaration`（HTTP 落点）：POST `/beacon/v1/agent/declaration`，结果映射 `200 → APPLIED`、`400 → REJECTED`、`401/404 → UNAVAILABLE`、连接失败 → `UNAVAILABLE`。
- **启动竞态**：若声明调用发生在"已装配但尚未注册成功"时，core 记住**最近一次**声明企图，并在注册成功后**自动补报一次**（幂等，重复补报无副作用）——避免接入方在插件启动早期声明丢失。
- 不在 MC 主线程阻塞：调用方在异步线程发起（门面注释写明，同 `Discovery.query` 的既有约定）。

**壳层**：无需新配置项；`BeaconAgentProvider` 暴露的门面即自动带上新方法（`AgentAssembly` 装配透传）。

### 3.4 失败分类（两类结论）

| 场景 | 服务端 | agent 门面 | 处置 |
|---|---|---|---|
| token 缺 / 错 | `401 UNAUTHORIZED` | `UNAVAILABLE` | 可重试（先修凭据） |
| 尚未挂载数据面 / 实例不在册 | `404 NOT_REGISTERED` | `UNAVAILABLE` | 可重试（退避后重报） |
| 请求体格式非法 / 两字段全缺 | `400 INVALID_PARAM` | `REJECTED` | 改正后重报 |
| 声明超界（key 格式 / 长度 / 数量 / capacity < 0） | `400 INVALID_PARAM` | `REJECTED` | 稳定事实，改正后重报 |
| 身份与在册归属不符（写他人） | `400 INVALID_PARAM`（`namespace` / `serverId` 校验）+ 在册判定 `404` | `REJECTED` / `UNAVAILABLE` | 不可达 |

不新增错误码（复用既有分面即可区分两类结论）；若实现中发现需要把"声明内容被拒"与"请求体格式非法"进一步分开，再单独评估（不在本规格内提前预留）。

### 3.5 读回与"不新增真源"（负向边界）

- 读回路径：`GET /beacon/v1/agent/discovery` 与 `/admin/*` 实例视图（二者共用 `toInstanceViews`）——既有 `capacity` / `metadata` 字段即读回面。
- **负向（必须有断言）**：声明刷新后，调度候选（`/beacon/v2/agent/schedule/*`）、健康打分、`?tag.*=` 标签过滤的**输入与结果**均不受影响（它们各自读既有真源）。

## 4. 任务拆分

- [x] 服务端：`registry.SetDeclaration`（部分刷新 + 幂等 + 深拷贝）+ 单测
- [x] 服务端：`POST /beacon/v1/agent/declaration`（handler + service + 校验 + 错误分面）+ 单测 / 集成测试
- [x] `agent-api`：`NodeDeclaration` / `DeclarationOutcome` / `SelfDeclaration` + `BeaconAgent.declaration()`
- [x] `agent-core`：`BeaconApiDeclaration` + `NodeDeclarationHolder` + 注册后补报 + 单测
- [x] 壳层接线（`AgentAssembly` 透传；两壳不出新配置）
- [x] 文档同步：PRD 状态、ARCHITECTURE（agent 边界一句）、API（端点索引 + 错误码）、`.claude/rules/architecture-invariants.md` §1（已随 ADR-0086 修订）、CHANGELOG

## 5. 验收标准

- **运行期刷新**：注册后调 `declaration`，实例视图（admin 与 discovery）**立即**读到新的 `capacity` / `labels`；无需重启、无需重新注册。
- **幂等**：同一请求连续上报 N 次，实例数不变、标签不叠加、结果与一次上报一致（集成断言 + 单测）。
- **部分刷新**：只给 `capacity` 时 `labels` 不变；只给 `labels` 时 `capacity` 不变；`capacity = 0` 生效（不与"缺键"混淆）。
- **两类结论可区分**：未注册 → `UNAVAILABLE`（服务端 404）；超界声明 → `REJECTED`（服务端 400）；两者的门面取值与服务端状态码均有断言。
- **负向（不可达面）**：改 zone（声明里无 zone 字段、请求体不允许携带）、改配置（无对应字段与端点路径）、写他人（`namespace` / `serverId` 与实际在册身份不符被拒）——三者各有断言。
- **不新增第二真源**：声明刷新后调度候选、健康打分、`?tag.*=` 过滤结果**逐项不变**（负向断言）。
- **向后兼容**：不下发该端点的旧 agent 行为与今天逐字一致（注册路径回归全绿）；缺失键语义不破坏既有 `Register` / `Report` 契约。
- **构建门**：`go build ./...` + `go test ./...` + `make lint` 全绿；agent 侧 `./gradlew ktlintCheck detekt test` 全绿。

## 6. 风险 / 待定

- **风险：把声明当权威**。接入方若把声明拿去做调度判定，会形成第二真源（违背不变量 §3 / ADR-0075 的"禁第二落脚真源"）。本规格在 §2.2 与 §3.5 明文禁止，并以负向断言守护；文档（本规格 + ADR-0086）须保持同一口径。
- **风险：内存事实随重启丢失**。这是与控制面真源切分一致的**有意取舍**（与控制面"注册/健康 = 进程内存"同源）；接入方需在注册成功后重放自己的声明（core 已提供自动补报一次；更复杂的重放策略不在本次范围）。
- **待定：是否需要"声明读取面"作为门面方法**（业务插件读回自己声明的当前值）。当前设计只开放写 + 由实例视图读回；若接入方需要"程序化读回自己的声明"，作为后续增强评估，不提前预留。
- **待定：标签与 `server_tag` 的关系**。本次明确不合并（§2.2）；若后续确需"节点声明可被 `?tag.*=` 过滤"，那是一次**真源合并**决策，须另写 ADR。
