# 规格：文件资产（第二版）

> 状态：草拟 · 关联 FR：FR-163,FR-164 · 阶段：P8（0.28.x）

## 1. 背景与目标

大规模交付（P9 变更单）之前，必须先「看清」每台子服 / BC 节点上实际落盘的插件与配置文件。第一版反向抓取 / 文件树原型（Legacy）以整盘快照 + 受管工作流为中心，与第二版区服权威模型脱节，已冻结维护态。

本域按第二版模型重建**只读的文件资产索引**：agent 周期扫描本机目录生成清单（路径 / sha256 / 大小 / mtime），增量上报控制面；控制面只存每服最新快照，支持按服务器 / 路径 / 扩展名 / 哈希搜索、跨服一致性比对、文本内容实时预览与 diff。挂载页面为「交付 → 文件资产」（`/assets`，UX.md §2），唯一职责是**看**——目录清单、哈希、内容预览与 diff，只读。

目标：

- 1000+ 子服规模下，运维能在后台快速回答「这台服上有什么文件、和别的服一致吗、内容是什么」。
- 为 P9 交付编排提供清单与哈希底座（模板源差异扫描复用本域能力）。
- 内容查看有安全边界：大小上限、二进制不传输、敏感路径默认禁看、查看必入审计。

## 2. 范围（做什么 / 明确不做什么）

范围内：

- agent 侧：扫描根与排除规则、周期 + 手动触发扫描、本地清单缓存、增量 diff 上报协议、单文件内容读取回传（复用 Legacy FsBrowse 同源的只读安全口径：路径遍历校验、符号链接不逃逸、纯只读）。
- 控制面：`file_asset` 最新快照存储、清单接收与摘要校准、搜索 / 分页 / 跨服比对查询、内容预览与 diff 的同步中转、敏感路径规则、审计事件。
- 管理面 API 与 `/assets` 页面契约（页面 mock 已在 P2 拍板，本阶段接真）。

明确不做（范围外）：

- **任何写操作**：文件同步、覆盖、删除、备份、还原一律不做，归 P9 交付编排（`v2-delivery-orchestration.md`）。本域 agent 面与管理面端点全部只读（对目标文件系统而言）。
- 文件内容历史版本、清单历史快照（只存最新，历史追溯是配置中心版本链与变更单的职责）。
- 目录实时惰加载浏览（Xftp 式交互，Legacy FR-109 原型不回流）；第二版以「清单快照 + 按需取内容」覆盖同类诉求。
- 大文件（超预览上限）内容传输——不属于「看」，需要搬运文件时走 P9 流式数据面。
- 统一权限风险分级框架（FR-168，P9 落地）；本域先按管理面登录态 + 敏感原因放行执行，P9 接入统一分级。

## 3. 数据模型（表 / 字段 / 索引要点）

共享实体（`namespace` / `server` 等）以基座 §3 与 `v2-zone-authority.md` 为权威，本域仅外键引用。

### 3.1 `file_asset`（每服最新清单，行 = 一个文件）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT 自增（GORM 抽象） | 主键 |
| namespace_id | BIGINT | 冗余自 server，隔离过滤与索引必需 |
| server_id | BIGINT | 引用 `server` |
| path | VARCHAR(512) | 相对服务器工作目录的规范化相对路径，`/` 分隔，如 `plugins/Foo/config.yml` |
| ext | VARCHAR(16) | 小写扩展名（无扩展名为空串），冗余列，支撑扩展名索引查询 |
| sha256 | CHAR(64) | 文件内容 sha256 小写 hex |
| size | BIGINT | 字节数 |
| mtime_ms | BIGINT | 文件修改时间，UTC epoch 毫秒（避免方言时区差异） |
| is_text | BOOLEAN | 扫描期按扩展名启发的文本提示（权威判定在预览期做） |
| scanned_at | DATETIME | 本行来自哪次扫描（UTC） |
| created_at / updated_at | DATETIME | 建表通用约定（`v2-zone-authority.md` §3.1），GORM 自动维护 |

索引：

- 唯一 `(server_id, path)`——最新快照语义的物理保证，增量 upsert 依据。
- `(namespace_id, sha256)`——按哈希找文件、跨服比对。
- `(namespace_id, ext)`——扩展名搜索。
- `(namespace_id, path)`——跨服同路径比对与路径前缀搜索（前缀查询用 `path LIKE 'plugins/Foo/%'` 左锚定走索引）。

