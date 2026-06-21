# 功能规格：配置导入（在线实例反向抓取）

> 状态：开发中　·　关联 PRD：FR-39　·　分支：feature/config-import-reverse-fetch

## 1. 背景与目标

FR-38（已交付@v0.6.0）做了「正向」配置导入：管理台把一份本地目录批量上传到某组文件树（通道B，[ADR-0010](../adr/0010-file-tree-hosting-blob-channel.md)）。FR-39 做「反向」：从一台**在线实例**把它**当前真实落盘的 `plugins/` 目录**反向抓取上来，ingest 入库为**组 / 实例级**文件树覆盖——让运维以「现网某台为模板」一键建立托管，免手工逐个上传。属 P2 治理增强。

这需要一条此前不存在的 **server→agent 命令通道**（控制面命令某台 agent 干活并回传结果）。传输**复用** FR-24 / [ADR-0015](../adr/0015-sse-server-push-transport.md) 的单条 SSE 流——该 ADR 已预留 `command-pending` 事件与「命令回执走 HTTP」，故本特性的新决策只涉「反向取文件」的**语义与安全面**（新 [ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md)），不新增传输机制。

## 2. 需求（要什么）

- 范围内：
  - **命令通道**：控制面给指定在线 serverId 下发一条「抓取 plugins」命令；经 SSE `command-pending` 事件唤醒该 agent；agent 经 HTTP 拉命令详情、执行、回传结果；命令有生命周期（`pending → fetched → done / failed / expired`），真源落库（可跨 SSE 断连重拉、可审计）。
  - **反向抓取**：agent 读取其**真实 `plugins/` 根目录**整棵子树的**文本配置文件**，回传控制面。
  - **ingest**：控制面把回传文件集 ingest 为**组级或实例级**文件树覆盖（复用 FR-38 的 `FileService.Import` 落盘语义、通道B）。
  - **安全面（新 ADR-0027）**：读取限死在真实 `plugins/` 内、排除 `.jar` 与二进制、单文件 / 单次总字节 / 文件数上限、agent 为最终权威、控制面入库前同口径再校验、命令 admin 鉴权触发、触发与 ingest 入审计。
  - **管理台**：配置导入处新增「从在线实例反向抓取」入口——选在线实例 + 目标组 / 实例，触发并展示结果。
- 不做（范围外，需要再单独提）：
  - 抓取 `.jar` / 世界数据 / 非 `plugins` 路径；任何 shell / 进程执行（守 [ADR-0011](../adr/0011-third-party-file-override-and-restricted-reload-command.md)）。
  - 通用远程命令框架：本期命令通道只接「抓取 plugins」这一种消费者（机制可扩展，但不预埋多命令空壳，守 `scope-discipline`）。
  - 自动 / 定时反向同步、双向 diff 合并（仅一次性抓取覆盖）。

## 3. 设计（怎么做）

涉及模块：控制面 `model`（命令表 + 枚举）/ `repository`（命令 CRUD）/ `service`（命令编排 + ingest 复用 `FileService.Import` + 审计）/ `handler`（admin 触发 + agent 拉命令 / 回传）/ `server`（路由 + SSE `command-pending` 发射 + 安全校验）/ `stream`（新增 `command-pending` 事件）；agent `core`（命令消费 + 读 `plugins` 纯逻辑 + 回传）/ 平台适配器（读真实 `plugins` 文件，路径安全 + 上限）；前端（反向抓取入口）。

**命令模型**：`agent_command` 表——`id` / `namespace` / `server_id` / `type`（`ingest-plugins`，落 VARCHAR）/ `payload`（TEXT JSON：目标组 / scope）/ `status`（`pending`/`fetched`/`done`/`failed`/`expired`，VARCHAR）/ `created_at` / `updated_at` / `result_detail`。GORM 可移植、无方言专有类型。真源在库（与注册 / 健康内存事实不同——命令需持久、可审计、跨断连重拉）。

**流程**：
1. admin `POST /admin/v1/instances/{serverId}/reverse-fetch`（写操作，需 full 角色，readonly → 403）→ 校验目标在线 → 建 `agent_command(pending)` + 审计 `file.reverse-fetch`（请求）→ 发 SSE `command-pending` 给该 agent。
2. agent 收 `command-pending` → `GET /beacon/v1/agent/commands`（取本机 pending 命令）→ 命令标 `fetched`。
3. agent 读 `plugins/` 子树（平台适配器在 async 线程读盘、不碰 MC 主线程；路径安全 + 排除 `.jar`/二进制 + 上限）→ `POST /beacon/v1/agent/files/ingest`（携带命令 id + 文件集 path→bytes）。
4. 控制面**同口径再校验**（路径 / 大小 / 数量 / 非 jar——控制面是入库前最后一道）→ `FileService.Import` 落为组 / 实例级覆盖（事务）→ 命令标 `done` + 审计 `file.ingest`（含目标 + 文件数）→ 唤醒文件长轮询。
5. 失败 / 超时：命令标 `failed`/`expired`；admin 侧经命令状态可见。

