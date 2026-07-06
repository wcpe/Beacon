# 规格：交付编排 V2——变更单统一发布（第二版）

> 状态：草拟 · 关联 FR：FR-162, FR-165, FR-166, FR-167, FR-168, FR-171 · 阶段：P9（0.29.x）
>
> 需求真源 `docs/PRD.md`；页面职责 `docs/UX.md` §2「交付」大域。共享实体（namespace / region / zone / server / agent_identity）与全仓建表约定以 `v2-zone-authority.md` 为权威，本文引用不复制。

## 1. 背景与目标

Legacy 把「配置发布」「文件同步」做成了两套灰度编排，真实运维动作「换插件 jar + 改插件配置一起灰度」跨域无归属，会产生「新配置 + 旧 jar」半套状态。第二版把二者统一为**变更单**模型：

- 一次交付 = 一个变更单：黄金模板源文件差异 + 配置变更绑成一单，一起预览、一起审批、一起灰度、一起回滚。
- 灰度编排引擎只做一套，**载荷无关**：批次规划、推进门、熔断、暂停 / 终止对文件项与配置项一视同仁。
- 管到生效为止：批次 = 推送 → 触发生效（重启 / 热重载 / 仅推送）→ 观察窗 → 人工放行；「关了没起来」判生效失败并计入熔断。
- 控制面 / 数据面分离：命令通道只做编排，文件内容一律走流式 HTTP 数据面 + 控制面中转 blob 存储。
- 统一权限风险分级与贯通审计：一条变更单从创建到回滚可用 orderId 完整追溯。

## 2. 范围

### 做什么

- 变更单模型与三层状态机（单 / 批次 / 目标服），状态全落库、页面刷新可恢复。
- 黄金模板源差异生成：复用文件资产清单做差异扫描 → 选择文件集 + 关联配置版本 → 组单。
- 影响预览（目标服清单 + 逐服差异摘要 + 传输量估算）、审批、撤回、驳回。
- 交付数据面：模板源 agent 流式上传差异文件到控制面中转 blob 存储（sha256 寻址去重），目标 agent 流式拉取；断点、重试、校验；清理策略。
- 统一灰度编排引擎：全量 / 大区 / 小区 / 单服混合筛选，比例或数量批次规划，推进门 = 人工确认 + 自动熔断（失败率 / 健康恶化阈值），暂停 / 继续 / 紧急终止。
- 生效编排：restart / hot_reload / push_only 三种生效方式；重启 = agent 优雅关服 + 宿主自启拉起，超时未回归判失败；观察窗健康数据供推进门展示与熔断判定。
- 覆盖前 agent 本地备份（目录布局 + 保留策略）与整单回滚（文件备份还原 + 配置版本回退 + 重新生效）。
- 交付能力统一权限风险分级矩阵与全链路审计（FR-168，含配置 / 预览 / 变更单 / payload 查看）。
- `/changes`（发）与 `/changes/history`（溯）两页的后端契约。

### 明确不做（PRD §1.3 + 基座）

- 不建插件制品库 / 仓库、不做后台直传载荷：载荷只来自黄金模板源与配置中心。
- 不做自动依赖解析、蓝绿切换、虚拟合区。
- 不做跨 namespace 变更单：模板源与全部目标必须同 namespace，违者拒绝。
- 不用命令通道传文件内容；不把文件内容写入命令 payload、审计 detail 或配置对象。
- 不做源路径 → 目标路径映射：同相对路径覆盖。
- 不引入 Redis / MQ / DI 框架；编排推进由控制面进程内驱动 + 状态落 MySQL。

## 3. 数据模型

> 枚举一律 `VARCHAR` + 应用层校验；结构化字段落 `TEXT`（JSON 序列化）；时间 UTC；哈希 sha256 小写 hex。禁 MySQL 专有特性（可切 Postgres）。

### 3.1 `change_order` 变更单主表

| 字段 | 类型 | 说明 |
|---|---|---|
| id | 自增主键 | |
| namespace_id | BIGINT | 归属 namespace，索引；单内一切实体不得跨 namespace |
| title | VARCHAR(128) | 标题 |
| description | TEXT | 说明（可空） |
| source_server_id | VARCHAR(64) 可空 | 黄金模板源 serverId；纯配置变更单可空 |
| status | VARCHAR(32) | 单状态机（§4.1），索引 |
| pause_kind | VARCHAR(16) 可空 | `manual` / `circuit_break` / `prepare_failed` |
| pause_reason | VARCHAR(512) 可空 | 暂停 / 熔断原因（脱敏文案） |
| selector | TEXT | 目标筛选器 JSON 快照（§4.3.1） |
| batch_mode | VARCHAR(16) | `percent` / `count` |
| batch_sizes | VARCHAR(255) | JSON 数组，如 `[5,20,75]`（percent）或 `[1,10,50]`（count，剩余进末批） |
| activation_method | VARCHAR(16) | `restart` / `hot_reload` / `push_only`，单级配置、全批继承 |
| observe_window_sec | INT | 观察窗时长，默认 120 |
| activate_timeout_sec | INT | 生效超时，默认 300 |
| failure_rate_threshold_percent | INT | 批内失败率熔断阈值（1-100；0 = 关闭该熔断） |
| unhealthy_rate_threshold_percent | INT | 观察窗内批内 unhealthy 占比熔断阈值（1-100；0 = 关闭） |
| payload_state | VARCHAR(16) | `pending` / `uploading` / `ready` / `failed`，blob 就绪度 |
| diff_snapshot_at | DATETIME 可空 | 差异计算依据的文件资产快照时间 |
| created_by / submitted_at / approved_by / approved_at | | 生命周期记录 |
| reject_reason | VARCHAR(512) 可空 | 最近一次驳回原因 |
| started_at / finished_at | DATETIME 可空 | 执行起止 |
| cancel_reason | VARCHAR(512) 可空 | 紧急终止原因 |
| rollback_by / rollback_reason / rollback_at | 可空 | 整单回滚记录 |
| created_at / updated_at | | |

