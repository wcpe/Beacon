# 规格：热 / 冷数据生命周期与归档（第二版）

> 状态：草拟 · 关联 FR：FR-151, FR-152, FR-153 · 阶段：P6（0.26.x）

## 1. 背景与目标

连接明细、跨服消息、调度决策、审计、指标在 1000+ 子服规模下是持续膨胀的大数据量域（PRD §3「热 / 冷数据分层」）。本规格定义：

- 热库数据达到保留期后归档到同 MySQL 实例的独立 database `beacon_archive`（FR-151）。
- 冷查询默认只查热库，显式 `includeArchived` 后跨热 / 冷合并查询（FR-152）。
- 归档清理页面：预览、dry-run、执行、失败重试，全程审计，**清理前必须归档并校验通过**（FR-153）。

## 2. 范围

### 2.1 做什么

- 各数据域的默认保留期与运维设置热更（键名契约）。
- 归档库形态：同实例独立 database、同名表结构、预留独立归档 DSN 的切换语义。
- 归档器：分批搬运、断点续跑、幂等、行数 + 抽样哈希校验、校验通过后删热库。
- 冷查询路由机制与 `includeArchived` 参数契约（供各查询域采用）。
- 归档清理页面流程与管理面 API（`/admin/v2/archive/*`）。

### 2.2 明确不做什么

- 不引入 Redis / MQ / 分区表语法 / 分布式任务框架（基座 §1；大表分片一律日期后缀表名）。
- 不做归档库自身的二次清理：归档库数据默认永久保留，迁移 / 瘦身由运维离线处理（见 §8）。
- 不做跨实例自动数据迁移：切换归档 DSN 不自动搬历史数据（见 §4.2）。
- 不为归档单独建权限体系：沿用管理面登录令牌 / API 密钥；统一风险分级由 FR-168（P9）承接。
- 不改各数据域的表结构：表结构与日期后缀命名以属主规格为权威（§6）。

## 3. 数据模型

### 3.1 参与归档的数据域（域注册表）

`domain` 为 VARCHAR 枚举（应用层校验），是归档任务、保留期设置、审计的统一域标识。表结构与表名以**属主规格为权威**（下表已与属主对齐）：

| domain | 数据内容 | 表形态 | 属主规格 | 默认保留期（热库） |
|---|---|---|---|---|
| `metric_sample` | 指标批（5s 批聚合） | 日期后缀表（`metric_sample_YYYYMMDD`） | v2-metrics-health-scheduling.md §3.1 | 14 天 |
| `health_snapshot` | 健康周期快照 | 日期后缀表（`health_snapshot_YYYYMMDD`） | v2-metrics-health-scheduling.md §3.2 | 30 天 |
| `sched_decision` | 调度决策记录 | 日期后缀表（`sched_decision_YYYYMMDD`） | v2-metrics-health-scheduling.md §3.4 | 60 天 |
| `conn_detail` | 每连接明细 | 日期后缀表（`conn_detail_YYYYMMDD`） | v2-connection-message-storage.md §3.2 | 60 天 |
| `msg_trace` | 跨服消息元数据 | 日期后缀表（`msg_trace_YYYYMMDD`） | v2-connection-message-storage.md §3.3 | 60 天 |
| `msg_payload` | 跨服消息 payload | 日期后缀表（`msg_payload_YYYYMMDD`） | v2-connection-message-storage.md §3.4 | 30 天 |
| `audit` | 审计记录 | 单表（时间索引） | 现行 audit 域（无独立 v2 规格，见 §8） | 180 天 |

- 保留期语义：数据「业务发生时间」早于 `当日 UTC 0 点 - 保留期天数`（下称 cutoff）即到期，进入归档流程。
- 两种表形态对应两种归档单元（§4.3）：**日期后缀表**以整表为单元（仅归档日期严格早于 cutoff 的表，天然无半表状态）；**单表**以 `发生时间 < cutoff` 的行区间为单元、按主键分批。

### 3.2 归档任务表（落热库，控制面事实）

`archive_job`：

