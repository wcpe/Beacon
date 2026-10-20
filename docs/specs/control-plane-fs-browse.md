# 功能规格：控制面文件浏览端点（FR-110）

> 状态：开发中　·　关联 PRD：FR-110（依赖 FR-109）　·　分支：master（feat 直接进 master）
>
> **§3.1–§3.3 的审批与一次性授权口径已被 [ADR-0093](../adr/0093-direct-sensitive-content-reads.md) 取代**：浏览正文改为 `content.read` + `management.read` 直读并强制专项审计，`POST` 直接创建命令、旧 `GET` 改为按 `commandId` 读结果。下述正文已按现状改写，历史设计（pending grant → 激活 → 原申请主体一次消费）仅作为演进记录保留在 §6。

## 1. 背景与目标

FR-109 已落地 agent 侧只读交互式浏览原语（`FsBrowseReader.listDir / readTree / readFile`，经
`PlatformAdapter.browse*` 暴露），但**只是被调用的原语，agent 自身不调度**。FR-110 把这套浏览能力接进
控制面↔agent 的命令通道两端：控制面新增 admin 只读端点，复用既有 `agent_command` 生命周期下发浏览命令、
agent 收命令调原语读盘回传、控制面把结果代理给前端。

它是 FR-111 配置工作台双面板右侧「实时浏览在线服 plugins」的控制面底座。本期只做后端能力，UI 不接
（FR-111 才 surface）。属 P2。依据 [ADR-0049](../adr/0049-agent-fs-browse.md) 决策 9（FR-110 不另立 ADR）。

## 2. 需求（要什么）

- 控制面新增 admin **只读**浏览端点：列目录 / 读子树 / 读单文件，代理目标在线 agent 的 `plugins/` 浏览。
- 复用 [ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md) /
  [ADR-0037](../adr/0037-reverse-fetch-managed-task.md) 的 SSE 唤醒 + `agent_command` 生命周期
  （FR-104 `pending → fetched → done / failed / expired`）；**不新增传输、不直连 agent**。
- 鉴权：admin `full` 角色可触发；`readonly` → 403（扩展 [ADR-0026](../adr/0026-runtime-api-keys-and-readonly-role.md)）。
- 触发与结果**入审计**：记「谁 / 何时 / 浏览哪台的哪路径 / 哪种操作」；**文件内容绝不入审计 detail**。
- agent 命令处理器识别新浏览命令类型，async 调 `FsBrowseReader`（经 `PlatformAdapter.browse*`）读盘回传；
  旧 agent 收到未知命令按既有逻辑忽略（向后兼容）。
- 范围内：列目录（分页）/ 读子树（逐层有界）/ 读单文件（受单文件上限）三种只读操作的端到端代理。
- 不做（范围外）：UI（FR-111）；写盘 / 任何改盘旁路（浏览纯只读）；新命令传输通道；缓存 / 预取。

## 3. 设计（怎么做）

### 3.1 命令模型（复用 agent_command 生命周期）

新增命令类型 `fs-browse`（`model.CommandTypeFsBrowse`），与 `ingest-plugins` / `tail-logs` / `resync-config`
平行。载荷 `browsePayload{ op, path, offset, limit, maxDepth }`（`op` ∈ `list` / `tree` / `file`）。

浏览命令由 `POST` 直接创建并入队，不在 HTTP 请求中等待 Agent：`POST /browse` 建 `fs-browse` 命令、写审计 → `NotifyCommand`
唤醒 agent SSE → agent 拉命令、async 读盘并回传 → 控制面在同一事务内转存瞬态结果、CAS `fetched → done`。之后任一经
`management.read` 校验的主体用 `GET /browse?commandId=` 或兼容路径 `grants/{grantId}/consume` 读取一次；读取事务内清空
瞬态正文并写专项审计。旧 `GET` 不再创建申请或命令，缺 `commandId` 返回 `400`。按 [ADR-0093](../adr/0093-direct-sensitive-content-reads.md)，
本端点属 `content.read` 直执，**不创建审批申请、不签发或消费审批 grant**。

Agent 回传须通过既有 v2 `X-Beacon-Token`、`X-Beacon-Identity`、`X-Beacon-Boot` 权威身份校验，控制面只接受
与命令 `namespace/serverId` 一致的身份，不信任请求体自报的归属。Agent 失败原因统一归约为安全枚举摘要，绝不保存
或回显原始原因。

agent 回传内容（目录清单 / 子树 / 文件内容）是受控瞬态：转存到 `agent_command.browse_result`（新增 TEXT 列，
与 `imprint_content` / `log_content` 同范式——瞬态、done 后即可清、过期清理一并抹除、不入审计 detail、不导出 git）。

### 3.2 分层（router → handler → service → repository）

- **handler**（`BrowseHandler`）：`POST /admin/v1/instances/{serverId}/browse` 直接创建浏览命令并返回
  `202 {commandId,status}`；`GET /admin/v1/instances/{serverId}/browse?commandId=` 按命令读取一次结果；
  `POST /admin/v1/instances/{serverId}/browse/grants/{grantId}/consume` 为兼容路径（`grantId` 仅作定位器）。
  handler 解析受限参数、校验目标在线（`InstanceService.Get`），不碰 GORM / 内存结构。