索引：`(namespace_id, status)`、`(status)`、`(created_by, created_at)`。

### 3.2 `change_order_item` 变更项（两种载荷）

| 字段 | 类型 | 说明 |
|---|---|---|
| id | 自增主键 | |
| order_id | BIGINT | 索引 |
| kind | VARCHAR(16) | `file_diff` / `config_change` |
| path | VARCHAR(512) 可空 | 文件项：服务器根内相对路径 |
| action | VARCHAR(16) 可空 | 文件项：`add` / `update` / `delete`（相对目标的语义在执行期按目标本地清单重判，见 §4.2.3） |
| sha256 | CHAR(64) 可空 | 文件项：模板源侧内容哈希（delete 项为空） |
| size_bytes | BIGINT 可空 | 文件项：字节数 |
| config_scope_kind | VARCHAR(16) 可空 | 配置项：作用域层级（五层之一，定义引用 `v2-config-center.md`） |
| config_scope_id | BIGINT 可空 | 配置项：作用域实体 id |
| config_from_version_id | BIGINT 可空 | 配置项：组单时该作用域当前生效版本快照（回滚锚点） |
| config_to_version_id | BIGINT 可空 | 配置项：要发布的目标版本 |
| created_at | | |

唯一约束：`(order_id, kind, path)`（文件项）、`(order_id, kind, config_scope_kind, config_scope_id)`（配置项，一个作用域在一单内只出现一次）。

### 3.3 `change_batch` 批次表

| 字段 | 类型 | 说明 |
|---|---|---|
| id | 自增主键 | |
| order_id | BIGINT | 索引 |
| batch_no | INT | 从 1 递增；唯一约束 `(order_id, batch_no)` |
| status | VARCHAR(32) | 批状态机（§4.1） |
| planned_count | INT | 批内目标数 |
| success_count / failed_count / skipped_count | INT | 终态计数 |
| started_at / observe_started_at / finished_at | DATETIME 可空 | |
| gate_confirmed_by / gate_confirmed_at | 可空 | 推进门人工确认记录 |
| break_reason | VARCHAR(512) 可空 | 熔断原因（含触发阈值与实测值） |

### 3.4 `change_target` 目标服表

| 字段 | 类型 | 说明 |
|---|---|---|
| id | 自增主键 | |
| order_id / batch_id | BIGINT | 均索引 |
| server_id | VARCHAR(64) | 唯一约束 `(order_id, server_id)` |
| status | VARCHAR(32) | 目标状态机（§4.1） |
| pushed_at / activated_at | DATETIME 可空 | |
| changed_file_count / skipped_file_count | INT | agent 回执的实际变更 / 跳过数 |
| backup_present | BOOL | agent 回执是否已生成本地备份（回滚预检依据） |
| error | VARCHAR(1024) 可空 | 失败原因（脱敏，展示到前端，ADR-0057） |
| rollback_status | VARCHAR(32) 可空 | `pending` / `running` / `rolled_back` / `failed`，独立于主状态 |
| rollback_error | VARCHAR(1024) 可空 | |
| updated_at | | |

### 3.5 `delivery_blob` 中转 blob 元数据

| 字段 | 类型 | 说明 |
|---|---|---|
| sha256 | CHAR(64) | 唯一索引（内容寻址主身份） |
| size_bytes | BIGINT | |
| state | VARCHAR(16) | `uploading` / `ready` |
| last_referenced_at | DATETIME | 最近被活动变更单引用时间，清理依据 |
| created_at | | |

磁盘布局：`<data-dir>/delivery/blobs/<sha256 前 2 位>/<sha256>`；上传先写 `<data-dir>/delivery/tmp/` 再原子 rename。同 sha256 天然去重：多个变更单、多个文件路径共享同一 blob。

## 4. 机制与状态机

### 4.1 三层状态机

#### 变更单

```text
draft → pending_approval → approved → rolling → completed
  ↑______↓（驳回/撤回/改单）   ↓            ↕ paused
         cancelled ←─────────┴──── cancelled（紧急终止）
completed / paused / cancelled ──rollback──→ rolling_back → rolled_back
```