| 字段 | 类型 | 说明 |
|---|---|---|
| id | 自增主键（GORM 抽象） | |
| mode | VARCHAR | `dry_run` / `execute` |
| trigger | VARCHAR | `scheduled`（每日自动）/ `manual`（页面触发） |
| status | VARCHAR | `pending` / `running` / `succeeded` / `failed` / `cancelling` / `cancelled` |
| domains | TEXT | JSON 数组；本次任务包含的 domain，空数组=全部 |
| cutoffs | TEXT | JSON 对象；创建时按当时保留期快照的各域 cutoff（任务执行期间不随设置热更漂移） |
| operator | VARCHAR | 操作人；自动任务固定 `system` |
| error | TEXT | 失败原因（脱敏后可直接展示前端，ADR-0057） |
| started_at / finished_at | 时间（UTC） | |
| created_at | 时间（UTC） | |

索引：`(status)`、`(created_at)`。

`archive_job_item`（任务内以「域 × 表 / 区间」为粒度的工作项，也是断点续跑的检查点）：

| 字段 | 类型 | 说明 |
|---|---|---|
| id / job_id | 主键 / 外键 | |
| domain | VARCHAR | §3.1 枚举 |
| table_name | VARCHAR | 目标表名（日期后缀表为具体表名；单表为表名本身） |
| range_to | 时间（UTC，可空） | 单表形态的区间上界（= cutoff）；日期后缀表为空 |
| phase | VARCHAR | `pending` / `copying` / `verifying` / `deleting` / `done` / `failed` / `skipped` |
| cursor | VARCHAR | 已搬运的最大主键（断点续跑游标）；空=未开始 |
| rows_expected / rows_copied / rows_deleted | 整数 | dry_run 只填 rows_expected |
| verify_rows_hot / verify_rows_archive | 整数 | 行数校验双侧结果 |
| verify_sample_size / verify_sample_seed | 整数 | 抽样条数与随机种子（可复算） |
| verify_hash_hot / verify_hash_archive | VARCHAR | 抽样哈希（sha256 小写 hex，基座 §2） |
| verify_passed | 布尔 | 校验结论；仅 true 才允许进入 `deleting` |
| error | TEXT | 本项失败原因（脱敏） |

索引：`(job_id)`、`(job_id, phase)`。

### 3.3 保留期与运行参数（运维设置键，热更）

配置存储与热更机制复用运维设置域（`/settings`），本规格只定义键名契约与默认值：

| 设置键 | 默认值 | 说明 |
|---|---|---|
| `archive.retention-days.metric-sample` | 14 | 各域热库保留天数，下同（默认值以属主规格为准，见 §3.1） |
| `archive.retention-days.health-snapshot` | 30 | |
| `archive.retention-days.sched-decision` | 60 | |
| `archive.retention-days.conn-detail` | 60 | |
| `archive.retention-days.msg-trace` | 60 | |
| `archive.retention-days.msg-payload` | 30 | |
| `archive.retention-days.audit` | 180 | |
| `archive.auto-enabled` | true | 是否每日自动执行归档任务 |
| `archive.schedule-hour-utc` | 4 | 每日自动执行的 UTC 整点 |
| `archive.batch-rows` | 1000 | 单批搬运 / 删除行数 |
| `archive.batch-interval-ms` | 200 | 批间歇（限流，保护主库） |
| `archive.verify-sample-size` | 100 | 抽样校验条数上限 |
| `archive.cold-query-max-days` | 31 | 冷查询单次最大时间跨度 |

- 保留期下限守卫：任何 `retention-days.*` 不得小于 7（应用层校验），防误配当天删光。
- 热更生效点：设置变更即时生效于**下一次**任务创建与冷查询校验；运行中任务按其 `cutoffs` 快照执行，不受影响。
- 保留期变更走运维设置自身的审计，不重复记录。

### 3.4 归档库连接配置（启动配置，非热更）

落 Beacon 自身 `config.yml`（kebab-case，中文注释，遵循 config-files.md）：

