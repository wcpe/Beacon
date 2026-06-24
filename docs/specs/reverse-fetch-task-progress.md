# 功能规格：反向抓取受管任务进度 + 错误回传（FR-87）

> 状态：开发中　·　关联 PRD：FR-87（增强 FR-58/FR-60）　·　ADR：[ADR-0037](../adr/0037-reverse-fetch-managed-task.md)（spec 级扩展、无新 ADR）　·　分支：feature/fr-87-task-error

## 1. 背景与目标

FR-58/FR-60 把反向抓取升级为受管任务 + 两段式（scan/submit），但真机暴露两处可观测盲区：

- **任务卡在 scanning / fetching 不动、UI 无任何进度反馈**：agent 读盘失败时只在 agent 本地日志 `error` 后**静默放弃**（见 `ReverseFetchExecutor.runScan/runSubmit` 的 `catch → return`），不回传任何错误；控制面任务停在非终态，要等后台过期清理器超时才转 `expired`。运维盯着任务台只见「scanning」长时间不变，无从判断 agent 是否在干活、卡了多久，只能去翻磁盘日志才能定位。
- **agent 端错误对控制面 / UI 完全不可见**：任务失败原因（IO 错、目录读不了）只活在 agent 进程日志里，控制面与管理台都看不到。

FR-87 补这两点，**不改受管任务状态机本身、不加新状态**：① 任务视图算「已用时长」`elapsedSec` + 前端任务台显时长与**卡死警示**（非终态停留超阈值前端标「疑似 agent 未响应」）；② agent 执行 scan/submit **失败时主动回传错误**到控制面，控制面把任务转既有终态 `failed` 并记 `lastError`，前端 `failed` 任务展示该错误。

## 2. 需求（要什么）

- **进度·已用时长**：任务视图新增 `elapsedSec`（控制面渲染时刻 UTC 距 `updatedAt` 的秒数，负值归零）。非终态按 `updatedAt`（最近一次状态迁移）起算「当前态已停留多久」；终态同样给出（距最后一次迁移）。
- **进度·卡死警示**（纯前端）：任务处非终态（`scanning`/`fetching`/`pending-review`/`conflict-review`/`ingesting`）且 `elapsedSec` 超阈值（前端常量 `STUCK_THRESHOLD_SEC`）→ 任务台标「疑似 agent 未响应」。纯展示派生，不改后端状态。
- **错误回传**（agent + 控制面）：agent 执行 scan/submit 读盘失败（IO 错 / 异常）时，回传错误到控制面**新端点** `POST /beacon/v1/agent/files/error`（commandId + reason 文本）；控制面据 commandId 反查所属任务，把任务从其当前非终态 CAS 转 `failed`、命令转 `failed`、记 `lastError`（任务新字段）；前端 `failed` 任务显 `lastError`。
- **`lastError` 字段**：`reverse_fetch_task` 加 `last_error`（落 DB，可移植 `VARCHAR`），与既有 `note`（结果摘要 / 取消原因）分立——`last_error` 专记**失败原因明细**（agent 回传或控制面入库失败），任务视图同时暴露 `note` 与 `lastError`。

### 不做（范围外）

- 不加新状态机状态、不改 scan/submit 两段式契约本身。
- 不改后台过期清理器阈值（卡死警示是前端展示，过期仍走既有清理器）。
- 不做实时进度百分比 / 字节级进度条（受管任务进度仍由状态 + 计数 + 已用时长表达）。
- agent 不重试回传错误（best-effort 一次；回传不通则仍交控制面超时清理为 `expired`，与既有放弃语义一致）。

## 3. 设计（怎么做）

### 3.1 数据模型

`reverse_fetch_task` 加列 `last_error` `VARCHAR(512)`（GORM 可移植，无 ENUM/JSON）。AutoMigrate 自动补列；旧行为空串。失败时写入摘要文本（无敏感文件内容，沿 ADR-0027 决策7）。

### 3.2 已用时长 `elapsedSec`

控制面 handler 渲染视图时按 `time.Now().UTC()` 距 `task.UpdatedAt` 计算整秒，负值归零（时钟漂移防御）。纯派生、不落库。任务视图（`reverseFetchTaskView`）加 `elapsedSec int`、`lastError string`。