**安全**（摘要，正文见 [ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md)）：读取限死真实 `plugins/` 根内（`Path.normalize().startsWith`、禁符号链接逃逸 / `..` / 绝对 / UNC / `:`）；排除 `.jar` 与非文本二进制（沿 ADR-0011 禁 jar，只 ingest 文本配置）；上限复用 FR-38 import 常量（单文件 / 单次总字节 / 文件数），超限整体失败、**不部分入库**；agent 为最终权威、控制面入库前再校验；命令 admin 鉴权触发、触发 + ingest 入审计；agent 仅在收到鉴权命令时读盘上传，不主动、不常驻。

接口（详见 [API.md](../API.md)）：admin `POST /admin/v1/instances/{serverId}/reverse-fetch`（触发，结果经命令状态 / 审计 / 文件树体现）；agent `GET /beacon/v1/agent/commands`（拉 pending）、`POST /beacon/v1/agent/files/ingest`（回传）；SSE 新增 `command-pending` 事件。

## 4. 任务拆分

- [x] [ADR-0027](../adr/0027-reverse-fetch-channel-and-security.md)：反向取文件通道与安全面
- [x] PRD FR-39 → 开发中；ARCHITECTURE（命令通道 + ingest）；API.md（新端点 + SSE 事件）
- [x] 控制面 `model`/`repository`：`agent_command` 表 + 枚举 + CRUD + AutoMigrate + 单测
- [x] 控制面 `service`：命令编排（建 / 拉 / 收 / 超时清理）+ ingest 复用 `FileService.Import` + 审计 + 单测
- [x] 控制面 `handler`/`server`：admin 触发端点 + agent 拉命令 / 回传端点 + SSE `command-pending` 发射 + 路由 + 安全再校验 + 单测（触发端点先校验目标在线，namespace 走查询参数对齐 `/instances/{serverId}` 既有约定）
- [x] 控制面集成测试：触发→pending→agent 拉→回传→ingest→审计全链；安全（jar 拒 + 无残留）；缺 ns 400 / 离线 404 / readonly 403（真 MySQL 全绿）
- [x] agent `core`：`command-pending` 消费 + 读 `plugins` 纯逻辑（路径安全 + 上限 + 排除 jar/二进制）+ 回传 + 单测（CI 验证，本机 gradle 受 JAVA_HOME 限制不可跑）
- [x] agent 平台适配器：读真实 `plugins` 文件能力（bukkit / bungee 壳，含 `toRealPath` 容器校验 + 拒符号链接逃逸）
- [x] 前端：反向抓取入口（选在线实例 + 目标组 / 实例）+ 客户端 + dev mock + 测试（`pnpm build` + vitest 全绿）
- [ ] E2E：真机一台在线实例反向抓取→ingest→组覆盖生效（**属发版前门**，见 `testing-and-quality.md`；由 `sdd-release-version` 在 CI 跑——本机 agent JAR 不可构建，控制面全链已由集成测试覆盖、agent 读取/过滤/回传已由 Kotlin 单测覆盖）
- [x] 文档同步：PRD 状态 / ARCHITECTURE / API / ADR-0027 / CHANGELOG

## 5. 验收标准

- admin 对某在线 serverId 触发反向抓取 → 该 agent 读其 `plugins/` 文本配置回传 → 入库为指定**组 / 实例级**文件树覆盖，下游按覆盖链生效。
- readonly 密钥 / 角色触发 → `403`。
- 安全：agent 拒读 `plugins/` 外（`..` / 绝对 / 符号链接逃逸）；拒 `.jar` / 二进制；超单文件 / 总字节 / 文件数上限 → **整体失败、不部分入库**；控制面入库前同口径再校验、越界拒。
- 触发与 ingest 各入 `audit_log`（operator / action / target / detail，detail 不含敏感文件内容）；管理台审计页可查。
- 命令生命周期：`pending → fetched → done`；agent 离线 / 超时 → `expired`/`failed`，admin 侧可见。
- 受影响组件测试全绿（`go test ./...` 单元 + `-tags=integration` 集成；agent `gradle test`；前端 `pnpm build` + `pnpm test`）+ E2E 关键时序绿。

## 6. 风险 / 待定

- **安全面最大**：读整个 `plugins/` 可能含他插件 DB 密码 / 密钥——本特性把「现网真实配置」搬入控制面库，运维须知情（抓取 = 把目标机 `plugins` 文本配置集中到控制面）。上限 / 排除 jar / 审计为硬约束、不可放开。
- 大 `plugins` 目录的体量与传输：上限内一次性回传，超限失败不部分入库；是否分块传输属后续优化、本期不做。
- 命令通道是新机制：与 SSE 断连 / 重连、命令幂等（重复回传同 id）、超时清理需测。
- agent 读盘在 async 线程、不碰 MC 主线程（守架构不变量 #5）。
