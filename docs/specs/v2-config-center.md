# 规格：配置中心 V2（第二版）

> 状态：草拟 · 关联 FR：FR-160, FR-161 · 阶段：P7（0.27.x）

## 1. 背景与目标

Legacy 配置中心（通道A：`config_item` + scope 覆盖链 + 长轮询下发）在第一版探索期与文件树、工作台原型互相堆叠，作用域模型（global/group/zone/server）也与第二版区服权威模型（namespace → bc_cluster → region → zone → server）不一致。第二版按 PRD §1.3 冻结旧原型，在 P7 以第二版权威模型重建配置中心。

本域目标：

- 配置作用域与区服权威模型（五层）完全一致，继承覆盖可解释——每个有效值能指出来自哪一层、哪个版本（FR-160）。
- 提供结构化编辑、schema 校验、diff、不可变版本链与回退（FR-161）。
- 敏感值标记 + 全链路脱敏：前端、日志、审计 detail 不出现明文（FR-161）。
- **本域只产出「已定稿的配置版本」**；配置的下发、生效、灰度、生效回滚全部由变更单承载（P9，`v2-delivery-orchestration.md`），P7 不做任何下发链路。

## 2. 范围

### 做什么

- 配置文件（作用域化）的创建、元数据管理、五层作用域覆盖编辑。
- 配置文件回收站：删除 = 软删可恢复（版本链保留），彻底删除（purge）才物理清除连带版本链（§4.9）。
- 键级深合并的有效配置解析、有效配置预览、逐键值来源解释（层 + 版本）。
- 语法与 schema 校验（保存前阻断）、编辑器实时校验端点。
- 版本间 / 层间键级 diff。
- 每层不可变版本链、回退（基于历史版本生成新版本）、撤销某层贡献。
- 敏感键路径标记与统一脱敏出口（读端点、日志、审计 detail）。
- 跨 namespace 强隔离在配置面的落点。
- `/configs` 页面从 mock 接真（页面骨架已在 P2 拍板）。

### 明确不做什么

- **不做配置下发 / 生效 / 灰度 / 生效观察 / 整单回滚**——全部走 P9 变更单（FR-162/165/166/167/171）。本域无 agent 面端点，`/beacon/v2/agent/*` 下不新增配置接口。
- 不做配置静态加密（PRD §1.3 明确不做；脱敏是展示层控制，不是存储加密）。
- 不做配置灰度 / Beta / canary 字段（灰度是变更单编排引擎的事，且载荷无关）。
- 不动 Legacy 通道（`/beacon/v1`、`/admin/v1` 配置与文件树接口维护态冻结）。
- 不做跨 namespace 配置共享或模板市场；`namespace_trust` 的互通能力不包含配置（见 §8-10）。
- 不做服务端草稿态、多人协同编辑锁（见 §8-3）。

## 3. 数据模型

五层实体（`namespace` / `bc_cluster` / `region` / `zone` / `server`）以 `v2-zone-authority.md` 为权威，本域只按 id 引用，不复制定义。所有枚举落 VARCHAR + 应用层校验，json 落 TEXT，禁 MySQL 专有特性。

### 3.1 组织方式：按文件（拍板）

配置以**文件**为一等公民组织：一个 `config_file` 对应目标机上的一个逻辑配置文件（如 `plugins/Foo/config.yml`）。编辑、版本、diff、回退、变更单载荷引用都以文件为单元；**键**只是文件内部的合并与来源解释粒度。理由：

- 运维与业务插件的心智单元就是「某插件的某配置文件」；
- 与 P8 文件资产、P9 变更单的载荷单位（文件）天然对齐；
- 键级组织（Legacy `config_item` 扁平列表）已被第一版证明看不清覆盖链全貌。