```yaml
archive:
  # 独立归档库 DSN；留空 = 与主库同实例模式（复用主库连接参数、仅替换库名）
  dsn: ""
  # 同实例模式下的归档库名
  database: "beacon_archive"
```

## 4. 机制与状态机

### 4.1 归档库形态

- 默认形态：与热库同 MySQL 实例的独立 database `beacon_archive`；**表结构与热库同名同构**（含日期后缀表，表名一致），由归档器在写入前用归档连接执行 GORM 迁移建表。
- `beacon_archive` database 本身由部署时预建（`docs/OPERATIONS.md` 补部署说明），Beacon 不执行 `CREATE DATABASE`（方言差异大且非 GORM 抽象范围）。启动时做归档库连通性检查：不可达则 WARN 日志 + 归档能力降级为不可用（overview 展示 `reachable=false`），**不阻断控制面启动**。
- 归档任务表（`archive_job` / `archive_job_item`）落热库——它们是控制面事实，不随数据归档。

### 4.2 独立归档 DSN 的切换语义

- `archive.dsn` 留空：同实例模式，复用主库已解析的连接参数（主机 / 端口 / 用户 / 口令 / 连接参数），仅将库名替换为 `archive.database`。
- `archive.dsn` 非空：独立库模式，归档器写入与冷查询全部路由该 DSN；`archive.database` 忽略。
- 切换语义（同实例 → 独立库或反向）：
  1. Beacon **不自动迁移历史归档数据**；运维须先自行把 `beacon_archive` 数据搬到新目标（mysqldump / 物理迁移），再改配置重启。
  2. 重启后首次归档 / 冷查询前，归档器对新目标做连通校验 + 表结构迁移。
  3. 若未迁移历史数据即切换，冷查询只能查到新目标已有的数据——属运维决策，Beacon 不做一致性校验（overview 中展示当前目标与冷侧行数，供人工核对）。
- DSN 属启动配置（含凭据，走 env 覆盖，不入库 yaml 明文，遵循 config-files.md §5），改 DSN 需重启；只有 §3.3 的策略键热更。

### 4.3 归档器批处理流程

归档器是控制面进程内的单例后台工作器（goroutine + 定时器，无外部调度组件）：

- **单飞约束**：同一时刻至多一个 `running` 任务；创建请求遇 running 返回 409。每日 `schedule-hour-utc` 自动创建 `execute` 任务（`auto-enabled=true` 时；若彼时有 running 任务则本轮跳过并记 WARN 日志）。
- **任务展开**：任务进入 `running` 后，按 `cutoffs` 快照展开 items——日期后缀表形态：枚举热库中日期 < cutoff 日期的表，每表一个 item；单表形态：每域一个 item（`range_to = cutoff`）。无到期数据的域生成 `skipped` item（预览可见「无事可做」）。
- **单 item 流水线**：`copying → verifying → deleting → done`，dry_run 只统计 `rows_expected` 后直接 `done`：

  1. **copying（分批搬运）**：按主键升序、每批 `batch-rows` 行从热库读出，用归档连接写入同名表；写入使用 GORM `clauses.OnConflict{DoNothing}`（MySQL / Postgres 均可移植）保证**幂等**——重复搬运不产生重复行。每批归档侧事务提交成功后，才把该批最大主键写回 `archive_job_item.cursor`。批间 sleep `batch-interval-ms` 限流。
  2. **verifying（校验，删除的前置门）**：
     - 行数校验：热库侧（表 / 区间内）`COUNT(*)` 与归档侧 `COUNT(*)` 相等（到期数据的发生时间已成过去，区间内不会再有新写入）。
     - 抽样哈希：以记录的 `verify_sample_seed` 在已搬运主键范围内确定性均匀取样至多 `verify-sample-size` 行；两侧对同一批主键做规范序列化（列名字典序取值、行按主键升序拼接）后计算 sha256，比对一致。种子与哈希双侧值都落 item，可事后复算。
     - 任一不一致 → `verify_passed=false`，item 置 `failed`，**绝不删除热库数据**。
  3. **deleting（校验通过后删热库）**：日期后缀表整表 `DROP TABLE`（GORM Migrator，标准 SQL）；单表区间用「先按主键批量 SELECT 一批 id，再 `DELETE WHERE id IN (...)`」循环删除（**禁用 `DELETE ... LIMIT`，MySQL 专有**），同样批大小与限流。