- **service**（`RequestBrowse` + `ReceiveBrowseResult` + `ReadBrowseResult`）：`RequestBrowse` 建命令并在事务内写
  审计；`ReceiveBrowseResult` 原子转存结果；`ReadBrowseResult` 在同一事务内读结果、清空瞬态正文并写元数据审计
  （审计写失败则不返回正文）。`ConsumeApprovedBrowse` 仅保留为兼容入口。
- **repository**：`UpdateStatusWithBrowseResult`（fetched→done 转存结果）+ `ClearBrowseResult`（读后清理）；
  `ExpireStale` 一并清 `browse_result`。

### 3.3 鉴权与审计

- 浏览读取按 `content.read` + `management.read` 直执（[ADR-0093](../adr/0093-direct-sensitive-content-reads.md)）：缺该能力 `403`；
  `GET` 缺或非法 `commandId` `400`；旧 `GET` 不再固定返回 `409 operation_requires_approval`。
- 建命令入队记 `ActionFileBrowse`（`file.browse`），target=command/serverId，detail 仅 `{commandId, op, path}`；
  读取另有专项审计（`{commandId, serverId, op, path, bytes}`）。结果内容、目录名与文件正文均不写审批、审计或日志。
- 读结果在单个事务内读取正文、清空 `browse_result`、写审计；清空失败则整笔回滚。未被读取的结果按独立有效期
  （5 分钟）由命令清理器抹除，命令元数据保留。

### 3.4 agent 侧

`ReverseFetchExecutor.runOnce` 增 `fs-browse` 分支 → `runBrowse(command)`：按 `op` 调
`adapter.browseListDir / browseReadTree / browseReadFile`，结果经新回传端点
`POST /beacon/v1/agent/files/browse-result`（`uploadBrowseResult`）回传；原语返回 null（越权 / 非目录 / 非文本）
→ 回传 `{ok:false, reason}`，控制面 CAS failed。未注入浏览能力（壳层 browse* 返回 null）→ 回 failed，
控制面据此 404 / 502，fail-static 不影响主流程。`browse-result` 命令载荷的 `op` 经 `IngestCommandPayload`
扩展字段解析（agent 复用同一命令数据类，加 `op/offset/limit/maxDepth` 可选字段）。

## 4. 任务拆分

- [x] 控制面：新增 `fs-browse` 命令类型 + 载荷 + `browse_result` 瞬态列 + `file.browse` 审计动作
- [x] 控制面：`POST /browse` 直接建命令并写审计；回传原子转存结果，读取事务内清空正文并写专项审计（原「审批 worker 建命令 + pending grant + 一次性消费」路径已按 ADR-0093 退役）
- [x] 控制面：repository `UpdateStatusWithBrowseResult` + `ClearBrowseResult` + `ExpireStale` 清 `browse_result`
- [x] 控制面：`BrowseHandler` 建命令、按 `commandId` 读取、兼容 grant 路径与路由（旧 GET 不再创建命令，缺 `commandId` → `400`）
- [x] 控制面：测试先行（service、handler、server integration）
- [x] agent：命令数据类扩展 `op/offset/limit/maxDepth` + `runBrowse` 分支 + `uploadBrowseResult` + 单测
- [x] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG（按需）

## 5. 验收标准

- 端点经命令生命周期代理列目录 / 读子树 / 读单文件：`POST` 直接建命令，读取方按 `commandId` 取结果。
- 读取按 `content.read` + `management.read` 直执，缺能力 `403`；旧 GET 不创建命令、不隐式下发，缺 `commandId` → `400`。
- 入队与读取审计 detail 均不含文件内容；目录/文件结果仅暂存，读取时清空瞬态正文并在返回前写完审计。
- 回传身份或命令归属不一致必须失败关闭；Agent 原始失败原因不得进入结果摘要、审批、审计或日志。
- agent 命令处理识别 `fs-browse`、调 browse 原语回传；未知命令 / 未注入浏览能力忽略 / 回 failed（agent 单测）。
- 控制面 `go test ./...` 绿、agent 单测绿 + 双端 jar build 绿、`go vet` 不新增问题。
- 真机维度（控制面端点→命令→agent 读盘→回传 端到端浏览）：本会话无真机能力 → 标「待真机验」。

## 6. 风险 / 待定

- **命令终态与读取分离**：`POST` 成功只表示命令已可靠入队；Agent 离线、超时或拒读由命令状态反映，
  不让 HTTP 建命令请求阻塞等待回传。
- **演进备注（已被 ADR-0093 取代）**：本规格原设计为「审批批准后 pending grant 激活、原申请主体一次消费」，
  现改为 `content.read` 直读；保留此记录仅为解释 §3 中仍有 grant 兼容入口的原因。
- **并发同一实例多次浏览**：每次浏览各建独立命令（不互斥，区别于反向抓取受管任务单实例互斥），
  agent 单飞排空逐条处理；commandHub waiter 按 serverId 唤醒，service 按 commandId 校验自己的结果到位才返回。