### 3.2 `config_file`（配置文件）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT PK 自增（GORM 抽象） | |
| namespace_id | BIGINT 非空 | 归属 namespace，创建后不可改 |
| name | VARCHAR(255) 非空 | namespace 内唯一逻辑名，建议用目标相对路径形式（如 `plugins/Foo/config.yml`） |
| format | VARCHAR(16) 非空 | `yaml` / `json` / `properties`，应用层校验，创建后不可改 |
| description | VARCHAR(512) | 用途说明 |
| schema_json | TEXT 可空 | JSON Schema（见 §4.4），空 = 不做 schema 校验 |
| sensitive_paths | TEXT 可空 | JSON 数组，敏感键路径列表（见 §4.7） |
| deleted_at | DATETIME 可空 | UTC；非空 = 已移入回收站（软删标记），NULL = 未删除（见 §4.9） |
| deleted_by | VARCHAR(64) 可空 | 移入回收站的操作者；恢复时与 deleted_at 一并清空 |
| created_by / created_at / updated_at | | |

索引：普通索引 `(namespace_id, name)`；**name 唯一性由应用层校验且只对未删除（`deleted_at IS NULL`）文件生效**——回收站内允许同名多条（见 §4.9）。

### 3.3 `config_layer_version`（层版本，不可变）

每个「文件 × 作用域」组合拥有一条独立的**不可变追加链**。

| 字段 | 类型 | 说明 |
|---|---|---|
| id | BIGINT PK 自增 | |
| config_file_id | BIGINT 非空 | |
| scope_level | VARCHAR(16) 非空 | `namespace` / `bc_cluster` / `region` / `zone` / `server` |
| scope_ref_id | BIGINT 非空 | 对应层实体 id（namespace 层 = namespace.id，server 层 = server.id，余类推） |
| version_no | INT 非空 | 链内从 1 单调递增 |
| content | TEXT | 归一化后的配置内容（见 §4.2）；撤销版本为空串 |
| content_hash | CHAR(64) | 归一化内容的 sha256 小写 hex |
| is_removal | BOOL 默认 false | true = 该版本表示「此层撤销贡献」 |
| based_on_version_id | BIGINT 可空 | 编辑基线 / 回退来源版本 id |
| remark | VARCHAR(255) | 本次变更说明 |
| created_by / created_at | | |

索引：唯一 `(config_file_id, scope_level, scope_ref_id, version_no)`。

约束（应用层强制）：

- **不可变**：本表只 INSERT，永不 UPDATE / DELETE 单行（文件彻底删除 purge 除外，见 §4.9）。
- **head 定义**：链内 `version_no` 最大的一行即当前生效头；head 为 `is_removal=true` 时该层视为「无贡献」。
- `scope_ref_id` 指向的实体必须归属于 `config_file.namespace_id`（跨 namespace 写入直接拒绝，见 §4.8）。
- 单版本 `content` 上限 1 MiB（超限拒绝，`CONFIG_CONTENT_TOO_LARGE`）。

### 3.4 数据量与归档

配置版本是低频人工写入的小表（对比连接 / 消息明细），**不做日期分表、不进 P6 归档**，全量留热库以保证版本链完整可溯。

## 4. 机制与状态机

### 4.1 五层作用域与继承覆盖（写死）

作用域继承链 = 基座 §3 层级链，低 → 高：

```
namespace → bc_cluster → region → zone → server
```

高层覆盖低层。合并为**键级深合并**，语义与 Legacy ADR-0029 一致（同一套规则、第二版重新写死为本域契约）：

1. **标量覆盖**：同键标量，高层值覆盖低层值。
2. **map 深合并**：同键两侧均为 map 时递归按键合并。
3. **list 整体替换**：同键任一侧为 list 时高层整体替换，不做元素级合并。
4. **null 删键**：高层某键显式为 `null` 时，删除低层该键（结果中不出现）；`yaml` / `json` 生效，`properties` 无此能力（值 `null` 是普通字符串，原样保留）。
5. **确定性输出**：合并结果按固定键序序列化，保证 `content_hash` / 有效 hash 幂等。

目标解析链：对某 server，经 `server.zone_id → zone.region_id → region.bc_cluster_id` 解出完整五层；**未分配 zone 的 server 只有 namespace、server 两层参与合并**。每层取其 head 版本（`is_removal=true` 或链不存在则该层不贡献），低 → 高依次合并。

