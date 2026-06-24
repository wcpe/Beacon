# ADR-0040：agent 只读日志回传——自身日志内存环形缓冲 + 命令-回传 + 落缓冲脱敏

**状态**：已接受

## 背景

排障时 agent / 控制面的日志只落在各自机器磁盘，运维要看 agent 近期日志必须 SSH 上机翻文件——慢、且把机器访问权扩散给只该用管理台的人。FR-88 要在管理台「服务器详情」直接拉某在线 agent 的最近 N 行日志，排障免上机。

难点在 agent 的形态：agent 是嵌在 MC 服务端进程里的 TabooLib 插件，**没有自己的 HTTP server**——控制面无法像调一个微服务那样直接 GET 它的日志端点。已有的 server→agent 通路只有一条：[ADR-0015](0015-sse-server-push-transport.md) 的单条 SSE 推送 + agent 主动拉命令 / 回传，[ADR-0027](0027-reverse-fetch-channel-and-security.md)、[ADR-0037](0037-reverse-fetch-managed-task.md) 的「反向抓取」正是沿这条通路做的「控制面下发命令 → agent 回传数据」。

把「读日志」做成又一种「读任意磁盘文件」的能力很危险：agent 进程能读它所在机器上的大量文件，一旦控制面被攻破、或端点鉴权被绕过，就成了任意文件读取的跳板。FR-88 的价值（看 agent 近期日志）并不需要读任意文件——只需要 agent **自己刚打过的那些日志行**。因此本 ADR 把能力**收敛到 agent 自身日志**，并把它做成内存环形缓冲、落缓冲即脱敏，从根上限制即便被攻破也只能拿到「agent 自己脱敏后的近期日志」。

## 决策

1. **agent 维护自身日志的有界内存环形缓冲，不读任意磁盘文件。** agent-core 新增线程安全的 `AgentLogBuffer`（固定容量环形缓冲，默认最近 N 行，N 有界、满则挤出最旧）。agent 经 `PlatformAdapter.info/warn/error` 打的每一行日志，**同时**写一份进该缓冲（用装饰器包裹既有 adapter，不改各壳日志实现）。缓冲只持有 agent 自己打的日志行，**绝不打开、读取任何磁盘日志文件**（不读 server `logs/latest.log`、不读 `plugins/` 下任何文件）。即便控制面被攻破，能拿到的上限就是「agent 进程自己近期打的、已脱敏的日志行」。

2. **落缓冲时脱敏（write-time redaction）。** 写进环形缓冲前对每行做敏感串替换（token / 密码 / 密钥 / Authorization / bootstrap-token 等常见键的值，以及形似密钥的长串），命中即替换为掩码（如 `***`）。脱敏在**落缓冲那一刻**完成、是纯函数，缓冲里存的就已是脱敏后的文本——回传链路任何环节都拿不到原文，杜绝「先存原文、回传时再脱敏」可能漏脱的窗口。脱敏规则集中在 core 一个纯函数里，可穷举单测。

3. **取日志走「命令-回传」周期，不给 agent 开端口、不让控制面直连 agent。** 复用既有命令通路（沿 ADR-0027/0037）：admin 触发 → 控制面建一条 `tail-logs` 命令（pending）+ 经 SSE 唤醒该 agent → agent 拉到命令 → 读自身环形缓冲快照 → 回传到控制面新端点 `POST /beacon/v1/agent/logs`。控制面把回传的日志行存为**瞬态**（命令上的短期字段，过期即清空），admin 经 `GET /admin/v1/instances/{serverId}/logs` 触发并轮询取结果。agent 不新增任何监听端口、不被控制面反向直连——通路方向与既有命令一致（agent 主动出站拉 / 传）。

4. **回传行数有界 + 限速 + 经 agentToken 信任面。** 回传的日志行数由环形缓冲容量天然有界（agent 侧不会回传超过缓冲容量的行）；控制面回传端点 `POST /beacon/v1/agent/logs` 与既有 agent 端点同属 agentToken 防误连信任面（[ADR-0027](0027-reverse-fetch-channel-and-security.md) 决策2）；admin 触发端点限速（每实例同时至多一条活跃 tail-logs 命令，命中即拒，避免刷命令压垮 agent）。admin 触发与既有命令一致经管理台鉴权（full 角色）+ 审计。

5. **瞬态不入库真源、不导出、不进审计 detail。** 回传的日志文本是受控瞬态审核暂存（与 [ADR-0027](0027-reverse-fetch-channel-and-security.md) 决策7「detail 不含文件内容」、ADR-0037 拓印瞬态同源思路一致）：存命令上的瞬态 TEXT 字段，命令 done / 失败 / 过期即清空；不落任何持久真源、不导出 git、不进审计 detail（审计只记「谁在何时拉了哪台的日志」，不记日志内容）。