- **两库无分布式事务**：跨连接不可能原子，靠幂等补偿——copy 幂等（OnConflict）、delete 幂等（按主键删）、cursor 单调推进，任意时刻崩溃重跑都收敛到一致终态。
- **失败处理**：item 任一步出错 → item `failed`（error 落库，脱敏），任务立即中止收尾为 `failed`（不再跑后续 item），保留全部 cursor。
- **断点续跑（重试）**：对 `failed` 任务发起 retry → 任务回到 `running`，`done`/`skipped` item 跳过，`failed`/未完成 item 从 `cursor` 处继续 copying（或从 verifying / deleting 阶段续起）。重试不新建任务，进度累计在原任务上。
- **取消**：`running → cancelling`，工作器在当前批次边界停止收尾为 `cancelled`；已搬运未删除的数据保留在两侧（幂等，下次任务自然续上）。`pending` 直接取消。

任务状态机：

```
pending → running → succeeded
                  → failed --(retry)--> running
running → cancelling → cancelled
pending → cancelled
```

### 4.4 冷查询路由（供各查询域采用的统一机制）

- **默认**：全部列表 / 明细查询只走热库连接，行为与无归档时完全一致。
- **显式包含归档**：查询端点接受 `includeArchived=true`（query 参数），语义与约束：
  1. 必须携带明确时间范围（`from` / `to`），且跨度 ≤ `archive.cold-query-max-days`；违反返回 400（错误文案明示边界，即「慢查询边界提示」的服务端部分）。
  2. 路由层对热连接与归档连接执行**同构查询**（同过滤、同 `ORDER BY 发生时间 DESC, id DESC`、同 limit），在应用层做有序归并取前 N；游标分页令牌编码 `(发生时间, id)` 边界，双侧统一应用。日期后缀表按时间范围在两侧各自枚举同名表。
  3. **主键去重**：归档进行中（已 copy 未 delete）的行两侧同时存在，归并时按主键去重（保留热侧）。
  4. 响应元信息带 `includeArchived: true`，前端在结果区明示「本次查询包含归档库，可能较慢」。
  5. 归档库不可达时**整体报错**（脱敏真因，ADR-0057），不静默返回仅热库的部分结果。
- namespace 强隔离对冷查询同样生效：路由层不绕过任何隔离过滤（v2-namespace-isolation.md）。
- 采用本参数的端点（连接明细、消息追踪、调度决策、审计、指标历史查询）在各属主规格的 API 契约中声明，语义以本节为准，不各自另立。

### 4.5 归档清理页面流程（挂 `/settings`「归档与清理」区块，P6 接真）

按 UX.md §4 写操作范式「影响预览 → 确认 → 进度可见 → 结果明示」：

1. **总览**：各域保留期、热库体量、到期待归档量、归档目标（同实例 / 独立库、可达性）、最近一次任务结果（overview 端点）。
2. **预览 / dry-run**：选择域（默认全部）→ 发起 `dry_run` 任务 → 展示逐 item 的 `rows_expected`（哪些表 / 区间、各多少行将被归档并删除）。dry-run 不写归档库、不删热库。
3. **执行**：高风险写操作——二次确认（明示「校验通过后将删除热库数据」）后创建 `execute` 任务；进度页 5 秒轮询任务详情，逐 item 展示 phase / 已搬运行数 / 校验结果。
4. **失败重试**：`failed` 任务展示脱敏失败原因 + 「重试」按钮（断点续跑）；`running` / `pending` 可取消。
5. **审计**：任务创建（含 dry-run）、执行完成 / 失败、重试、取消全部写审计（自动任务操作人 `system`）；保留期修改走运维设置审计。