保真度拍板：配置中心 V2 是**类型化配置存储**，保存与合并均经 parse → 规范序列化，**值归一化与注释丢失可接受**（沿用 ADR-0034 中对通道A 的同一判断；需要逐字节保真的文件属 P8 文件资产 / P9 变更单文件载荷的场景，不进配置中心）。

### 4.2 保存新版本（编辑 → 定稿）

无服务端草稿态：编辑器在前端持稿，**保存即定稿为新的不可变版本**。服务端处理顺序：

1. 校验 scope 合法：`scope_level` + `scope_ref_id` 存在且归属文件 namespace，否则 `CONFIG_SCOPE_MISMATCH`。
2. 语法解析：按 `format` parse，失败 `CONFIG_SYNTAX_INVALID`（含行列与原因）。
3. 敏感占位符回填（见 §4.7）。
4. schema 校验（见 §4.4），失败 `CONFIG_SCHEMA_VIOLATION`（逐条路径 + 原因），**拒绝落库**。
5. 乐观并发：请求携带 `basedOnVersionId`，必须等于该链当前 head 的 id（链为空时传 null）；不等则 `409 CONFIG_VERSION_CONFLICT`，前端提示重新加载合并。
6. 归一化：parse 后按固定键序规范序列化，计算 sha256；与 head 内容 hash 相同则拒绝（`400 CONFIG_NO_CHANGE`，不产生空版本）。
7. 事务内取 `max(version_no)+1` 插入新版本（唯一索引兜底并发插入），提交后写审计。

### 4.3 有效配置预览与值来源解释

- 输入：`config_file_id` + 目标（`server_id`，或假想目标 `zone_id` / `region_id` / `bc_cluster_id` / 仅 namespace——用于预览「分到某区会生效什么」）。
- 输出：
  - `effectiveContent`（脱敏后）与 `effectiveHash`（脱敏**前**内容的 sha256，与下发渲染一致，供比对）；
  - `provenance`：逐键 `{path, scopeLevel, scopeRefId, scopeName, versionNo}`——每个叶子值来自哪层哪版本；
  - `deletedKeys`：被高层 `null` 删除的键及执行删除的层 / 版本；
  - `layers`：参与合并的各层摘要（head 版本号、hash、更新人 / 时间），无贡献层明确标示。
- 解析实现为无副作用纯函数（合并 + provenance 平行实现须与主合并结果交叉测试一致，沿用 Legacy ADR-0013 的防漂移手法）；provenance 计算不改变合并结果 hash。

### 4.4 schema 校验

- **schema 从哪来**：管理员在配置文件元数据里维护 JSON Schema（`schema_json`，Draft 2020-12 子集：type / properties / required / enum / minimum / maximum / pattern / items / additionalProperties）。schema 是文件级属性、不带独立版本链；修改 schema 入审计。保存 schema 时先校验其本身是合法 JSON Schema，否则拒绝。
- **校验对象与模式**：层内容是增量（可能只含少数键），故按**部分校验**执行——只校验提交内容中出现的键的类型 / 枚举 / 取值范围 / 结构；`required` 完整性只对 **namespace 层**（完整基线层）强制。显式 `null`（删键指令）跳过类型校验。
- **失败行为**：保存版本时校验失败 → `400 CONFIG_SCHEMA_VIOLATION`，逐条返回 `{path, message}`，**不落库**；编辑器通过 validate 端点用同一引擎做实时校验（不落库、不审计）。
- 无 schema 的文件只做语法校验。
- `properties` 格式的 schema 按扁平键名匹配 `properties` 定义（值均按 string 校验 pattern / enum）。

### 4.5 diff（版本间 / 层间）

统一 diff 端点，两侧各接受一种描述：

- `version:<versionId>`——某历史版本内容；
- `scope:<scopeLevel>:<scopeRefId>`——某层当前 head 内容；
- `effective:<targetType>:<targetId>`——某目标的有效合并结果。