| 从 | 到 | 触发 | 条件 |
|---|---|---|---|
| draft | pending_approval | 提交审批 | ≥1 个变更项；selector 解析出 ≥1 目标；模板源与全部目标同 namespace |
| pending_approval | draft | 审批人驳回（填原因）或创建人撤回 | |
| pending_approval | approved | 审批通过 | 审批权限；审批人 ≠ 创建人（§4.7） |
| approved | draft | 创建人撤回；或任何对 items / selector / 批次策略 / 生效策略的编辑 | **approved 后改单 = 审批自动作废回 draft**，重新走审批，作废动作入审计 |
| draft / pending_approval / approved | cancelled | 放弃整单 | 未执行过任何目标 |
| approved | rolling | 启动 | 执行权限 + 二次确认；目标快照固化落 `change_target`；**冲突守卫**：目标集与其他活动单（rolling / paused / rolling_back）的目标集有交集则拒绝启动 |
| rolling | paused | 人工暂停 / 自动熔断（§4.4.4）/ payload 准备失败 | pause_kind 区分三种来源 |
| paused | rolling | 继续 | 熔断暂停与准备失败需填原因 + 二次确认；mode 见 §4.4.5 |
| rolling | completed | 末批推进门人工确认 | 确认时执行配置正式切版（§4.6.2） |
| rolling / paused | cancelled | 紧急终止（填原因） | 未开始批 / 目标置 skipped；进行中推送尽力中止；已进入生效的目标不中断（避免半截覆盖） |
| completed / paused / cancelled | rolling_back | 整单回滚（填原因 + 二次确认） | ≥1 目标曾达 pushed / activated |
| rolling_back | rolled_back | 全部回滚目标终态成功；或人工「结束回滚」（残留 rollback_failed 目标保留记录并告警） | |

`draft` 单可删除（物理删除，入审计）。`rolled_back` 为终态；`completed` / `cancelled` 非严格终态（仍可回滚）。

#### 批次

```text
pending → running → observing → awaiting_confirm → completed
   ↓         ↓           ↓
skipped    failed（熔断） failed（熔断）
```

- `pending → running`：首批由启动触发；后续批由上一批推进门确认触发。
- `running`：批内目标并行推进；全部目标终态后——若熔断条件已触发 → `failed` 且单 `paused`；否则 → `observing`，记 `observe_started_at`。
- `observing → awaiting_confirm`：观察窗计时结束；窗内熔断检查持续，触发则 → `failed` 且单 `paused`。
- `awaiting_confirm → completed`：人工确认（**每批都要确认，含末批**；末批确认即单 `completed`）。
- 紧急终止时 `pending` 批置 `skipped`。

#### 目标服

```text
pending → pushing → pushed → activating → activated
   ↓         ↓                    ↓
skipped    failed               failed
```

- `pending → pushing`：批启动，控制面经命令通道下发 `delivery_push`（§4.5.1）。
- `pushing → pushed`：agent 完成清单拉取、覆盖前备份、blob 流式下载、落盘覆盖后回执成功。回执失败、命令超时、agent 离线 → `failed`。
- `pushed → activating`：控制面自动接续下发 `delivery_activate`。
- `activating → activated / failed`：按生效方式判定（§4.6.1）。
- 回滚推进用独立 `rollback_status` 字段，不复用主状态（一个目标同时保留「正推结果」与「回滚结果」两份事实）。

**恢复语义**：三层状态与全部计数均落库，编排推进由控制面后台 goroutine 驱动、不依赖浏览器会话；控制面重启后按库内状态恢复推进（rolling 单重新装载，pushing / activating 中的目标按命令超时与回执继续判定）。页面刷新 / 换人接手只需重新拉取详情或订阅 SSE。

### 4.2 模板源差异生成与组单

#### 4.2.1 组单流程

1. 创建 draft 单：选定 namespace、（可选）黄金模板源子服（须为已确认绑定、在线的 backend 子服）、扫描目录范围（服务器根内相对目录，如 `plugins/`）。
2. 差异扫描：控制面读取**文件资产最新快照**（`v2-file-assets.md` 权威，路径 / sha256 / 大小 / mtime），计算模板源清单 vs 目标集清单差异；快照时间落 `diff_snapshot_at` 并在前端明示。可触发「重扫」：向模板源 agent 下发资产重扫命令（文件资产域既有能力），完成后重算。
3. 选择文件集：运维在差异结果中勾选纳入本单的文件（默认全选差异项），落 `change_order_item(kind=file_diff)`。`delete` 项 = 模板源已删除而目标仍存在的文件，勾选后目标侧将删除（覆盖前备份保护）。
4. 关联配置版本（可选）：从配置中心 V2 选择一个或多个作用域的待发布版本，落 `change_order_item(kind=config_change)`，同时快照该作用域当前生效版本为 `config_from_version_id`。
5. 目标筛选 + 批次规划 + 生效策略（§4.3、§4.4、§4.6）→ 影响预览 → 提交审批。

纯配置变更单（无模板源、只有 config_change 项）与纯文件变更单（无配置项）均合法——引擎载荷无关。

#### 4.2.2 影响预览

`GET .../impact` 返回（分页）：

- 汇总：目标总数、批次划分预览（每批服务器数）、差异文件总数 / 总字节、预计传输字节（去重后）、配置作用域命中数。
- 逐目标：serverId、在线 / 健康状态、新增 / 覆盖 / 删除 / 相同跳过计数、该目标命中的配置作用域与 from→to 版本。
- 预览基于文件资产快照，明示快照时间与「以执行期实际哈希为准」的口径。

#### 4.2.3 执行期最终一致

组单差异基于快照，可能过期。执行期以目标本地实际状态为准：agent 收到清单后逐文件比对本地 sha256，相同则跳过（计入 `skipped_file_count`），不同或缺失才下载覆盖，目标本地已不存在的 `delete` 项跳过。控制面不因快照过期误传或误删。

### 4.3 目标筛选（载荷无关引擎·输入面）

#### 4.3.1 筛选器 JSON（落 `change_order.selector`）