6. **agent 架构约束一条不破。** 写缓冲是纯内存追加（O(1)、无 IO），可在任意线程（含日志调用所在线程）安全执行——但读缓冲快照 + HTTP 回传仍只在 async 线程（沿 ADR-0027 决策8，绝不上 MC 主线程）；HTTP / JSON 仍只在适配器层（[ADR-0005](0005-agent-transport-codec-abstraction.md)）；core 不依赖 CoreLib；agent 自管身份。**改 agent-core → 双端 jar（bukkit/bungee）重建并真机重部。**

## 理由

- **环形缓冲而非读 server log 文件**：读 `logs/latest.log` 要么把整文件读进内存（可任意大、含大量与 agent 无关的 server / 其它插件输出，且可能含未脱敏的敏感串），要么做文件 tail（复杂、跨平台、仍读任意文件）。agent 只关心自己打的日志，内存环形缓冲是最小、最安全的载体：有界、O(1)、内容可控、落缓冲即脱敏，不触碰文件系统。
- **命令-回传而非给 agent 开端口**：agent 是插件、无 HTTP server，给它开端口意味着引入新的监听面 + 新鉴权 + 新攻击面，且与现有「agent 只出站」的网络模型相悖。复用成熟的命令通路（SSE 唤醒 + 拉命令 + 回传），零新增网络监听、方向与既有一致。
- **落缓冲脱敏而非回传时脱敏**：脱敏点越靠近源头，漏脱的窗口越小。落缓冲即脱敏后，缓冲、回传、控制面瞬态、前端展示全链路都只见脱敏文本，任何一环被看到都不泄密。
- **瞬态 + 限速 + 鉴权审计**：日志可能含运维不愿长期留存的运行细节；做成瞬态（取完即弃）+ 每实例单活跃限速 + full 角色鉴权 + 审计，把「谁能看、看多久、留多久」收紧到最小。

## 后果

- agent-core 新增 `AgentLogBuffer`（环形缓冲）+ 日志脱敏纯函数 + 包裹 adapter 的日志装饰器 + `tail-logs` 命令分路（拉到该类型 → 读缓冲快照 → 回传）；改 agent-core → **双端 jar 重建 + 真机重部**。
- 控制面新增 `tail-logs` 命令类型 + 一个 `AgentLogService`（编排命令下发 / 接收回传瞬态 / 单活跃限速 / 过期清空）+ agent 回传端点 `POST /beacon/v1/agent/logs` + admin 触发并查询端点 `GET/POST /admin/v1/instances/{serverId}/logs`；`agent_command` 加一个瞬态 `log_content` TEXT 列（与 `imprint_content` 平行，过期清空）。
- 前端「服务器详情」Sheet 加「查看 agent 日志」面板：触发 → 轮询 → 展示最近 N 行（脱敏后），与拓印「触发 → 轮询 status → 取结果」交互范式一致。
- 旧 agent 与新控制面：旧 agent 收不到 / 不识别 `tail-logs` 命令（按未知类型忽略，沿 ReverseFetchExecutor 既有处理），控制面命令超时清理为 expired——能力仅在新 agent 上线（真机须双端升级）。

## 备选方案

- **agent 直接读 server `logs/latest.log` 回传**：能拿到更全的日志，但读任意磁盘文件是安全倒退（任意文件读取跳板），且日志含大量与 agent 无关、可能未脱敏的输出，体积不可控。被否（违背「只自身日志、不读任意文件」边界）。
- **给 agent 开一个只读日志 HTTP 端口、控制面直连拉**：控制面直接 GET agent 更直观，但 agent 是插件无 server，开端口=新监听面 + 新鉴权 + 新攻击面，与「agent 只出站」模型相悖。被否（复用命令通路，零新增监听）。
- **回传时再脱敏（缓冲存原文）**：缓冲存原文则任何一处读到缓冲都见明文，漏脱窗口大。被否（落缓冲即脱敏，源头收口）。
- **日志瞬态落库做持久真源 / 可检索历史**：把 agent 日志长期落库做成可检索历史，越界（控制面只存「事实」、不做日志聚合平台，属 ELK/Loki 之类的事，超 MVP 范围），且持久留存敏感运行细节风险更高。被否（瞬态、取完即弃，不做日志平台）。
- **不改 agent、控制面侧想办法拿 agent 日志**：控制面无法在不改 agent 的前提下让 agent 吐出自身内存日志——技术上不可行。被否。