三者可任意组合，覆盖「版本间 diff」（同链两个版本）、「层间 diff」（如 zone head vs server head）、「目标间 diff」（两台 server 的有效结果）。输出为键级差异：`added` / `removed` / `changed`（含左右值）+ 可选整文本 unified diff；敏感键的值按 §4.7 脱敏后再进入 diff 输出。

### 4.6 版本不可变链、回退与撤销

- **head = 最新定稿版本，非「当前生效版本」**：链 head 只代表最新定稿；线上「生效版本」由 P9 变更单域持有，配置中心不追踪生效态、无 agent 下发通道（§2）。任何 save（新增 / 编辑 / 回退 / 撤销）都只改定稿、对线上无即时影响，生效必须由变更单引用版本并走灰度编排（模型见 [ADR-0071](../adr/0071-config-gray-effectuation-model.md)）。
- **不可变链**：版本只追加；无任何修改 / 删除单版本的端点。`based_on_version_id` 串起编辑 / 回退谱系。
- **回退 = 基于历史版本生成新版本**：对链内任一历史版本执行 rollback → 在同链追加一个新版本，`content` 取该历史版本内容、`based_on_version_id` 指向它、`remark` 自动注明「回退自 v<N>」。回退同样过 §4.2 全部校验（含 schema——schema 可能已收紧，历史内容不再合法时回退被阻断并明示原因）。内容与当前 head 相同时拒绝（`CONFIG_NO_CHANGE`）。
- **撤销层贡献**：对某链执行撤销 → 追加一个 `is_removal=true`、内容为空的版本；此后该层不参与合并。head 已是 removal 时拒绝重复撤销。恢复贡献 = 对该链任一历史版本 rollback。
- 回退与撤销**只改变「定稿态」**，对线上无任何即时影响——生效必须由 P9 变更单引用新版本并走编排。前端在这两个操作的确认框中固定提示这一点。

### 4.7 敏感值标记与脱敏

- **标记**：`sensitive_paths` 为文件级键路径列表（如 `["database.password", "api.token"]`），精确路径匹配，命中键的叶子值即敏感值。修改 `sensitive_paths` 属高风险操作：二次确认 + 原因 + 审计。
- **读出口统一脱敏**（后端做，非前端做）：所有管理面读端点（版本详情、有效预览、diff、审计 detail）中命中路径的值替换为固定占位符 `__BEACON_MASKED__`。**不存在返回明文的管理面端点**，因此无「查看敏感值」的场景与审计——敏感值 write-only。
- **写回填**：保存版本时，提交内容中敏感键的值若等于占位符 `__BEACON_MASKED__`，服务端回填该链上一版本（head）中该键的明文后再归一化落库；提交了非占位符的新值则直接采用。占位符出现在新增键 / 无上一版本可回填处 → `400 CONFIG_SENSITIVE_PLACEHOLDER_INVALID`。
- **日志与审计**：日志不打任何 `content`；审计 detail 只记键路径级摘要（新增 / 修改 / 删除了哪些路径），敏感路径只记「值已变更」，任何位置不落明文。
- **hash 基于明文**：`content_hash` / `effectiveHash` 均对脱敏前内容计算（保证与 P9 下发渲染一致），hash 本身不可逆、可对外展示。
- **不做静态加密**：DB 内 `content` 为明文 TEXT（PRD §1.3 既定非目标），依赖数据库访问控制。

### 4.8 跨 namespace 隔离落点

- `config_file` 归属单一 namespace；所有端点强制携带 / 推导 namespace 并校验一致。
- 写入校验 `scope_ref_id` 归属链最终落在文件的 namespace，跨 namespace 引用直接 `CONFIG_SCOPE_MISMATCH` 拒绝——配置值不可能被解析到其他 namespace 的目标上。
- 有效解析与 diff 的目标（server / zone / …）同样必须属于文件 namespace。
- `namespace_trust` 互通能力**不包含配置**：不存在「信任后共享配置」的通路（拍板，见 §8-10）；需要复用的配置在各 namespace 各建各管。
- 列表端点必须按 namespace 过滤，不提供跨 namespace 聚合视图。