```json
{
  "all": false,
  "regions": [3],
  "zones": [7, 8],
  "servers": ["lobby-1", "game-42"],
  "excludes": ["build-2"]
}
```

- 语义：`all=true` 取 namespace 内全部合格目标；否则取 regions ∪ zones ∪ servers 的并集；最后减去 excludes。
- 合格目标：`kind=backend` 子服 + 身份已确认绑定 + 已分配 zone + 未禁用。模板源自身自动排除。
- 解析仅在单 namespace 内进行；引用其他 namespace 的实体直接校验失败（FR-162 跨 namespace 拒绝）。
- 启动时固化：`approved → rolling` 时按 selector 重新解析并快照落 `change_target`，此后集群拓扑变化不影响本单目标集。

#### 4.3.2 目标在线性

启动或批启动时 agent 离线的目标不自动跳过：下发命令超时后判 `failed`（原因 = agent 离线），计入熔断失败率——离线面大本身就是不该继续放量的信号。

### 4.4 批次规划与推进门

#### 4.4.1 批次规划

- `percent` 模式：`batch_sizes=[5,20,75]` 表示按目标总数百分比切批，逐批向上取整、末批兜底全部剩余；百分比之和不足 100 时自动补一个「剩余」末批。
- `count` 模式：`batch_sizes=[1,10,50]` 表示逐批固定台数，剩余全部进末批。
- 批内成员按 serverId 字典序稳定排序后顺序切分（同输入必同输出，可复现）。
- 批次在启动时一次性生成落 `change_batch`，执行中不重划。

#### 4.4.2 payload 准备（启动前置）

启动动作触发后、首批下发前：

1. 控制面对全部 file_diff 项按 sha256 查 `delivery_blob`，得出缺失集。
2. 缺失集非空 → `payload_state=uploading`，向模板源 agent 下发 `delivery_upload` 命令（含缺失 sha256 清单摘要）；agent 逐文件流式上传（§4.5.2）。
3. 全部就绪 → `payload_state=ready`，首批开始；上传失败重试耗尽 → `payload_state=failed`，单自动 `paused`（pause_kind=prepare_failed），修复后可 resume 重试准备。

#### 4.4.3 推进门 = 人工确认 + 自动熔断

每一批的放行流程：批内全部目标终态 → 观察窗（`observe_window_sec`）→ `awaiting_confirm` → 人工确认（执行权限，前端展示批结果 + 观察窗健康数据）→ 下一批启动；末批确认即整单 `completed`。人工确认不可跳过、不可配置为自动。

#### 4.4.4 自动熔断（两条独立阈值，任一触发即熔断）

- **失败率**：批内 `failed / planned_count × 100 ≥ failure_rate_threshold_percent`。目标每次进入终态时即时评估，不等整批结束——提前熔断可中止尚未下发的批内目标。
- **健康恶化**：观察窗内（含 running 期已 activated 的目标），批内 activated 目标中健康等级为 `unhealthy` 的占比 `≥ unhealthy_rate_threshold_percent`。健康数据来自健康域内存快照（§6）。
- 熔断动作：当前批 `failed`（break_reason 记录触发阈值与实测值）、单 `paused`（pause_kind=circuit_break）、批内未下发目标置 `skipped`、后续批保持 `pending`。熔断为系统动作，入审计（actor=system）。

#### 4.4.5 暂停 / 继续 / 紧急终止

- **暂停**（人工）：不再下发新目标与新批；已在 pushing / activating 的目标继续走到终态（不制造半截覆盖）。
- **继续**：普通暂停直接恢复；熔断暂停 / 准备失败需填原因 + 二次确认，且带 mode：
  - `retry_failed`：熔断批回 `running`，重置批内 failed / skipped 目标为 pending 重推；
  - `skip_failed`：熔断批直接置 `completed`（保留 failed 记录），进入该批推进门等待确认。
- **紧急终止**：见 §4.1 单状态机 cancelled 行；已生效目标保持现状，事后可整单回滚恢复。

### 4.5 数据面（控制面中转 blob + 流式传输）

#### 4.5.1 命令通道只做编排

命令沿用第二版 agent 长轮询命令通道（ADR-0006 决策不变），payload 只含控制信息，**绝不含文件内容**：

| 命令 | 接收方 | payload 要点 |
|---|---|---|
| `delivery_upload` | 模板源 agent | orderId、待上传 sha256 数（清单经 agent 面 GET 拉取） |
| `delivery_push` | 目标 agent | orderId、manifest 摘要（文件数 / 总字节 / 清单哈希）；完整清单经 agent 面 GET 拉取 |
| `delivery_activate` | 目标 agent | orderId、activation_method、（restart 时）activate_timeout_sec |
| `delivery_rollback` | 目标 agent | orderId、回滚清单摘要 |

#### 4.5.2 流式上传（模板源 → 控制面）

1. agent 对每个待传文件先 `HEAD /beacon/v2/stream/delivery/blobs/{sha256}`：已存在（ready）则跳过——**sha256 寻址去重**，跨单、跨路径复用。
2. `PUT` 流式上传（`Content-Length` 必填）：控制面写临时文件、边收边算 sha256；完成后与路径声明比对，不符则丢弃并返回校验错误；一致则原子 rename 进 blob 目录、置 `ready`。
3. 上传失败整文件重试（不做分块断点），单文件重试上限 3 次；耗尽走 §4.4.2 准备失败路径。
4. 控制面全局上传并发上限默认 4 流（运维设置热更）；磁盘容量上限默认 20 GiB，超限拒绝新上传并报明确错误（不静默）。
5. agent 侧文件读取与 HTTP 走流式 IO、异步线程（TabooLib async），不整读入内存、不阻塞 MC 主线程。