### 3.3 错误回传线路契约（agent → 控制面）

- **端点**：`POST /beacon/v1/agent/files/error`（agentToken 中间件下，与 scan/ingest 同信任面）。
- **请求体**：`{"commandId":123,"reason":"读 plugins 目录失败：..."}`。
- **控制面处理**（`ReverseFetchTaskService.ReceiveError`）：据 commandId 反查所属任务（scan 命令查 `scan_command_id`、submit 命令查 `submit_command_id`，命令须 `fetched`）；任务须处对应非终态（scan→`scanning`、submit→`fetching`）→ CAS 转 `failed`、命令转 `failed`、写 `last_error`、清瞬态、解除互斥占位、记审计 `file.reverse-fetch-error`。命令 / 任务态不符 → `404`/`409`（与既有回传端点同口径，幂等：重复回传不二次终结）。
- **agent 改动**（`ReverseFetchExecutor`）：`runScan` / `runSubmit` 的读盘 `catch` 分支由「仅 `error` 日志 + `return`」改为「`error` 日志 + `apiClient.uploadError(commandId, reason)` 后 `return`」。`BeaconApiClient` 加 `uploadError(commandId, reason): Boolean`（POST /error，连接失败回 false、不重试）。全程仍 async、不碰 MC 主线程、HTTP/JSON 只在适配器层（ADR-0005），observe-only 写回守卫不变。

### 3.4 前端（任务台）

- `ReverseFetchTaskView` 加 `elapsedSec`、`lastError`。
- 列表行进度列旁显「已用 Ns / Nm」（人类可读时长）；非终态且超 `STUCK_THRESHOLD_SEC` → 行内「疑似 agent 未响应」警示徽标。
- `failed` 任务详情 / 列表显 `lastError`（非空时）。
- 时长格式化与卡死判定抽为纯函数（`lib/reverseFetchProgress.ts`），穷举单测。

## 4. 任务拆分
- [ ] 后端：模型加 `LastError`；视图加 `elapsedSec`/`lastError` + 渲染算 elapsed；repo `MarkTerminalWithError`（或扩 MarkTerminal 写 last_error）。
- [ ] 后端：`ReverseFetchTaskService.ReceiveError` + handler `Error` 端点 + router 挂载。
- [ ] 后端测试（先行红）：error 回传转 failed 记 lastError / 命令任务态不符拒 / elapsedSec 计算。
- [ ] agent：`BeaconApiClient.uploadError` + executor scan/submit 读盘失败回传错误；agent 单测（读盘失败走 /error 端点）。
- [ ] 前端：进度纯函数 + 任务台时长 / 卡死警示 / lastError 展示 + 单测。
- [ ] doc-sync：PRD FR-87、API.md（新端点 + 视图字段）、ADR-0037 spec 扩展节、CHANGELOG、本规格。

## 5. 验收标准
- 任务视图含 `elapsedSec`（≥0）与 `lastError`。
- agent 读盘失败 → 回传 `/files/error` → 任务转 `failed`、`lastError` 落库且可查、命令转 `failed`。
- error 端点对命令 / 任务态不符按 404/409 拒（不误终结无关任务）。
- 前端任务台显已用时长；非终态超阈值显「疑似 agent 未响应」；`failed` 显 `lastError`。
- 受影响组件测试全绿（`go build/test/vet ./...`；真 MySQL 集成验 lastError 落库 + failed 流转；`cd agent && ./gradlew :agent-core:build`；`cd web && pnpm test && pnpm build`）。
- **真机**：制造扫描失败（如 plugins 目录不可读）→ 任务台见 `lastError` + 非终态卡死警示。

## 6. 风险 / 待定
- 改 agent-core → 双端 jar 重建 + 真机重部（由主控做）。
- error 回传 best-effort 不重试：回传不通仍靠过期清理器兜底（任务不会永久卡非终态）。
- `last_error` 与 `note` 语义分立：note 记结果 / 取消摘要，last_error 专记失败明细；两者可同时非空（如 ingest 失败：note 记前态、last_error 记错因）。