### 4.9 配置文件删除（回收站软删除）

删除分两级：常规删除 = 移入回收站（可恢复），物理删除只能对回收站内文件执行（purge）。

- **移入回收站（DELETE）**：仅置 `deleted_at` / `deleted_by` 软删标记，全部层版本链原样保留。软删后文件不出现在常规列表、不参与合并渲染与有效解析（进程内明文有效渲染接口同样拒绝；变更单侧自存载荷快照，不受影响）；除回收站列表 / restore / purge 外，其余端点对该文件一律 404（视同不存在）。审计 `config.file.trash`。
- **名称唯一性**：`(namespace_id, name)` 唯一性由应用层校验且只对未删除文件生效，回收站内允许同名多条（同名文件可先删后建）。
- **恢复（restore）**：清空软删标记，覆盖链与版本历史完整如初。**冲突规则写死**：若该名称已被某个未删除文件占用 → `409 CONFIG_FILE_DUPLICATE`，须先将占用者移入回收站或彻底删除后再恢复（`name` 无改名端点，不提供恢复时改名）。审计 `config.file.restore`。
- **彻底删除（purge）**：仅对回收站内文件可执行（未软删调用 → `400 CONFIG_FILE_NOT_TRASHED`）；物理删除 `config_file` 连带其全部层版本链。高风险操作：二次确认 + 原因必填；审计 `config.file.purge`，detail 记录文件名、format、各链最终版本号与 hash 摘要（不含内容），保证事后可追溯「删了什么」。被 P9 变更单历史引用的版本 id purge 后悬空——变更单侧须自存载荷快照，不回查本域（边界见 §6）。

### 4.10 `/configs` 页面交互要点

页面挂载「交付」大域 `/configs`（UX.md §2），唯一职责：**改**——作用域配置编辑、校验、版本管理（下发走变更单）。P2 已 mock 拍板，本阶段接真：

- 三段式布局：文件列表（按 namespace 过滤 + 搜索分页）→ 该文件的作用域覆盖链（五层树，标示各层有无贡献 / head 版本）→ 编辑器（结构化编辑 + 实时校验）与有效预览（目标选择 + 逐键来源色块 + 删除键列表）。
- 版本历史抽屉：链内版本列表、任两版 diff、rollback 入口。
- 回收站为文件列表区内的视图切换（常规 / 回收站），**不新增独立页面**；回收站视图内提供恢复与彻底删除，purge 走二次确认 + 原因必填并明示「不可恢复」。
- 敏感值在编辑器中显示占位符，替换新值时明示「保存后不可再查看明文」。
- 空态（引导创建首个文件）/ 加载骨架 / 错误可重试 / 1000+ 子服目标选择用服务端搜索（全局交互契约 UX.md §4）。

## 5. API 契约