容量估算：1000 服 × 约 2000 文件 = 200 万行量级，普通索引表可承载，不分片、不分日期表（区别于连接 / 消息明细）。

### 3.2 `file_asset_scan`（每服扫描概要，行 = 一台服务器）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT 自增 | 主键 |
| namespace_id / server_id | BIGINT | 同上，`server_id` 唯一 |
| manifest_digest | CHAR(64) | 当前清单摘要（算法见 §4.3），增量校准锚点 |
| file_count | INT | 清单文件数 |
| total_size | BIGINT | 清单总字节数 |
| truncated | BOOLEAN | agent 侧超单服文件数上限被截断 |
| scanned_at | DATETIME | 最近一次扫描完成时间（UTC） |
| scan_duration_ms | INT | 最近一次扫描耗时 |
| created_at / updated_at | DATETIME | 建表通用约定（`v2-zone-authority.md` §3.1），GORM 自动维护 |

### 3.3 敏感路径规则

持久化为运维设置中的一个字符串数组设置项（键 `assets.sensitive-path-patterns`，glob 模式列表），初始化种子为内置默认清单（§4.6），之后整表可编辑（含删改默认项），修改走设置变更审计。不单独建表。

## 4. 机制与状态机

### 4.1 扫描范围（agent 侧）

- **扫描根**（相对服务器工作目录）：
  - `plugins/`（插件 jar 与各插件配置子目录，Bukkit / BC 通用）；
  - 服务器根目录**顶层配置文件白名单**：`server.properties`、`bukkit.yml`、`spigot.yml`、`paper-global.yml`、`paper-world-defaults.yml`、`config.yml`、`velocity.toml`、`waterfall.yml`（存在才收，不递归根目录）。
- **默认排除规则**（glob，作用于相对路径，大小写不敏感）：`**/*.log`、`**/*.log.gz`、`**/*.tmp`、`**/*.lock`、`**/.git/**`、`**/logs/**`、`**/cache/**`、`**/crash-reports/**`、`**/.DS_Store`。
- 排除规则可在运维设置调整（设置键 `assets.scan-exclude-patterns`，热更下发，agent 下次扫描生效）；扫描根白名单为代码内置，不开放配置。
- 世界数据、logs 目录天然不在扫描根内；jar 属清单内容（哈希 / 大小 / mtime 全收，是跨服比对与 P9 差异扫描的核心对象），只是不可预览内容（二进制）。
- 单服清单**文件数上限 50000**：超限按路径字节序截断，`truncated=true` 随概要上报，页面明示「清单不完整」。

### 4.2 扫描频率与触发

- **周期扫描**：默认每 30 分钟一次，首次在 agent 确认接入后启动，起始加 0~10% 周期随机抖动（防 1000 台同时上报踩踏）；周期可由运维设置热更（键 `assets.scan-interval-sec`，下限 300）。
- **手动扫描**：管理面 `POST /admin/v2/assets/rescan` 对目标服创建 agent 命令（类型 `asset-rescan`，沿用 ADR-0006 长轮询命令通道），支持 `force=true` 忽略本地 mtime 缓存全部重哈希；命令生命周期与结果按命令通道通用模型可观测。
- **agent 执行约束**（架构不变量 §5）：扫描全程在 TabooLib async 线程执行；目录遍历用 java.nio、不跟随符号链接下降、解析后仍须落在扫描根内；哈希分块读（128 KiB）、逐文件让出，不阻塞 MC 主线程；`(size, mtime)` 与本地缓存一致则复用上次哈希，不重读文件。
- **fail-static**：控制面不可达时扫描照常执行并更新本地缓存，上报失败静默等待下个周期重试（不影响 agent 其他职责，不重试风暴）。

### 4.3 清单上报协议：增量 diff 为主，全量兜底

**选型定案**：增量 diff（默认）+ 摘要校准 + 全量快照兜底。理由：清单在稳定运行期变化极小（多数周期为零变更），全量每次 2000 行 × 1000 服的上报对控制面是无谓写放大；增量协议用摘要锚点保证收敛，失配即退全量，不引入复杂对账。