#### 4.5.3 流式下载（控制面 → 目标）

1. agent 按清单逐文件 `GET /beacon/v2/stream/delivery/blobs/{sha256}`，支持 `Range` 断点续传：中断后从已收字节续拉。
2. 下载完成本地校验 sha256，不符删除重下；单文件重试上限 3 次，耗尽则该目标 `failed`（原因含文件路径与失败环节）。
3. 先全部下载到 agent 侧临时目录、校验齐全后才开始备份 + 覆盖（把「传输失败」与「覆盖失败」隔离，覆盖阶段不再依赖网络）。
4. 控制面全局下载并发上限默认 64 流（运维设置热更）；批次规模天然限流。

#### 4.5.4 blob 清理策略

- 后台清理器周期运行（默认每小时）：删除满足「所有引用它的变更单均已达终态或 cancelled 且超过保留期（默认 7 天）」且「不被任何活动单（rolling / paused / rolling_back）引用」的 blob（元数据 + 磁盘文件）。
- `uploading` 状态残留（上传中断）超 24h 的临时对象一并清除。
- 保留期与容量上限走运维设置热更；清理执行入审计（actor=system，含清理数量与释放字节）。

### 4.6 生效编排（FR-171）

#### 4.6.1 三种生效方式（单级配置，全批继承）

| 方式 | agent 动作 | activated 判据 | failed 判据 |
|---|---|---|---|
| `restart` | 回执「开始生效」→ 优雅关服（广播、save-all、shutdown）→ 依赖宿主自启脚本拉起 → agent 随进程重启后重新注册 / 心跳回归 | 控制面在 `activate_timeout_sec` 内观测到该 identity 心跳回归且状态 online（注册 / 健康真源 = Go 进程内存） | 超时未回归；或关服指令回执失败 |
| `hot_reload` | 文件项已在推送阶段落盘；配置项拉取并应用关联版本、触发 agent 配置热更回调；完成后回执 | agent 回执成功 | agent 回执失败；或 `activate_timeout_sec` 内无回执 |
| `push_only` | 无生效动作 | pushed 后立即置 activated（语义 = 随目标下次自然重启生效） | —— |

- **边界声明**：`hot_reload` 不保证 jar 被插件框架真正重载——Beacon 只负责落盘与配置热更；含 jar 替换的变更单应选 `restart`。前端在组单时对「含 .jar 文件项 + hot_reload」组合给出显式警告。
- 重启依赖宿主机自启脚本拉起进程；Beacon 不做进程管理。「关了没起来」即超时判失败，计入熔断（本设计的关键安全阀）。
- 批内生效整批并发（批大小即爆炸半径，由批次规划控制）；推送阶段受全局下载并发上限约束。

#### 4.6.2 配置变更的灰度生效与正式切版

配置作用域是层级共享的，灰度期间同一作用域需要在「批内已生效目标」与「其余服」呈现不同版本。机制：

1. **版本指派（pin）**：单进入 rolling 后，对每个 config_change 项建立「orderId × 目标集 → to_version」指派；配置中心解析某目标服有效配置时，若该服在某活动单的**已 activated 集合**内，则该作用域按 to_version 解析，否则按当前正式版本。指派事实由本域持有（查询接口见 §6），解析逻辑归配置域。
2. **正式切版**：末批推进门人工确认（单 → completed）时，控制面在同一事务内把各 config_change 作用域的当前生效版本正式切至 to_version 并清除 pin；此后新接入 / 不在目标集内的服也解析到新版本。
3. **未完成即回滚 / 终止**：清除 pin 即可，已 activated 目标经回滚流程重新生效回旧版本。

#### 4.6.3 观察窗数据

观察窗内控制面按 5s 粒度为批内目标聚合快照序列：健康分、健康等级、TPS、告警计数（数据源见 §6），内存保留当前批全窗数据供 `GET .../observe` 展示与熔断判定；批终态后仅保留末次汇总（落 break_reason / 批计数），不长期入库——历史指标查询走指标域自己的存储。

### 4.7 备份与整单回滚

#### 4.7.1 agent 本地备份

覆盖 / 删除任何文件前，agent 在自身数据目录生成本单备份：

```text
plugins/Beacon/delivery-backups/<orderId>/
  manifest.json          # 备份清单：[{path, action, sha256(旧), size}]；add 项记 path+action（回滚时删除该文件）
  files/<原相对路径>      # 被覆盖 / 删除文件的原内容，按原相对路径存放
```

- 备份成功后才开始覆盖；备份失败则该目标 `failed`，不动原文件。
- 保留策略：每服最多保留 5 个变更单备份且最长 30 天，超限按最旧清理（agent 本地周期执行）。
- 备份存在性随推送回执上报（`backup_present`），供回滚预检。

#### 4.7.2 整单回滚

