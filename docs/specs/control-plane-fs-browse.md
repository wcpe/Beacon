# 功能规格：控制面文件浏览端点（FR-110）

> 状态：开发中　·　关联 PRD：FR-110（依赖 FR-109）　·　分支：master（feat 直接进 master）

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

浏览是**请求 / 响应**语义（admin 要拿到结果代理给前端），不同于反向抓取的 fire-and-forget。实现采用
**控制面侧阻塞等待**：admin 端点 → service 建 pending `fs-browse` 命令 + 审计（事务内）→ 提交后
`NotifyCommand` 唤醒 agent SSE → service 注册 `commandHub` waiter 并阻塞 → agent 拉命令、async 读盘、
回传结果（控制面侧把结果 JSON 转存到命令瞬态列 `BrowseResult` 并 CAS `fetched → done`，同时再 `NotifyCommand`
唤醒等待中的 admin）→ service 被唤醒后读出瞬态结果、返回给 handler 代理给前端；超时 / agent 离线 → 504。

agent 回传内容（目录清单 / 子树 / 文件内容）是受控瞬态：转存到 `agent_command.browse_result`（新增 TEXT 列，
与 `imprint_content` / `log_content` 同范式——瞬态、done 后即可清、过期清理一并抹除、不入审计 detail、不导出 git）。

### 3.2 分层（router → handler → service → repository）

- **handler**（`BrowseHandler`）：`GET /admin/v1/instances/{serverId}/browse?namespace=&op=&path=&offset=&limit=&maxDepth=`
  解析查询参数、校验目标在线（`InstanceService.Get`）、调 `BrowseService.Browse(...)`、渲染结果。
  handler 不碰 GORM / 内存结构。
- **service**（`AgentCommandService.RequestBrowse` + `ReceiveBrowseResult`）：建命令 + 审计（事务）+ 唤醒 +
  等待 + 读瞬态结果；agent 回传入口 `ReceiveBrowseResult` 转存结果 + CAS done + 唤醒等待者。
- **repository**：新增 `UpdateStatusWithBrowseResult`（fetched→done 转存结果）；`ExpireStale` 一并清
  `browse_result`。

### 3.3 鉴权与审计

- 该端点是 `GET`，但**触发浏览是写副作用**（建命令 / 唤醒 agent / 入审计），不能让 readonly 触发。
  故在 `router` 用一条**显式 full-only 守卫**（`requireFullRole`）包住该端点：readonly → 403。
  （`readonlyWriteGuard` 只拦写方法，GET 默认放过，故需显式守卫。）
- 触发 + 结果各记审计：`ActionFileBrowse`（`file.browse`），target=command/serverId，
  detail 仅 `{commandId, op, path}`（**无文件内容**）。

### 3.4 agent 侧

`ReverseFetchExecutor.runOnce` 增 `fs-browse` 分支 → `runBrowse(command)`：按 `op` 调
`adapter.browseListDir / browseReadTree / browseReadFile`，结果经新回传端点
`POST /beacon/v1/agent/files/browse-result`（`uploadBrowseResult`）回传；原语返回 null（越权 / 非目录 / 非文本）
→ 回传 `{ok:false, reason}`，控制面 CAS failed。未注入浏览能力（壳层 browse* 返回 null）→ 回 failed，
控制面据此 404 / 502，fail-static 不影响主流程。`browse-result` 命令载荷的 `op` 经 `IngestCommandPayload`
扩展字段解析（agent 复用同一命令数据类，加 `op/offset/limit/maxDepth` 可选字段）。

## 4. 任务拆分

- [ ] 控制面：新增 `fs-browse` 命令类型 + 载荷 + `browse_result` 瞬态列 + `file.browse` 审计动作
- [ ] 控制面：`RequestBrowse`（建命令 + 审计 + 唤醒 + 等待 + 读结果）+ `ReceiveBrowseResult`（转存 + done + 唤醒）
- [ ] 控制面：repository `UpdateStatusWithBrowseResult` + `ExpireStale` 清 `browse_result`
- [ ] 控制面：`BrowseHandler` + admin 只读端点 + agent 回传端点 + `requireFullRole` 守卫 + 路由
- [ ] 控制面：测试先行（service 单测 + handler 鉴权测试）
- [ ] agent：命令数据类扩展 `op/offset/limit/maxDepth` + `runBrowse` 分支 + `uploadBrowseResult` + 单测
- [ ] 文档同步：PRD 状态、ARCHITECTURE、API、CHANGELOG（按需）

## 5. 验收标准

- 端点经命令生命周期代理列目录 / 读子树 / 读单文件（service 单测：建命令 + 转存结果 + done）。
- admin `full` 可触发、`readonly` → 403（handler 鉴权测试）。
- 触发与结果入审计，detail 不含文件内容（service 单测断言审计 + detail 形状）。
- agent 命令处理识别 `fs-browse`、调 browse 原语回传；未知命令 / 未注入浏览能力忽略 / 回 failed（agent 单测）。
- 控制面 `go test ./...` 绿、agent 单测绿 + 双端 jar build 绿、`go vet` 不新增问题。
- 真机维度（控制面端点→命令→agent 读盘→回传 端到端浏览）：本会话无真机能力 → 标「待真机验」。

## 6. 风险 / 待定

- **请求 / 响应经命令通道的等待窗口**：admin GET 阻塞等待 agent 回传，超时即 504。超时取值复用既有
  长轮询 / 命令超时口径，避免 admin 端长挂。agent 离线（无 SSE waiter）→ 命令留 pending、admin 等到超时 504。
- **并发同一实例多次浏览**：每次浏览各建独立命令（不互斥，区别于反向抓取受管任务单实例互斥），
  agent 单飞排空逐条处理；commandHub waiter 按 serverId 唤醒，service 按 commandId 校验自己的结果到位才返回。