- **本地缓存**：agent 在自身数据目录持久化 `asset-manifest.json`（上次成功上报后的清单 + 服务端确认摘要）。重启后缓存可用则继续增量；缓存缺失 / 损坏则全量。
- **清单摘要算法**：条目按 `path` 字节序升序，每条串接 `path + "\n" + sha256 + "\n" + size + "\n" + mtime_ms + "\n"`，对整体取 sha256。agent 与控制面同算法，摘要相等即两侧清单一致。
- **增量上报**（`mode=delta`）：携带 `baseDigest`（agent 认为控制面当前持有的摘要）+ `upserts[]` + `deleted[]`。控制面校验 `baseDigest` 与 `file_asset_scan.manifest_digest` 一致才应用（单事务：批量 upsert + 删除 + 更新概要行），返回新摘要；agent 比对返回摘要与本地期望，不一致下次转全量。`baseDigest` 失配返回 409（`code=asset_manifest_out_of_sync`），agent 立即改发全量。零变更周期不发请求（本地新摘要 == 已确认摘要即跳过），仅当概要需要刷新 `scanned_at` 时每 6 个周期发一次空 delta 保活。
- **全量上报**（`mode=full`）：首次接入、缓存缺失、409 后、`force` 重扫后使用。单请求条目上限 2000，超限分片：agent 生成 `uploadId`，按 `seq` 递增分批发送，末批 `eof=true`；控制面按 `uploadId` 内存暂存分片（TTL 5 分钟，过期丢弃），收齐后**单事务整体替换**该服全部 `file_asset` 行并更新概要。全量期间旧快照持续可查（替换是原子事务）。
- **并发与幂等**：同一 server 的上报按 server 串行处理（摘要乐观锁天然拒绝并发交叉写）；重复发送同一 delta（摘要已推进）会 409 转全量，最终收敛，无脏数据。
- 入库为常规同步事务（请求分片已限 2000 条，单事务毫秒级），不引入异步队列（区别于指标批量入库基调，清单写入频度低得多）。

### 4.4 搜索、分页与跨服比对（管理面）

- **搜索**：`namespaceId` 必填（强隔离 + 保证走索引，禁止无 namespace 全表扫描）；可组合 `serverId`、`pathPrefix`（左锚定）、`name`（文件名包含，兜底条件，需与其他至少一个索引条件组合，否则拒绝——防无索引慢查询）、`ext`、`sha256`。强制分页，`pageSize ≤ 200`，默认 50。
- **跨服哈希比对**：给定 `namespace + path`（精确相对路径）+ 范围（整 namespace / bc 集群 / 大区 / 小区 / 显式 serverId 列表），控制面按 `(namespace_id, path)` 索引单查，把命中行按 sha256 分组返回：每组含 sha256、size、成员服务器（serverId / mtime / scanned_at），另返回范围内**没有该文件**的服务器列表；最大组即多数派，前端据此标注少数派差异。范围内服务器数不设上限但成员列表分页（每组默认展开前 50 台）。
- **页面性能边界**（UX §4 契约）：所有列表服务端筛选分页；比对视图按组折叠；超大量（如比对范围 1000 台）截断并明示边界；查询接口目标为纯索引查询，不做实时聚合重算。

### 4.5 文本内容预览与 diff（实时向 agent 取）

清单是快照，内容是实时——控制面**不存文件内容**，每次预览向 agent 现取：