- 触发：completed / paused / cancelled 单 + 回滚权限 + 原因 + 二次确认。
- 目标集 = 本单所有曾达 `pushed` 或 `activated` 的目标；**一次性全量下发，不走灰度批次**（事故场景求快）。
- 逐目标动作（`delivery_rollback` 命令编排）：
  1. 文件还原：按备份 manifest 恢复被覆盖 / 删除文件、删除 add 项文件；备份缺失（被保留策略清理）→ 该目标 `rollback_status=failed`（原因 = 备份不存在），预检阶段即在前端明示哪些目标不可文件回滚。
  2. 配置版本回退：控制面基于各 config_change 项的 `config_from_version_id` 在配置中心生成**新版本**（版本不可变链，引用 `v2-config-center.md` 回退原语）并正式切版 + 清除本单 pin。
  3. 重新生效：按本单 activation_method 对回滚目标再走一次生效判定（restart 的心跳回归 / 超时同 §4.6.1）。
- 回滚结果落 `rollback_status` / `rollback_error`；失败目标可重试；人工「结束回滚」允许在残留失败时收单（保留记录 + 告警）。全程入审计。

### 4.8 统一权限与审计（FR-168）

#### 4.8.1 风险分级矩阵（交付能力统一口径）

| 级别 | 操作 | 要求 |
|---|---|---|
| 低 | 变更单列表 / 详情 / 历史查看；文件资产清单查看；有效配置预览（非敏感值） | 登录 + 查看权限 |
| 中 | 文本文件内容预览与 diff；配置编辑保存草稿；创建 / 编辑变更单；影响预览；触发重扫 | 对应写权限；文件内容查看入审计（执行归文件资产域） |
| 高 | 审批 / 驳回；启动；批次放行确认；暂停后继续（熔断场景）；紧急终止；整单回滚；删除 draft 单；消息 payload 查看；敏感配置明文查看 | **权限 + 填写原因 + 二次确认**，全部入审计 |

- 权限能力位：`delivery.view` / `delivery.edit` / `delivery.approve` / `delivery.execute` / `delivery.rollback`，挂接管理面既有登录角色 / API 密钥机制（不重设计）。
- 职责分离：审批人不得是创建人（默认开启；单管理员小规模部署可在运维设置关闭，关闭动作本身入审计）。
- payload 查看、敏感路径文件、敏感配置的具体执行分别归消息域 / 文件资产域 / 配置域，本矩阵是统一分级口径（§6）。

#### 4.8.2 审计贯通

- 全生命周期动作写统一审计：`delivery.order.create / update / delete / submit / withdraw / approve / reject / start / pause / resume / batch_confirm / cancel / rollback / rollback_finish`，系统动作 `delivery.order.circuit_break / blob_cleanup`（actor=system）。
- 每条审计 detail 必含 `orderId`（批 / 目标级动作再含 batchNo / serverId）；`/audits` 按 orderId 过滤即得一条变更单从创建到回滚的完整链路。
- 审计 detail 不落文件内容、配置明文、blob 数据；错误文案经脱敏（ADR-0057）。

### 4.9 页面契约要点（/changes 发 · /changes/history 溯）

- `/changes`：变更单列表（服务端筛选：状态 / namespace / 创建人）+ 组单向导（模板源与差异 → 文件集与配置版本 → 目标与批次 → 影响预览 → 提审）+ 执行视图（批次进度、目标状态墙、观察窗健康数据、推进门确认 / 暂停 / 继续 / 终止操作）。实时进度走 SSE，断线回退轮询。
- `/changes/history`：终态与执行中单的历史检索（按单 / 批次 / 服务器三层下钻），整单回滚入口与回滚预检展示。
- 两页均满足 UX.md 全局契约：1000+ 目标分页 / 虚拟化、四形态（空态 / 常规 / 超大量 / 异常）、高风险操作二次确认 + 原因、错误脱敏展示。页面刷新后从 API 全量恢复状态（§4.1 恢复语义）。

## 5. API 契约

> 错误统一 `{code, message, traceId}`。agent 面鉴权：`X-Beacon-Token`（namespace 级）+ `X-Beacon-Identity`；管理面沿用登录令牌 / API 密钥。列表接口一律分页。