全部挂 `/admin/v2/*`，沿用管理面登录令牌 / API 密钥；错误统一 `{code, message, traceId}`，时间 UTC。读端点响应一律经 §4.7 脱敏。

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| GET | `/admin/v2/config-files` | `namespaceId`（必填）、`keyword`、`serverId`（可选：只列对该 server 有生效贡献的文件并附有效 hash）、`page`/`pageSize` | 分页文件列表（不含回收站）：id、name、format、描述、有贡献层数、更新时间 |
| POST | `/admin/v2/config-files` | `namespaceId`、`name`、`format`、`description?`、`schemaJson?`、`sensitivePaths?` | 201 文件详情；重名 `409 CONFIG_FILE_DUPLICATE` |
| GET | `/admin/v2/config-files/{id}` | — | 文件元数据 + 各层覆盖概览（同 scopes 端点摘要） |
| PATCH | `/admin/v2/config-files/{id}` | `description?`、`schemaJson?`、`sensitivePaths?`（改敏感路径需 `reason`） | 更新后详情；schema 非法 400 |
| DELETE | `/admin/v2/config-files/{id}` | `reason?` | 204；移入回收站（软删除，版本链保留），入审计（§4.9） |
| GET | `/admin/v2/config-files/trash` | `namespaceId`（必填）、`keyword`、`page`/`pageSize` | 回收站分页列表：id、name、format、删除人 / 删除时间 |
| POST | `/admin/v2/config-files/{id}/restore` | — | 200 文件详情；名称已被未删除文件占用 `409 CONFIG_FILE_DUPLICATE`（§4.9） |
| POST | `/admin/v2/config-files/{id}/purge` | `reason`（必填） | 204；物理删除连带版本链，仅回收站内文件可执行，否则 `400 CONFIG_FILE_NOT_TRASHED`（§4.9） |
| GET | `/admin/v2/config-files/{id}/scopes` | — | 各贡献链：scopeLevel、scopeRefId、scopeName、headVersionNo、headHash、isRemoval、更新人 / 时间 |
| GET | `/admin/v2/config-files/{id}/versions` | `scopeLevel`、`scopeRefId`（必填对）、`page`/`pageSize` | 该链版本列表：versionNo、hash、isRemoval、basedOn、remark、创建人 / 时间 |
| GET | `/admin/v2/config-versions/{versionId}` | — | 版本详情含 `content`（脱敏） |
| POST | `/admin/v2/config-files/{id}/versions` | `scopeLevel`、`scopeRefId`、`content`、`remark?`、`basedOnVersionId`（链空传 null） | 201 `{versionId, versionNo, contentHash}`；失败码见 §4.2（400 语法 / schema / 无变化 / 占位符非法、409 并发冲突、422 超限） |
| POST | `/admin/v2/config-versions/{versionId}/rollback` | `remark?` | 201 新版本（§4.6）；schema 不再通过时 400 明示原因 |
| DELETE | `/admin/v2/config-files/{id}/scopes/{scopeLevel}/{scopeRefId}` | `reason`（必填） | 201 removal 版本；head 已撤销则 400 |
| POST | `/admin/v2/config-files/{id}/validate` | `scopeLevel`、`content` | `{valid, errors:[{path, message}]}`；不落库、不审计 |
| GET | `/admin/v2/config-files/{id}/effective` | 目标之一：`serverId` / `zoneId` / `regionId` / `bcClusterId`（都不传 = 仅 namespace 基线） | `effectiveContent`（脱敏）、`effectiveHash`、`provenance[]`、`deletedKeys[]`、`layers[]`（§4.3） |
| GET | `/admin/v2/config-files/{id}/diff` | `left`、`right`，取值 `version:<id>` / `scope:<level>:<refId>` / `effective:<targetType>:<targetId>` | 键级差异 `added/removed/changed` + unified 文本 diff（脱敏） |

错误码汇总：`CONFIG_FILE_DUPLICATE`、`CONFIG_FILE_NOT_TRASHED`、`CONFIG_SCOPE_MISMATCH`、`CONFIG_SYNTAX_INVALID`、`CONFIG_SCHEMA_VIOLATION`、`CONFIG_VERSION_CONFLICT`、`CONFIG_NO_CHANGE`、`CONFIG_CONTENT_TOO_LARGE`、`CONFIG_SENSITIVE_PLACEHOLDER_INVALID`。

审计事件：文件创建 / 元数据修改（含 schema、敏感路径变更）、移入回收站（`config.file.trash`）/ 恢复（`config.file.restore`）/ 彻底删除（`config.file.purge`）、新版本保存、回退、撤销层贡献——detail 均为键路径级摘要，无明文（§4.7）。

## 6. 与其他规格的边界