1. 管理面 `POST /admin/v2/assets/preview` → 控制面校验敏感规则与在线状态 → 创建 agent 命令（类型 `asset-read`，payload：path、maxBytes）→ handler 注册等待器同步等待。
2. agent 长轮询取到命令，async 线程读文件（只读原语口径：路径校验、符号链接不逃逸），回传 `POST /beacon/v2/agent/assets/content`。
3. 控制面唤醒等待器，把内容透传前端；**内容为瞬态，不落库、不进审计 detail、不缓存**（每次查看都是一次受审计的实时读取）。
- **大小上限**：文本预览上限 **512 KiB**。超限时 agent 只读取并回传前 512 KiB，响应带 `truncated=true`，前端明示「已截断，完整内容超出预览上限」；不支持继续翻页读尾部（需要完整文件时走 P9 数据面）。512 KiB 属小载荷，经 agent 面 JSON 回传不违反「大文件必须走流式数据面」约束（该约束针对交付级文件搬运）。
- **同步等待超时**：默认 10 秒。超时 / agent 离线 / 命令失败 → 返回明确错误（`code=asset_preview_timeout` / `asset_agent_offline` 等），按 ADR-0057 脱敏展示，不静默。
- **二进制识别（两层）**：扫描期按扩展名启发写 `is_text` 供列表提示；预览期 agent 权威判定（内置二进制扩展名黑名单如 `.jar/.zip/.png` + 内容含 NUL 字节或非法 UTF-8）。判定为二进制 → 不传输内容，只返回元数据（path / sha256 / size / mtime / `binary=true`），前端只展示元数据卡。
- **diff**：`POST /admin/v2/assets/diff` 指定左右两个 `(serverId, path)`（可同服不同路径或跨服同路径）。控制面先查两侧清单哈希，相同则短路返回 `identical=true` 不取内容；不同则并行向两侧 agent 取内容，双侧齐活后一并返回，diff 渲染在前端做。任一侧为二进制或超 512 KiB → 拒绝 diff（`code=asset_diff_unsupported`），提示改用哈希比对。

### 4.6 敏感路径规则与放行

- **默认禁止查看模式清单**（glob，大小写不敏感，命中即禁）：

  ```
  **/*secret*        **/*password*      **/*credential*
  **/*.pem           **/*.key           **/*.jks           **/*.p12
  **/.env            **/.env.*          **/token.*
  plugins/Beacon/**  （agent 身份文件与本地缓存目录）
  ```

- 规则只作用于**内容查看**（preview / diff）；清单元数据（路径 / 哈希 / 大小 / mtime）不受限——运维要能看见敏感文件「存在且是否一致」，只是默认看不到内容。
- **放行机制（单次原因放行）**：命中敏感规则的预览 / diff 请求必须携带非空 `reason`；无 reason → 403（`code=asset_sensitive_path`，响应标注 `sensitive=true` 供前端弹原因框）。携带 reason 的请求放行**本次**，审计记录加标 `sensitiveOverride=true` + 原因原文。放行不修改规则，不产生持续豁免。
- **规则编辑**：`GET/PUT /admin/v2/assets/sensitive-rules` 查看与整体替换规则清单（含默认项），每次修改写审计（前后差异入 detail）。清空清单等价关闭敏感保护，允许但同样入审计。
- 规则匹配在**控制面**执行（命令下发前拦截），agent 不感知敏感语义（agent 只守只读与路径安全口径）。

### 4.7 查看行为审计

审计接入统一审计设施（P5 落地），本域登记以下事件类型，全部含操作者、namespace、serverId、path、结果（成功 / 超时 / 拒绝）、traceId：

事件命名统一「点分小写 `<域>.<动作>`」形态（与 `identity.*`、`cross_namespace.*`、`delivery.order.*`、`message.payload.view` 一致；`asset-rescan` / `asset-read` 是命令类型，不受此约束）：

| 事件 | 附加字段 |
|---|---|
| `asset.preview` | truncated、binary、sensitiveOverride、reason（命中敏感时） |
| `asset.diff` | 左右两侧 (serverId, path)、identical、sensitiveOverride、reason |
| `asset.rescan` | 目标 serverId 列表、force |
| `asset.sensitive_rule_update` | 规则前后差异 |

文件内容永不写入审计 detail（对齐 payload 类瞬态数据纪律）。

## 5. API 契约（供 docs/API.md 汇总）

鉴权沿用基座 §2：agent 面 `X-Beacon-Token` + `X-Beacon-Identity`（未确认 agent 无权调用本域端点）；管理面沿用登录令牌 / API 密钥。错误统一 `{code,message,traceId}`。

### 5.1 agent 面

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| POST | `/beacon/v2/agent/assets/manifest` | `mode`(full/delta)、`scannedAt`、`scanDurationMs`、`truncated`；delta：`baseDigest`+`upserts[]`(path/sha256/size/mtimeMs/isText)+`deleted[]`；full 分片：`uploadId`+`seq`+`eof` | `digest`（应用后清单摘要）、`fileCount`；delta 基线失配 409 `asset_manifest_out_of_sync` |
| POST | `/beacon/v2/agent/assets/content` | `commandId`、`path`、`sha256`、`size`、`binary`、`truncated`、`content`（UTF-8 文本；binary 时缺省）、`error`（读取失败原因） | 200 确认；命令等待器已超时则丢弃内容仅标记命令完成 |