### 5.1 管理面 `/admin/v2/change-orders`

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/admin/v2/change-orders` | 创建 draft 单（namespace、模板源、扫描范围、初始策略） |
| GET | `/admin/v2/change-orders` | 列表：status / namespace / 创建人 / 时间过滤 + 分页 |
| GET | `/admin/v2/change-orders/{id}` | 详情：单 + items + 批次概要 + 计数 |
| PATCH | `/admin/v2/change-orders/{id}` | 编辑（draft；approved 编辑触发回 draft） |
| DELETE | `/admin/v2/change-orders/{id}` | 删除 draft 单（高风险：原因 + 二次确认） |
| POST | `/admin/v2/change-orders/{id}/diff-scan` | 触发模板源重扫并重算差异（返回任务态，完成后 diff_snapshot_at 更新） |
| GET | `/admin/v2/change-orders/{id}/impact` | 影响预览（汇总 + 逐目标分页，§4.2.2） |
| POST | `/admin/v2/change-orders/{id}/submit` | 提交审批 |
| POST | `/admin/v2/change-orders/{id}/withdraw` | 创建人撤回（pending_approval / approved → draft） |
| POST | `/admin/v2/change-orders/{id}/approve` | 审批通过（原因可选） |
| POST | `/admin/v2/change-orders/{id}/reject` | 驳回（原因必填）→ draft |
| POST | `/admin/v2/change-orders/{id}/start` | 启动（二次确认；含冲突守卫与 payload 准备） |
| POST | `/admin/v2/change-orders/{id}/pause` | 人工暂停 |
| POST | `/admin/v2/change-orders/{id}/resume` | 继续；body `{mode: "retry_failed"|"skip_failed", reason}`（熔断场景必填） |
| POST | `/admin/v2/change-orders/{id}/cancel` | 紧急终止（原因必填 + 二次确认） |
| POST | `/admin/v2/change-orders/{id}/batches/{batchNo}/confirm` | 推进门放行（末批确认即完成整单） |
| POST | `/admin/v2/change-orders/{id}/rollback` | 整单回滚（原因 + 二次确认）；重复调用对 failed 目标重试 |
| POST | `/admin/v2/change-orders/{id}/rollback/finish` | 残留失败时人工结束回滚 |
| GET | `/admin/v2/change-orders/{id}/targets` | 目标分页（batch / status / serverId 过滤） |
| GET | `/admin/v2/change-orders/{id}/observe` | 当前批观察窗数据（逐目标健康分 / 等级 / TPS / 告警序列） |
| GET | `/admin/v2/change-orders/{id}/events` | SSE 实时进度（单 / 批 / 目标状态变更事件） |

### 5.2 agent 面 `/beacon/v2/agent/delivery`（命令经既有长轮询通道下发，此处为配套拉取 / 回执接口）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/beacon/v2/agent/delivery/orders/{id}/upload-manifest` | 模板源拉取待上传 blob 清单（path / sha256 / size） |
| GET | `/beacon/v2/agent/delivery/orders/{id}/manifest` | 目标拉取本服专属差异清单（path / action / sha256 / size）与配置项摘要 |
| POST | `/beacon/v2/agent/delivery/orders/{id}/result` | 阶段回执：`{phase: "upload"|"push"|"activate"|"rollback", status, changedFileCount, skippedFileCount, backupPresent, error}`（restart 的 activate 由控制面按心跳回归判定，agent 只回执「开始生效」） |

### 5.3 流式数据面 `/beacon/v2/stream/delivery`

| 方法 | 路径 | 说明 |
|---|---|---|
| HEAD | `/beacon/v2/stream/delivery/blobs/{sha256}` | 存在性 / 就绪查询（去重与断点判断） |
| PUT | `/beacon/v2/stream/delivery/blobs/{sha256}` | 模板源流式上传；Content-Length 必填；服务端校验 sha256，不符 422 |
| GET | `/beacon/v2/stream/delivery/blobs/{sha256}` | 目标流式下载；支持 `Range` 断点续传 |

流式面同样走 agent 鉴权双 header，并校验请求 identity 属于持有该 blob 引用的活动变更单（模板源仅可上传、目标仅可下载本单清单内 sha256）。

## 6. 与其他规格的边界

| 事项 | 归属 | 本域姿态 |
|---|---|---|
| 配置作用域模型、版本不可变链、结构化编辑 / 校验 / diff、敏感值处理、「基于历史版本生成新版本」回退原语 | `v2-config-center.md` | 只引用版本 id 组单；配置域已在其 §6 承接两个接缝：① 有效渲染 / 解析接口接受按作用域的版本覆盖参数（本域以 pin 查询结果传入，pin 语义以本文 §4.6.2 为权威）；② 回退原语供整单回滚调用 |
| 文件清单扫描、哈希 / 大小 / mtime 快照、重扫命令、内容预览与敏感路径 | `v2-file-assets.md` | 消费最新快照做差异计算 + 触发重扫；不自建清单存储 |
| 健康分 / 等级 / TPS / 告警等观察窗数据、心跳与在线状态 | `v2-metrics-health-scheduling.md` 与注册健康真源（Go 进程内存） | 只读消费：熔断判定、观察窗展示、restart 回归判定 |
| namespace / region / zone / server / agent_identity 实体 | `v2-zone-authority.md` | 引用不复制；目标合格性依赖其状态 |
| namespace 强隔离与信任关系 | `v2-namespace-isolation.md` | 变更单不支持跨 namespace（信任关系也不放开此项） |
| agent 命令长轮询通道、身份绑定 | `v2-agent-identity.md` / ADR-0006 | 沿用通道，仅新增命令类型 |
| 消息 payload 查看的执行 | `v2-connection-message-storage.md` | 本域 §4.8.1 只定统一风险分级口径 |
| 管理面登录 / API 密钥机制 | 系统域既有机制 | 挂接能力位，不重设计 |

## 7. 验收标准

对齐 PRD §4 验收摘要并展开：

**FR-162 变更单模型**
1. 模板源文件差异与配置变更可绑成同一变更单；纯文件单、纯配置单亦可独立成单。
2. 提审前可查看影响预览：目标清单、逐服差异摘要、传输量估算、配置版本 from→to。
3. 审批、驳回、撤回、approved 后改单回 draft 全部生效且入审计；审批人 ≠ 创建人被强制（默认开启时）。
4. 模板源或任一目标跨 namespace 的组单 / 提审 / 启动被拒绝并报明确错误。

**FR-165 交付数据面**
5. 命令通道 payload 与审计 detail 中不出现文件内容；文件内容仅经流式面传输。
6. 同 sha256 文件跨单、跨路径只存一份 blob；目标本地同 hash 文件被跳过，仅传变更文件。
7. 大文件上传 / 下载全程流式不整读入内存；下载断点续传生效；sha256 校验失败自动重试、耗尽判 failed 且原因可见。
8. 覆盖前每目标生成本地备份（布局符合 §4.7.1）；备份失败则不覆盖、目标判 failed。
9. blob 保留期 / 容量上限生效：超限上传被拒且报错；到期 blob 被清理并留审计。

