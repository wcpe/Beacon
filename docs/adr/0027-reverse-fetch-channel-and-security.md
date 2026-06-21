# ADR-0027：在线实例反向抓取——命令通道与安全边界

**状态**：已接受

## 背景

FR-39「配置导入（在线实例反向抓取）」要从一台**在线实例**把其真实落盘的 `plugins/` 文本配置反向抓取入库为组 / 实例级文件树覆盖。这需要一条 server→agent **命令通道**（控制面命令某台 agent 读盘并回传）——此前不存在。

传输层已由 [ADR-0015](0015-sse-server-push-transport.md) 决定（单条 SSE 流，已预留 `command-pending` 事件 + 命令回执走 HTTP），本 ADR **不再决传输**，只锁「反向取文件」的**语义与安全面**。

本特性把「现网某台真实 `plugins/` 配置」搬入控制面库，安全面与 [ADR-0011](0011-third-party-file-override-and-restricted-reload-command.md)（远程命令执行面）同级——对抗式评审须把读取范围、内容、体量、鉴权锁死，放开任一即越界。

## 决策

1. **命令通道复用 SSE（ADR-0015），不新增传输**：`command-pending` 事件唤醒目标 agent → agent HTTP 拉命令详情 → 执行 → HTTP 回传结果。命令真源**落库**（`agent_command` 表），可跨 SSE 断连重拉、可审计、有生命周期（`pending → fetched → done / failed / expired`）。

2. **本期命令通道只接「ingest-plugins」一种消费者**：不做通用远程命令框架、不预埋多命令空壳（守 `scope-discipline`）。后续命令到时按需加 `type`。

3. **读取范围限死真实 `plugins/` 根内**：agent 以真实 dataFolder 之上的 `plugins/` 根为基准，`Path.normalize().startsWith`（Path 级）校验；解析符号链接后仍须落在根内（**禁符号链接逃逸**）；禁 `..` / 绝对 / UNC / 盘符 `:` / 保留设备名（沿 ADR-0011 路径口径，含 Windows 专项）。

4. **只 ingest 文本配置，排除 `.jar` 与二进制**：沿 ADR-0011 **禁 jar**（jar 属 P3 版本发布编排、非托管配置）；二进制 / 非文本不 ingest（通道B 管文本配置）。

5. **硬上限、超限整体失败不部分入库**：单文件 / 单次总字节 / 文件数上限**复用 FR-38 import 常量**；任一超限 → agent 不上传、控制面拒入，命令 `failed`，**绝不部分落库**（避免半截覆盖污染基线）。

6. **agent 为最终权威 + 控制面入库前再校验**：与 ADR-0011 同构——agent 本地按规则过滤读取，控制面入库前**同口径再校验**（双保险，控制面被绕也不入越界文件）。

7. **命令鉴权 + 审计**：触发端点是 admin 写操作（`full` 角色，`readonly` → 403，扩展 [ADR-0026](0026-runtime-api-keys-and-readonly-role.md) / [ADR-0009](0009-control-plane-auth-pulled-forward.md)）；触发与 ingest 各入 `audit_log`；**敏感文件内容不入审计 detail**。

8. **agent 不主动、不常驻读盘**：仅在收到鉴权命令时于 **async 线程**读一次盘上传，不轮询、不常驻、不碰 MC 主线程（守架构不变量 #5）；纯**只读上传**，不写目标机盘。

## 架构边界论证（为何不违「控制面只存事实、禁游戏逻辑」）

控制面存的是「某在线实例当前 `plugins` 文本配置态」这一**事实快照**（运维把现网快照入库为托管基线），不编排、不决策游戏逻辑——性质等同 FR-38 正向导入（把一份配置入库为组覆盖），只是来源从「管理台上传」变为「命令在线实例回传」。命令由 **agent 在本地**执行「读自己的盘并上传」，是数据面对自身的**只读**运维操作：无 shell、无 jar、不写盘。**放开任一约束即越界**：读 jar / 写盘 → 滑入 P3 发布编排；无鉴权触发 / 不限范围 → 把现网密钥无差别外泄入库。

## 后果

- 新增 `agent_command` 表 + server→agent 命令通道（首个且本期唯一消费者 `ingest-plugins`）。
- agent 新增「读真实 `plugins` 文件」平台能力（壳实现；core 不碰平台 IO，守 [ADR-0005](0005-agent-transport-codec-abstraction.md)）——只读、async、带上限与排除。
- ingest 复用 `FileService.Import`（[ADR-0010](0010-file-tree-hosting-blob-channel.md) 通道B）落组 / 实例覆盖。
- 把现网真实配置搬入控制面库：运维须知情（审计 + OPERATIONS 写清「反向抓取 = 把目标机 `plugins` 文本配置集中入库」）。
- 随实现同步 `ARCHITECTURE.md` / `API.md`（新端点 + `command-pending` 事件）/ `OPERATIONS.md`。

## 备选方案

- **新建独立命令长连接 / 通道**：撞 ADR-0015（已统一 SSE）、连接膨胀。否决。
- **读整盘含 jar / 二进制 / 世界数据**：越界（jar → P3）、安全面爆炸、体量不可控。否决。
- **命令不落库、纯内存**：断连即丢、无审计、无法重拉。否决（命令需持久 + 审计）。
- **控制面全信 agent、不再校验**：agent 被改 / 绕则越界文件入库。否决（双保险）。
- **做通用远程命令框架**：超本期范围（`scope-discipline`）、预埋空壳。否决，到需要再扩。
