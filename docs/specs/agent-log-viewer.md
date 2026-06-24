# 功能规格：在线日志/诊断查看器（agent 只读日志回传）

> 状态：开发中　·　关联 PRD：FR-88　·　分支：feature/fr-88-agent-logs　·　决策：[ADR-0040](../adr/0040-agent-readonly-log-tail.md)

## 1. 背景与目标

排障时 agent 日志只落各自机器磁盘，看日志要 SSH 上机。FR-88 让运维在管理台「服务器详情」直接拉某在线 agent 的最近 N 行日志（脱敏后），排障免上机。属 P2。

## 2. 需求（要什么）

- 管理台「服务器详情」加「查看 agent 日志」面板：点触发 → 拉该 agent 最近 N 行日志展示。
- agent 只回传**自身**日志（它经 PlatformAdapter 打的行），**不读任意磁盘文件**、不读 server 全量 log。
- 日志在 agent 侧**落内存环形缓冲那一刻即脱敏**（token/密码/密钥等敏感串掩码）。
- 行数有界（环形缓冲容量）、控制面侧每实例限速（同时至多一条活跃取日志命令）、经 agentToken 信任面 + admin full 角色鉴权 + 审计。
- 日志文本是瞬态：取完即弃，不落持久真源、不导出、不进审计 detail。
- 范围内：agent 自身日志最近 N 行的「命令-回传」拉取与展示。
- 不做（范围外）：读 server `logs/latest.log` / 任意文件；日志长期落库 / 检索历史 / 日志聚合平台；实时流式 tail；agent 开 HTTP 端口。

## 3. 设计（怎么做）

决策见 ADR-0040，不在此重复正文。落地要点：

### 3.1 agent-core（Kotlin，双端通用）
- 新增 `AgentLogBuffer`：线程安全（如 `synchronized` 守护的定长 `ArrayDeque`）、固定容量环形缓冲，`append(line)` 满则挤出最旧，`snapshot()` 返回当前全部行（最旧→最新）。容量有界常量。
- 新增日志脱敏纯函数 `LogRedactor.redact(line): String`：对常见敏感键（token/password/secret/authorization/bootstrap-token 等）的值、形似密钥的长串掩码替换。集中、可穷举单测。
- 新增 `BufferingPlatformAdapter`：装饰既有 `PlatformAdapter`，`info/warn/error` 先委托原 adapter 打日志、再把「级别 + 脱敏后文本」`append` 进 `AgentLogBuffer`。core 装配时用它包裹壳传入的 adapter，使所有 core 日志自动入缓冲（壳层零改动）。
- 命令分路：`tail-logs` 命令拉到后，读 `AgentLogBuffer.snapshot()` → 经 `BeaconApiClient.uploadLogs(commandId, lines)` 回传。读快照 + HTTP 在 async 线程。复用 ReverseFetchExecutor 的 trigger/单飞，或新增一个轻量 `LogTailExecutor`（择简）。
- `BeaconApiClient.uploadLogs`：`POST /beacon/v1/agent/logs`，body `{commandId, lines:[{level,text}...]}`。
- 命令类型常量 `tail-logs`；`AgentCommand` 解析时按 type 分路（payload 对 tail-logs 为空）。

### 3.2 控制面（Go）
- `model.enums`：加 `CommandTypeTailLogs = "tail-logs"`；`IsValidCommandType` 纳入。
- `model.AgentCommand`：加瞬态列 `LogContent string gorm:"column:log_content;type:text"`（与 ImprintContent 平行，过期清空、不入审计、不导出）。
- 新增 `AgentLogService`：
  - `RequestTailLogs(ns, serverId, operator, clientIP)`：每实例单活跃限速（已有非终态 tail-logs 命令则拒 409）→ 事务建 pending tail-logs 命令 + 审计（instance.tail-logs，detail 仅 commandId/serverId，无内容）→ 唤醒 agent。
  - `ReceiveLogs(commandID, lines, clientIP)`：命令须 fetched 且 type=tail-logs → 把 lines 序列化存 LogContent（瞬态）→ CAS done。
  - `GetLogs(ns, serverId)`：取该实例最近一条 tail-logs 命令视图（状态 + 若 done 则解出 lines）。
- agent 回传 handler：`POST /beacon/v1/agent/logs`（agentToken 信任面）。
- admin handler：`POST /admin/v1/instances/{serverId}/logs?namespace=`（触发，202 返回命令视图，写、readonly 403）；`GET /admin/v1/instances/{serverId}/logs?namespace=`（取最近一条命令的状态 + 日志行，读）。
- FetchPending 复用既有 AgentCommandService（已按 type 透传 payload）；tail-logs 命令也经 `/commands` 拉取——确认 FetchPending 不限定 type（现状不限）。
- 过期清理：ExpireStale 已覆盖 pending/fetched 超时；扩展为转 expired 时清空 LogContent 瞬态（与命令过期同批）。

### 3.3 前端（React/TS）
- `api/types.ts`：`AgentLogLine { level; text }`、`AgentLogView { commandId; status; lines? }`。
- `api/client.ts`：`requestAgentLogs(serverId, namespace)`、`getAgentLogs(serverId, namespace)`。
- `ServerDetailSheet`：加「查看 agent 日志」区，点按钮触发 → 轮询 status 至 done/failed/expired → 展示 lines（等宽、按级别着色），脱敏说明 hint。i18n key 入 zh-CN/en。

## 4. 任务拆分
- [ ] agent：AgentLogBuffer + LogRedactor + BufferingPlatformAdapter + tail-logs 分路 + uploadLogs（测试先行）
- [ ] 控制面：enums + model 瞬态列 + AgentLogService + 回传/触发/查询 端点 + 路由 + 过期清空（测试先行，真 MySQL 集成）
- [ ] 前端：types + client + ServerDetailSheet 面板 + i18n（vitest + build）
- [ ] 文档同步：PRD 状态、API.md、ARCHITECTURE.md（命令通路新类型）、CHANGELOG

## 5. 验收标准
- agent 单测：环形缓冲有界（满挤出最旧）、线程安全、落缓冲脱敏（敏感串掩码）；gradle build 绿。
- 控制面：go test 绿 + 真 MySQL 集成跑通「触发→拉命令→回传→查询得脱敏日志」全链路、单活跃限速 409、过期清空瞬态。
- 前端：vitest + build 绿。
- 真机（主控）：lobby-1 agent 经管理台拉到自身日志、脱敏生效。

## 6. 风险 / 待定
- 缓冲容量 N 取值：默认 300 行（够排障、内存可忽略）；不做成配置项（YAGNI，需要再加）。
- 脱敏规则覆盖面：先覆盖常见 token/password/secret/authorization + 长 hex/base64 串；保守掩码，宁可多掩。