页面四形态（UX.md §4）：空态（从未归档，引导先 dry-run）、加载骨架、错误可重试、超大数据量（item 列表分页）。

## 5. API 契约（管理面，`/admin/v2/archive/*`）

鉴权沿用管理面登录令牌 / API 密钥（基座 §2）；错误统一 `{code, message, traceId}`。

| 方法 | 路径 | 说明 | 请求要点 | 响应要点 |
|---|---|---|---|---|
| GET | `/admin/v2/archive/overview` | 归档总览 | — | `target`：{mode: `same-instance`/`external`、库名 / 脱敏 DSN、reachable}；`domains[]`：{domain、retentionDays、hotRows、archiveRows、expiredRows、lastJob 摘要} |
| POST | `/admin/v2/archive/jobs` | 创建任务（dry-run / 执行） | `{mode: "dry_run"|"execute", domains: []}`（空数组=全部） | 201 返回 job；已有 running 任务返回 409 |
| GET | `/admin/v2/archive/jobs` | 任务列表 | 分页 + `status` / `mode` / `trigger` 过滤 | `{items, total}` |
| GET | `/admin/v2/archive/jobs/{id}` | 任务详情 | — | job 全字段 + items[]（phase、行数、校验结果、cursor、error） |
| POST | `/admin/v2/archive/jobs/{id}/retry` | 失败任务断点续跑 | — | 200；仅 `failed` 可重试，否则 409 |
| POST | `/admin/v2/archive/jobs/{id}/cancel` | 取消任务 | — | 200；仅 `pending` / `running` 可取消，否则 409 |

- 保留期与运行参数的读写**不在本域**：走运维设置域既有端点（本规格只定义 §3.3 键名）。
- `includeArchived` / `from` / `to` 冷查询参数是**跨域参数契约**（§4.4），出现在各查询域自己的端点上，不在 `/archive/*` 下。
- overview 中 DSN 一律脱敏展示（去除 `user:pass@`，遵循 error-surfacing 规则的凭据边界）。

## 6. 与其他规格的边界

| 对方 | 边界 |
|---|---|
| v2-connection-message-storage.md（P5） | 权威定义 `conn_detail` / `msg_trace` / `msg_payload` 表结构与日期后缀命名；其查询端点采用 §4.4 的 `includeArchived` 契约；payload 冷查询同样受「权限 + 原因 + 审计」约束（该规格权威） |
| v2-metrics-health-scheduling.md（P4） | 权威定义指标批 / 健康快照 / 调度决策三张日表的表结构、命名与保留期默认值（其 §3.7）；其历史查询端点采用 §4.4 契约 |
| v2-namespace-isolation.md（P3） | 冷查询路由不绕过 namespace 隔离过滤 |
| 运维设置域（`/settings`，FR-158） | 承载 §3.3 设置键的存储、热更与审计；本规格不重复定义设置 API |
| v2-delivery-orchestration.md（P9，FR-168） | 统一权限风险分级落地后，归档执行 / 重试 / 取消纳入其高风险分级；本期先按管理面登录态 + 前端二次确认 |
| docs/UX.md | 归档清理挂 `/settings`「归档与清理」区块（UX.md §2 页面职责已对齐，见 §8-2） |

### GORM 跨 database 可移植写法要点（实现约束）

- **双连接**：热库、归档库各持独立 `*gorm.DB`；**禁止** `db.Table("beacon_archive.xxx")` 式跨库表名限定、跨库 JOIN、`INSERT INTO ... SELECT` 跨库搬运——搬运一律「热连接读、归档连接写」在应用层完成。
- 归档侧建表用归档连接跑 GORM 迁移（与热库同一套 model 定义），不手写方言 DDL。
- 幂等写入用 `clause.OnConflict{DoNothing: true}`（两方言均支持），不用 `INSERT IGNORE` / `REPLACE INTO`（MySQL 专有）。
- 批量删除用「SELECT 主键批 + `DELETE WHERE id IN (...)`」，不用 `DELETE ... LIMIT`（MySQL 专有）。
- 整表删除用 Migrator `DropTable`（标准 SQL）。