| 对方 | 关系 |
|---|---|
| `v2-zone-authority.md`（P3） | **依赖**：五层实体表结构、归属链（server→zone→region→bc_cluster→namespace）以其为权威；本域只按 id 引用并沿链解析，不复制、不缓存归属。 |
| `v2-namespace-isolation.md`（P3） | **依赖**：namespace 强隔离总则；本域落点见 §4.8。同时向其明确：`namespace_trust` 能力清单不含配置互通。 |
| `v2-delivery-orchestration.md`（P9） | **下游消费方**：配置下发 / 生效 / 灰度 / 生效观察 / 整单回滚**全部**归它。本域产出物 = 「已定稿的配置版本」（不可变 `config_layer_version.id`）与内部服务层解析能力：变更单创建时按 `(configFileId, 目标)` 调本域**明文**有效渲染（含 hash）生成载荷快照——明文只经进程内服务接口流向交付渲染，不经任何 HTTP 端点；变更单须自存载荷快照，不依赖本域版本长存（§4.9 purge 悬空）。「配置版本回退」作为整单回滚的一环时，也是由变更单调本域 rollback 生成新版本再重新编排生效。灰度接缝：上述进程内有效渲染接口须支持**按作用域的版本覆盖参数**（scope → versionId）——变更单以此实现「版本指派（pin）+ 末批确认正式切版」的灰度机制（pin 语义、生命周期与切版时机以 `v2-delivery-orchestration.md` §4.6.2 为权威，引用不复制）：灰度期间对活动单已生效目标渲染 / 解析时，被指派作用域按传入的 to_version 参与合并、其余按各链 head；pin 事实由彼域持有并提供查询，本域不存 pin、不感知变更单状态。 |
| `v2-file-assets.md`（P8） | **无依赖**：文件资产是 agent 磁盘现状的只读索引，配置中心是期望态权威；两者不互查。逐字节保真的托管需求归文件载荷（P9），不进配置中心（§4.1 保真度拍板）。 |
| FR-162（变更单模型） | **不归本域**：变更单的模型、影响预览、审批、撤回由 P9 规格定义；本域不出现变更单表、状态或占位字段。 |
| 审计设施 | 复用统一审计写入（P5 落地），本域只定义事件与 detail 脱敏规则。 |
| Legacy 通道 | `/beacon/v1`、`/admin/v1` 配置 / 文件树接口维护态冻结，本域不改不迁移；Legacy 数据不自动导入 V2（如需迁移另立 FR）。 |

## 7. 验收标准

对齐 PRD FR-160/161 验收摘要展开，全部可验证：

1. **作用域一致（FR-160）**：在五层任意组合上保存版本，`GET …/effective` 对具体 server 的合并结果严格按 namespace→bc_cluster→region→zone→server 低到高覆盖；未分配 zone 的 server 只受 namespace + server 两层影响（测试断言）。
2. **合并语义穷举（高风险区必测）**：标量覆盖 / map 深合并 / list 整替 / null 删键 / 删后高层重加 / properties 无删键，穷举单测全绿；同输入重复解析 hash 幂等。
3. **值来源解释（FR-160）**：有效预览中每个叶子键都能给出 `{层, scope 名, 版本号}`；构造「namespace 基线 + zone 改 a + server 加 b + server 删 c」时 provenance 与 deletedKeys 逐项正确；provenance 平行实现与主合并交叉测试恒一致。
4. **跨 namespace 不可串用（FR-160）**：向 A namespace 文件写入 B namespace 的 scope、或以 B 的 server 做有效解析，均被 `CONFIG_SCOPE_MISMATCH` 拒绝（集成测试）。
5. **结构化编辑与校验（FR-161）**：语法错误与 schema 违例在保存时被 400 阻断且不落库、错误含路径与原因；validate 端点与保存校验结果一致；required 只在 namespace 层强制。
6. **diff（FR-161）**：版本间、层间、目标间三种组合的键级 diff 正确（含敏感值脱敏后展示）。
7. **版本链与回退（FR-161）**：版本只增不改；rollback 生成内容等于历史版本的新版本且 `based_on` 正确；撤销层后该层不再参与合并、rollback 可恢复；并发保存旧基线返回 409。
8. **敏感值（FR-161）**：标记敏感路径后——版本详情 / 有效预览 / diff / 审计 detail 中该值均为 `__BEACON_MASKED__`；保存时占位符正确回填上一版明文（落库后 hash 与直接提交明文一致）；服务端日志全文搜索不到敏感明文（测试注入哨兵值验证）。
9. **回收站闭环（§4.9）**：删除文件后——常规列表不再出现、其 effective / 版本 / 保存端点一律 404、版本链数据保留；恢复后覆盖链与版本历史完整如初、可继续保存新版本；名称已被未删除文件占用时恢复返回 409；purge 仅对回收站内文件可执行（否则 400）、物理删除连带版本链且审计摘要（文件名、format、各链最终版本号与 hash）可追溯（集成测试）。
10. **页面接真（P7 阶段验收）**：`/configs` 从 mock 切真，文件管理、五层编辑、实时校验、有效预览（来源色块）、版本历史、diff、回退、回收站视图（删除 / 恢复 / 彻底删除）在真后端 + 真机浏览器闭环可用；1000+ server 目标选择不卡（服务端搜索分页）。
11. **边界守卫**：全代码库（本域范围）无 agent 面配置端点、无下发 / 灰度 / canary 字段；回退 / 撤销确认框明示「不影响线上，生效需走变更单」。
12. 受影响组件测试全绿：`go test ./...`、集成（真 MySQL）、`cd web && pnpm test` + build。