**FR-166 统一灰度编排引擎**
10. 全量 / 大区 / 小区 / 单服混合筛选 + excludes 解析正确，1000+ 目标可规划。
11. percent / count 两种批次规划正确切批、稳定可复现。
12. 每批生效 + 观察窗后进入 awaiting_confirm，人工确认后才下发下一批（含末批确认才 completed）。
13. 失败率、健康恶化任一超阈值自动熔断：当前批 failed、单 paused、原因含实测值；继续需原因 + 二次确认，retry_failed / skip_failed 两种恢复路径均可用。
14. 暂停不打断进行中目标；紧急终止后未开始批 / 目标为 skipped、单为 cancelled。
15. 批次与目标进度经 SSE 实时推送；断线后轮询可恢复。

**FR-171 生效编排**
16. restart：agent 优雅关服后由宿主拉起，心跳回归即 activated；`activate_timeout_sec` 内未回归判 failed 并计入熔断失败率。
17. hot_reload：配置热更回调触发且回执驱动状态；含 .jar + hot_reload 组合在组单时有显式警告。
18. push_only：推送落盘即 activated，无生效动作。
19. 观察窗展示批内逐目标健康分、等级、TPS、告警序列，与熔断判定同源。

**FR-167 历史与整单回滚**
20. 可按变更单 / 批次 / 服务器三层查看历史与状态。
21. 整单回滚 = 文件备份还原 + 配置版本回退（生成新版本）+ 按原生效方式重新生效；回滚结果逐目标可见、失败可重试。
22. 备份缺失的目标在回滚预检中明示且不静默跳过。
23. 控制面重启、页面刷新后执行中变更单状态完整恢复，编排继续推进。

**FR-168 统一权限与审计**
24. §4.8.1 矩阵内高风险操作缺权限 / 缺原因 / 未二次确认均被拒绝。
25. `/audits` 按 orderId 过滤可完整追溯一条变更单创建 → 编辑 → 审批 → 启动 → 各批放行 → 熔断 / 终止 → 回滚全链路，含系统动作。
26. 配置正式切版仅发生在末批确认事务内；灰度期间批内已生效目标与其余服解析到不同配置版本，回滚 / 终止后 pin 清除。

## 8. 风险 / 待定（默认决定待拍板）

| # | 事项 | 本文默认决定 | 备注 |
|---|---|---|---|
| 1 | 变更单目标是否含 proxy（BC 节点） | 仅 `kind=backend` 子服；BC 插件更新暂不入变更单 | **已拍板（2026-07-07）：不纳入，BC 手动维护。**放开需扩目标合格性与生效语义 |
| 2 | 生效方式配置粒度 | 单级配置、全批继承（PRD「批次可配置生效方式」按"批次执行时按单上配置的方式生效"解释） | 若确需批级覆盖，`change_batch` 加可空覆盖字段即可，契约预留但本版不做 |
| 3 | 整单回滚是否走灰度批次 | 不走，一次性全量下发（事故求快） | **已拍板（2026-07-07）：一次性全量回滚。** |
| 4 | 数据面参数默认值 | blob 保留期 7 天、磁盘上限 20 GiB、上传并发 4、下载并发 64、清理周期 1h，均走运维设置热更 | 需按真机带宽 / 磁盘实测校准 |
| 5 | agent 本地备份保留策略 | 每服 5 个变更单 / 30 天，agent 本地自清理 | 备份被清理后该服不可文件回滚（回滚预检明示） |
| 6 | 审批职责分离 | 审批人 ≠ 创建人默认开启，可在运维设置关闭（关闭入审计） | 单管理员部署的现实妥协 |
| 7 | 上传断点 | 上传失败整文件重试（不做分块协议）；下载用 HTTP Range 断点 | 单文件超大（>2 GiB）且链路差时上传体验退化，届时再议分块 |
| 8 | 推送时 agent 离线的目标 | 判 failed 计入熔断，不自动跳过 | 可选「离线自动 skip」开关属镀金，未做 |
| 9 | 配置灰度机制 | 「变更单版本指派（pin）+ 末批确认正式切版」；解析归配置域、pin 事实归本域 | 已收口：`v2-config-center.md` §6 已承接——其进程内有效渲染 / 解析接口接受按作用域的版本覆盖参数（本域以 pin 查询结果传入）；pin 生命周期与正式切版归本域权威 |
| 10 | 生效 / 观察默认时长 | `activate_timeout_sec=300`、`observe_window_sec=120` | 大型整合包重启可能超 300s，需可按单调整（字段已支持） |
| 11 | 批内生效并发 | 整批并发生效（批大小即爆炸半径），不做批内逐台滚动 | 首批建议设 1-5 台即可覆盖谨慎场景 |
| 12 | 熔断健康恶化口径 | 按「批内 activated 目标中 unhealthy 占比」，不做健康分下降斜率 | 简单可解释优先；斜率类指标待健康域数据成熟再议 |
| 13 | 同目标并发变更守卫 | 同一目标服同时只允许被一个活动单覆盖，启动时交集校验拒绝 | 拒绝而非排队，避免隐式队列复杂度 |