下行命令（经 ADR-0006 长轮询命令通道，非独立端点）：`asset-rescan`（payload：force）、`asset-read`（payload：path、maxBytes）。

### 5.2 管理面

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| GET | `/admin/v2/assets` | query：`namespaceId`(必)、`serverId`、`pathPrefix`、`name`、`ext`、`sha256`、`page`、`pageSize`(≤200) | `items[]`(serverId/path/ext/sha256/size/mtimeMs/isText/scannedAt)、`total` |
| GET | `/admin/v2/assets/scan-status` | query：`namespaceId`(必)、`serverId`、`page`、`pageSize` | 每服概要：`manifestDigest`、`fileCount`、`totalSize`、`truncated`、`scannedAt`、`scanDurationMs` |
| GET | `/admin/v2/assets/compare` | query：`namespaceId`(必)、`path`(必,精确)、范围三选一：`clusterId`/`regionId`/`zoneId`/`serverIds` | `groups[]`(sha256/size/servers[](serverId/mtimeMs/scannedAt))、`missing[]`(无此文件的 serverId)、组内成员分页 |
| POST | `/admin/v2/assets/rescan` | `namespaceId`、`serverIds[]`(≤100)、`force` | 每服创建的命令 id 列表；离线服标记入响应不阻断整批 |
| POST | `/admin/v2/assets/preview` | `serverId`、`path`、`reason`(命中敏感时必填) | `content`、`truncated`、`binary`、`sha256`、`size`、`sensitive`；超时 / 离线 / 敏感未放行返回对应 code |
| POST | `/admin/v2/assets/diff` | `left{serverId,path}`、`right{serverId,path}`、`reason`(任一侧敏感时必填) | `identical` 或 双侧 `content`+元数据；二进制 / 超限拒绝 `asset_diff_unsupported` |
| GET | `/admin/v2/assets/sensitive-rules` | — | `patterns[]` |
| PUT | `/admin/v2/assets/sensitive-rules` | `patterns[]`（整体替换） | 应用后清单；写审计 |

## 6. 与其他规格的边界

- **本域只读**：对目标文件系统零写入。文件同步、覆盖、备份、还原、模板源差异下发等一切写操作归 `v2-delivery-orchestration.md`（FR-162/165/167）；但**模板源差异扫描复用本域能力**——P9 以本域清单快照与 `manifest_digest`/sha256 比对为差异计算输入，本域协议字段（path/sha256/size/mtimeMs）即差异扫描的数据契约，P9 不另建一套清单。
- **实体与隔离**：`namespace`/`server`/zone 层级引用基座 §3 与 `v2-zone-authority.md`；namespace 强隔离在本域的落点（查询强制 namespace、agent token 匹配校验、跨域拒绝）遵循 `v2-namespace-isolation.md`。
- **身份与命令通道**：agent 鉴权、未确认 agent 的能力限制依 `v2-agent-identity.md`；`asset-rescan`/`asset-read` 命令生命周期沿用命令通道通用模型，本域只定义命令类型与 payload。
- **审计**：审计存储与查询设施依 P5 相关规格；本域只登记事件类型（§4.7）。
- **权限分级**：统一风险分级与二次确认框架归 FR-168（`v2-delivery-orchestration.md`）；本域 P8 先以「登录态 + 敏感原因放行」执行，P9 框架落地后 preview/diff/规则编辑纳入统一分级，契约不变。
- **运维设置**：`assets.scan-interval-sec`、`assets.scan-exclude-patterns`、`assets.sensitive-path-patterns` 挂运维设置（`/settings`，FR-158 设施），热更下发机制依 P6 设置规格。
- **页面**：`/assets` 页面 mock 拍板归 P2（FR-172），本阶段接真；页面结构变更须重过评审门（`.claude/rules/ux-spec.md`）。

## 7. 验收标准

对齐 PRD FR-163/164 验收摘要展开，全部可验证：