## 8. 风险 / 待定（默认决定集中登记，待拍板）

1. **schema 为文件级属性、无独立版本链**：修改 schema 只审计、不产生配置版本；历史版本不回溯校验，但回退时按当前 schema 重新校验（可能阻断回退）。若需要 schema 演进史，再立独立版本链。
2. **schema 部分校验模式**：层内容按「出现的键」校验，`required` 仅 namespace 层强制。风险：非基线层永远不触发 required 缺失告警；备选是在有效预览里对合并结果做完整校验并给非阻断告警（本版不做）。
3. **无服务端草稿态**：保存即定稿新版本，前端持稿；断电丢稿风险由前端 localStorage 暂存缓解（页面实现细节）。
4. **敏感值 write-only + 占位符回填**（已拍板 2026-07-07：维持 write-only）：占位符字面量定为 `__BEACON_MASKED__`；管理面永远读不回明文（连管理员也不能），忘记明文只能重新录入。若未来运维强烈要求「高权限 + 原因可查看明文」，须另立决策（会引入明文读端点与查看审计）。
5. **sensitive_paths 为精确键路径**：不支持通配 / 前缀匹配；list 内元素不可标敏感。需要时再扩展匹配语法。
6. **properties 无 null 删键**：与 Legacy 语义一致（值 `null` 是字符串）；properties 文件想撤销某键只能在低层删除或整层撤销。
7. **归一化落库**：保存与合并均 parse → 固定键序规范序列化，注释与原始排版不保留（结构化编辑器本就不产注释）；需要逐字节保真的文件不进配置中心（走 P9 文件载荷）。
8. **单版本内容上限 1 MiB**：超限拒绝。配置文件超过此量级通常已是数据文件，应走文件载荷。
9. **文件删除为回收站软删除**（已拍板 2026-07-07，默认「物理删除连带版本链」被否）：DELETE 只打软删标记、版本链保留，物理删除仅经回收站 purge（二次确认 + 原因必填），机制见 §4.9。「P9 变更单自存载荷快照」边界仍须成立（purge 后版本 id 悬空）。
10. **`namespace_trust` 不开放配置互通能力**：跨 namespace 复用配置只能各自维护。若未来出现强共享需求（如全局基线模板），须新 FR + 新决策，不在信任关系上打洞。
11. **有效 hash 采用 sha256（基座 §2）**：与 Legacy md5 不兼容属预期（V2 全新通道，无存量比对需求）。
12. **假想目标预览只到「层」粒度**：支持按 zone / region / bc_cluster / namespace 预览，不支持「假想一台不存在的 server」——server 层贡献本就以真实 server 为 scope_ref，无从假想。