## 7. 验收标准

对齐 PRD FR-151/152/153 验收摘要并展开：

1. **FR-151 归档落位**：造出到期数据后执行归档，数据出现在 `beacon_archive` 同名表且热库对应数据被删除；任务重跑（模拟中途崩溃后 retry）不产生重复行、终态一致（幂等 + 断点续跑单测 / 集成测试）。
2. **FR-151 DSN 预留**：配置独立归档 DSN 后，归档写入与冷查询路由到该库；留空时同实例 `beacon_archive` 生效；归档库不可达时控制面正常启动、归档能力明示不可用。
3. **校验门**：构造两侧行数或抽样哈希不一致的场景，item 判 `failed` 且热库数据**未被删除**；校验通过路径 `verify_passed=true` 后才发生删除（必测的高风险区）。
4. **FR-152 冷查询**：默认查询不触碰归档库；`includeArchived=true` 且给定合法时间范围时返回跨热 / 冷有序合并结果、主键去重正确；缺时间范围或超 `cold-query-max-days` 返回 400 且文案明示边界；归档库不可达时报脱敏错误不静默。
5. **FR-153 页面闭环**：`/settings` 归档区块可完成 总览 → dry-run（rows_expected 正确、零写入零删除）→ 二次确认执行 → 进度轮询 → 失败重试 / 取消 全流程，真机验收。
6. **审计**：创建（含 dry-run）、完成、失败、重试、取消、每日自动任务均可在 `/audits` 查到；保留期修改在运维设置审计中可查。
7. **保留期热更**：改保留期后无需重启，下一次任务与冷查询边界按新值生效；低于 7 天被拒绝。
8. **不阻塞主线程**：归档搬运 / 删除全部在后台工作器分批执行，批间限流可配；管理面请求路径无长耗时操作（NFR）。
9. **可移植**：归档路径无 MySQL 专有 SQL（`golangci-lint` + 代码评审 + 集成测试在 MySQL 通过；模型与语句满足 §6 写法要点）。

## 8. 风险 / 待定（默认决定集中登记，待拍板）

1. **各域默认保留期**：统一基准 60 天（对齐 PRD「2 个月以上默认归档」），但 `msg_payload` 默认 30 天（体量大、时效价值低）、`audit` 默认 180 天（追溯价值高）、`metric_sample` 14 天 / `health_snapshot` 30 天（属主规格 v2-metrics-health-scheduling.md §8-2 的量级理由）——**已拍板（2026-07-07）：维持分层默认值**（均为运维设置项，上线后可调）。
2. **归档清理页面挂载位**：已收口——挂 `/settings`「归档与清理」区块，UX.md §2 的 `/settings` 页面职责已含「归档与清理」，无独立归档路由；若后续 mock 评审改为独立页面，按 ux-spec 门禁先回填 UX.md。
3. **不自动建库**：`beacon_archive` database 要求部署预建，Beacon 只建表不执行 `CREATE DATABASE`（方言差异）；需在 OPERATIONS 文档补部署步骤。
4. **DSN 非热更**：归档 DSN / 库名属启动配置，修改需重启；仅策略键（保留期 / 调度 / 批量参数）热更。
5. **归档库不可达时冷查询整体报错**：选择「明确失败」而非「静默返回部分热数据」，避免误判数据不存在。
6. **audit 域无 v2 权威规格**：审计表按现行 audit 表结构参与归档；若后续为审计立 v2 规格，域注册表随之指向新真源。
7. **归档库永久保留**：归档库自身的清理 / 迁出不在 P6 范围，由运维离线操作。
8. **单飞与自动任务撞车**：每日自动任务遇 running 任务跳过本轮（记 WARN），不排队——按每日一轮的节奏可接受。
9. **cutoff 不可覆写**：任务创建不提供自定义截止时间，一律按保留期计算，防误删近期数据；如运维确需提前归档某域，通过临时调小保留期实现（走设置审计）。
10. **冷查询最大跨度默认 31 天**（`archive.cold-query-max-days`），数值待运行数据回看后调整。