1. **清单闭环**：新接入并确认的 agent 在一个扫描周期内出现在 `/assets`，清单含 `plugins/` 全部未排除文件与根白名单配置文件，路径 / sha256 / 大小 / mtime 与磁盘实况一致（真机比对抽样）。
2. **增量收敛**：改 / 增 / 删文件后下个周期仅增量上报且控制面快照与磁盘一致；人为制造摘要失配（改库）后 agent 收 409 自动转全量并收敛（集成测试）。
3. **全量分片原子性**：>2000 文件的全量上报分片入库，替换期间查询不出现半截清单；分片超时未收齐不产生脏数据。
4. **不阻塞**：扫描 / 哈希 / 上报全在 async 线程，主线程零阻塞（沿用 agent 线程守卫测试口径）；控制面不可达时 agent 扫描照常、无异常刷屏、玩家不受影响（fail-static）。
5. **搜索与分页**：按 serverId / 路径前缀 / 扩展名 / sha256 组合搜索均走索引返回分页结果；无 namespace 或仅 name 单条件的请求被拒绝；200 万行量级下页面可操作不阻塞。
6. **跨服比对**：对同一 path 在选定范围内返回哈希分组 + 缺失服列表，能一眼识别少数派差异服；1000 台范围比对返回截断明示。
7. **文本预览与 diff**：文本文件可预览，超 512 KiB 截断并明示；两侧哈希相同的 diff 短路返回一致；哈希不同返回双侧内容可渲染 diff；二进制 / 超限 diff 被拒绝并提示。
8. **二进制边界**：jar 等二进制文件列表可见元数据（含哈希），预览只返回元数据不传内容。
9. **敏感保护**：命中默认敏感清单的文件无 reason 预览被 403；填 reason 后放行且审计带 `sensitiveOverride` 与原因；规则修改生效并入审计。
10. **审计完备**：preview / diff / rescan / 规则修改全部产生审计事件，可在 `/audits` 按 serverId / path / 操作者追溯；审计 detail 不含文件内容。
11. **错误可见**：agent 离线、预览超时、命令失败均返回脱敏真因并 toast 展示（ADR-0057），无静默失败。

## 8. 风险 / 待定（默认决定集中登记，待拍板）

1. **上报协议选型**：定为「增量 diff + 摘要校准 + 全量兜底」（§4.3），弃纯全量快照方案——默认决定待拍板。
2. **默认数值**：扫描周期 30 分钟（下限 300s）、单服文件数上限 50000、单请求条目上限 2000、全量分片暂存 TTL 5 分钟、预览上限 512 KiB、预览同步等待超时 10s、pageSize 上限 200——均为估值默认，待真机压测校准。
3. **扫描根白名单**：根目录顶层配置文件清单（§4.1）按主流 Bukkit/Paper/BC/Velocity 发行版拟定，可能需按实际部署补充；白名单定为代码内置不开放配置（YAGNI）——待拍板。
4. **敏感默认清单**：§4.6 模式为拟定值（含 `plugins/Beacon/**` 整目录），`**/*password*` 类通配可能误伤（如 `password-strength.yml` 之类业务配置），靠单次原因放行兜底而非收窄模式——待拍板。
5. **预览内容走 agent 面 JSON 回传**（≤512 KiB 小载荷）而非流式数据面：判断该约束针对交付级搬运，预览属控制面编排级小消息——若认为一切文件内容都须走数据面则需改走 `/beacon/v2/stream/*`，待拍板。
6. **全量分片内存暂存**：控制面重启丢弃未收齐分片、agent 下周期重传，接受这一窗口（不落临时表）——待拍板。
7. **预览采用同步等待**（HTTP 请求挂起 ≤10s）而非「创建任务 + 前端轮询」两段式：依据长轮询下行延迟低、交互显著更简洁；若未来预览排队严重再演进——待拍板。
8. **扫描相关设置挂运维设置**（FR-158 设施，P6 已落地）而非本域自建设置面——默认决定。
9. **mtime 用 UTC epoch 毫秒 BIGINT**、`ext` 冗余列、`file_asset` 不分片不分日期表（200 万行量级单表 + 索引可承载）——默认决定。
10. **diff 渲染在前端**（控制面只透传双侧内容），与配置中心 diff 组件复用同一前端能力——默认决定。
