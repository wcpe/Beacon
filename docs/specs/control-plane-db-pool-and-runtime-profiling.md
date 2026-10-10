# 功能规格：控制面连接池纵深防御与运行时取证（pprof）

> 状态：开发中　·　分支：fix/deadlock-single-connection　·　关联：生产 P0 死锁（2026-10-10 02:41:05）
> 配套：同批另有两组分别根治环的两边（审批事务不持连接等锁 / 推进器 tick 不持锁等连接）。
> 本文档覆盖四块：**纵深防御（§2）**、**连接等待预算（§2A，核心：把「无限挂起」变成「快速失败」）**、
> **锁内 DB 访问运行时守卫（§2B，防回归）**、**控制操作去锁（§2C）**与**取证能力（§3）**。
> §2 不根治锁序问题，只抬高成环门槛；**§2A 才直接掐断事故放大器**（等待无上限）；§2C 消除环的放大器。

## 1. 背景

### 1.1 生产事故（已确证）

sqlite + `max-open-conns: 1` 下，两条路径反序获取「DB 连接」与「编排内锁 `s.mu`」：

| 路径 | 资源获取顺序 |
|---|---|
| 审批批准执行 | 持连接（`db.Transaction`）→ 等 `s.mu` |
| 推进器 tick | 持 `s.mu` → 等连接（`ListActiveOrders`） |

池内仅一条连接时没有第三方可打破环，双方**永久互等**、不可自愈，只能重启恢复（现场：审批行停在 `executing`、`lease_until` 过期 20+ 分钟无人回收，所有需 DB 的端点永久挂起，进程 37 线程全 `futex_wait`、WAL 零推进）。

### 1.2 取证失灵

事故时尝试取 goroutine 转储，`GET /debug/pprof/goroutine` **返回 200**——但内容其实是内嵌前端的 `index.html`：`/debug/*` 从未注册，请求落到 `r.NotFound(h.Web.ServeHTTP)` 的 SPA history 回退。于是环图只能靠代码推理 + 测试复现，**无生产栈佐证**。

## 2. 方案 B：连接池纵深防御

### 2.1 目标与否证

**目标**：调整 sqlite 默认连接池，使「将来再出现同类锁序问题」时多一条连接可用于破环，避免永久挂起。

**必须明确（实测已证）：只调大连接池不能根治本环。** 池被 N 个外层事务占满时，N 个参与者仍可闭合成环（实验 exp10：池=2/4/8 全部永久互等）。加连接只是把「1 条即死」抬高成「需同时占满 N 条」，属**纵深防御**，不是修复。

### 2.2 实测（可复现，探测程序在 `apps/server/.tmp/sqlite-probe/`）

环境：`github.com/glebarez/sqlite v1.11.0`（内置 sqlite 3.41.2）、Linux amd64，一律走生产同款 `store.Open`（含 WAL）。

| 实验 | 内容 | 结果 |
|---|---|---|
| exp1 | 并发纯写事务（8 写者 × 10，首语句即写） | 池 1/2/4 均 **0 失败** |
| exp2 | 池被占满后新写请求 | 在**连接池排队**（1.2s+ 不返回），非 SQLITE_BUSY |
| exp3 | DEFERRED「先读后写」写锁升级 | B 的写**立即失败** `SQLITE_BUSY(517)`，`busy_timeout` 无效 |
| exp4 | 同上但 `_txlock=immediate` | 冲突前移到 BEGIN 处等待 |
| **exp6** | **池=4，8 并发 × 15 轮「先读后写」** | **DEFERRED 失败 101/120；immediate 失败 0/120** |
| exp7 | 互等环复刻（持连接等 mu / 持 mu 等连接） | **池=1 永久互等；池≥2 环被打破** |
| exp9 | 长只读事务（2s）期间写请求 | DEFERRED 4ms；immediate **1.95s**（等待而非失败） |
| **exp10** | **池被 N 个事务占满 + 环形取锁** | **池=2/4/8 全部永久互等（加连接不根治）** |

### 2.3 关键结论：调大池**必须**配套 `_txlock=immediate`

sqlite 默认 DEFERRED 事务在 BEGIN 时不取锁，第一条写语句才把读锁升级为写锁。**WAL 下该升级不可等待**：若另一事务已改过该页，升级立即返回 `SQLITE_BUSY(5)`/`BUSY_SNAPSHOT(517)`，`busy_timeout` 完全不生效（重试也无法让它看到更新快照）。

因此**只把 `max-open-conns` 从 1 调到 4 会让控制面从「永久挂起」变成「频繁写失败」**——控制面大量服务方法是「事务内先查后改」，exp6 实测失败率 84%。故 `store` 层自动为 sqlite DSN 注入 `_txlock=immediate`（已显式指定 `_txlock` 的 DSN 尊重用户配置）。

**代价**（exp9 实测，明文记录）：immediate 让每个事务在 BEGIN 处即取写者位，故只读事务不再互相并行——长只读事务（2s）期间普通写从 4ms 变 1.95s（等待而非失败）。控制面只读事务普遍短小（审计导出走游标分批、非单事务），该代价可接受。

### 2.4 默认值

| 项 | 原 | 新 | 依据 |
|---|---|---|---|
| `max-open-conns` | 1 | **4**（与内置默认一致） | 打破池=1 的死局；4 在 sqlite 单写者下不引入额外争用（exp1 实测 0 失败），且与 `config.Default()` 既有值对齐，消除「样例与内置默认不一致」这一真源分裂 |
| `max-idle-conns` | 1 | **2** | 够用即可，避免空闲连接长期占用（与内置默认一致） |

注：`config.Default()` 早已是 `MaxOpenConns: 4`，但**样例 YAML 写的是 1**，而样例才是首启释放为生产 `config.yml` 的模板（`EnsureConfigFile`）——即**生产实际拿到的是 1**。两者不一致本身就是缺陷，本次一并收口。

### 2.5 范围外

- 不改交付编排域代码（锁序根治属另两组）。
- 不改生产 `config.yml`（运维真源，仅提示需重启生效）。

## 2A. 连接等待预算：把「无限挂起」变成「快速失败」

> 同批加固的核心交付。方案 B 只抬高成环门槛（§2.1 已证「加连接不根治」），本节才是**直接掐断放大器**：
> 生产事故里 25 分钟不可自愈的根因不是「池会被占满」（那难避免），而是**等待没有上限**。

### 2A.1 认知订正：标准库本就支持超时，缺的是调用方给 ctx

事故记录曾断言「等待发生在 Go 层连接池排队，**不读 ctx、无超时**」。**该断言不成立**：
`database/sql` 的 `db.conn(ctx)` 在池满进等待队列时明确 `select { case <-ctx.Done(): return nil, ctx.Err() }`，
带 deadline 的 `WithContext` 实测约 600ms 准时返回。
真正的放大器是**本仓主力写法 `db.Transaction(func(tx *gorm.DB) error {...})` 不传 ctx**（gorm 默认
`context.Background()`，永不取消）——标准库给了能力，是调用侧没给它能超时的 ctx。

订正的意义：加固方向从「做不到」变成「一处改动覆盖全部调用点」。

### 2A.2 方案：ConnPool 包装层（而非逐点改 ctx）

在 `store` 层包一层 `gorm.ConnPool`，为**所有**经 gorm 的语句与事务补预算，调用点零改动。

**为什么不用 gorm 内建旋钮**（实测对照）：

| 旋钮 | 覆盖 | 缺口 |
|---|---|---|
| `DefaultTransactionTimeout` | 仅事务 | `db.First` 裸查询仍无限挂起 |
| `DefaultContextTimeout` | 仅 Execute 注入 ctx | `Begin` 取连接的 ctx 路径不命中，`db.Transaction` 仍挂起 |
| **ConnPool 包装层（本方案）** | **两者都覆盖** | —— |

**必须两阶段（勿退化为一阶段）**：`database/sql` 会把事务的 ctx 绑到**事务寿命**上
（`beginDC` 内 `go tx.awaitDone()`，ctx.Done 一到立即回滚；`Rows.awaitDone(ctx)` 同理）。
若用同一个短预算既管「取连接」又管「事务寿命」，任何活过该预算的正常事务都会被中途掐断——
本仓最长事务是文件批量导入 2000 文件（实测 8.5s）。故拆成两个独立预算：

- **取连接**（`call-timeout-ms`，默认 5s）：此阶段只占 `sql.Conn`，而 `Conn` 拿到后不再看 ctx
  （`grabConn` 忽略 ctx），故超时可安全取消，只需自行归还连接。
- **事务寿命**（`tx-timeout-ms`，默认 60s）：作为被 `awaitDone` 监听的那条 ctx，仅在拿到连接后生效。

实测两阶段**不损失性能**：300 次事务、池上限 4 —— 单阶段 743ms / 两阶段 581ms（更快，因
`db.Conn(ctx).BeginTx(ctx)` 复用了刚占用的那条连接）；池上限 1 且并发 8 时无连接流失。
池被占满时：裸查询约 700ms、事务约 701ms 准时失败。

### 2A.3 装配点（易错，已实测对照）

包装层必须经 **dialector 的 `Conn` 字段**注入（`mysql.New(mysql.Config{DSN, Conn})` /
`&sqlite.Dialector{DSN, Conn}`），**不能**在 `gorm.Open` 之后赋 `db.ConnPool`：
`gorm.Open` 末尾会把 `db.ConnPool` 固化进 `db.Statement.ConnPool`，而后续 `getInstance()`
取的是**语句级**那个字段——事后只赋 `db.ConnPool` 会导致业务语句仍走原池
（实测包装层命中 **0 次**，防护形同虚设且不留任何迹象）。经 dialector 注入则由 gorm 自己
写入两个字段，全路径（含 `Session` 派生、嵌套 savepoint、`AutoMigrate`）都被覆盖。

### 2A.4 引导期单独放宽（防「加防护反引入新故障」）

`call-timeout-ms` 是给**在线业务语句**定值的（秒级），但它同样约束**单条语句执行**。
`AutoMigrate` 在 MySQL 大表上是一条 `ALTER TABLE`——实测单条语句预算是硬约束、到点即被 ctx
掐断且无法续跑。若沿用秒级预算，升级时一次慢 DDL 就会让控制面**再也起不来**（AutoMigrate 失败即
fail-fast）。故启动期建表 / 存量回填走独立的 `bootstrapTimeout`（30 分钟）：够覆盖大表 DDL，
又仍是有界值。

### 2A.5 预算取值

| 项 | 默认 | 依据 |
|---|---|---|
| `call-timeout-ms` | **5000** | 业务事务实测中位 1.482ms / p95 3.817ms / 最大 11.301ms，5s 有两个数量级余量；真耗尽时必是池枯竭或长事务占位 |
| `tx-timeout-ms` | **60000** | 最长事务（导入 2000 文件）实测 8.5s，留足 7 倍余量；仍是有限值，卡死也在 60s 内释放连接 |

校验：`call-timeout-ms` 须小于 `tx-timeout-ms`（取反会把可诊断的配置错误伪装成连接池故障）。
错误对外为 503 + 业务码 `DB_WAIT_TIMEOUT` + 可读中文原因（「等 DB 连接超时（可能是连接池耗尽或有
长事务占用）」），便于运维直接定位方向。**不覆盖调用方已有 deadline**（调用方自带期限到期按原语义
透传，不报成池问题）。

## 2B. 锁内 DB 访问运行时守卫（防回归）

> §2A 负责「挂起有上限」，本节负责「同类写法不再悄悄回来」。规范侧早有明文
> （`.claude/rules/testing-and-quality.md` §3「DB IO 一律在锁外」），缺的是机器强制。

机制：在 GORM 六个处理器（`Query`/`Create`/`Update`/`Delete`/`Row`/`Raw`）各挂 `Before("*")` 锚点。
该锚点**先于取连接触发**（实测），故捕获的是「DB 访问**意图**」——既覆盖拿到连接后的 SQL，
也覆盖事故里最危险的那一态**「已持锁、正排在连接池队列上」**（还没拿到连接、SQL 还没执行）。

**关键设计决定——按持有者 goroutine id 精确归属**：常见做法是对目标锁 `TryLock`，失败即认定
「当前 goroutine 持锁」。但 `sync.Mutex` 不可重入，**TryLock 失败只说明锁被「某人」持有，不能说明
被「自己」持有**——于是「A 持锁做非 DB 工作、B（另一 goroutine）做 DB 访问」会被误报，而并发正是
本仓常态（审批 worker / 推进器 / HTTP 请求并存）。本方案记录持有者 goroutine id 并精确比对，
从根上消除该类误报，也不再需要在观测时短暂改动锁状态。

**成本控制**：判定入口先读一个全局「受观测锁被持有数」（1 次原子读，实测约 6ns）；
为 0 时直接返回，**根本不取 goroutine id**（取 id 实测约 5.8µs，不可放进热路径）。

**处置分两档**（与生产 panic 的取舍）：测试与 CI 用 `t.Fatalf`（fail-fast 的正确落点）；
生产**只记 ERROR 日志、不 panic**——事故教训正是「`healthz`/`metrics` 不查库掩盖了故障范围」，
若守卫 panic 会把「部分端点慢」升级为「进程崩溃」，对控制面可用性是净损失
（`.claude/rules/architecture-invariants.md` §3）。生产开关为运维设置项
`debug.lock-db-guard-enabled`（**默认关**，诊断窗口热开），照 `log.level` 的做法即时生效。

## 2C. 控制操作（Pause / Cancel）去锁

交付域 `Pause` / `Cancel` 是**仍持 `s.mu` 做 DB** 的 HTTP 直连入口
（`POST /change-orders/{id}/pause` / `.../cancel`）。它们是环的**放大器**：只要有持锁方在等连接，
这些端点会一起挂起。现按既有 `*InTx` 口径去掉 `s.mu`，互斥改由 CAS 前态承担：

- 主迁移 `UpdateStatusCAS(rolling|paused → …)` 带前态条件，并发下至多一次命中，另一个必得 409；
- `Cancel` 的收口更新（在途目标→failed、pending 批/目标→skipped）都是 `WHERE status IN (前态)`
  的条件批量更新，**幂等**——重复执行不会二次改写已终态行，故「紧急终止」语义不被削弱；
- 前置读单只用于给出可读拒绝原因，判定权在 CAS 的 `WHERE` 上，不需要 check-then-act 原子性；
- `releaseTerminalMemory`（清 `observeByOrder` / `stallByOrder`）走独立叶子锁，不依赖 `mu`。

## 3. 方案 D：运行时取证能力（pprof）

### 3.1 注册方案

在 `router.go` 中、`r.NotFound(h.Web.ServeHTTP)` **之前**注册 `/debug/pprof/*`（chi 具名路由优先，故不再被 SPA 回退吞掉）：

- 索引 `/debug/pprof/`、`cmdline`、`profile`、`symbol`、`trace`，以及 `goroutine`/`heap`/`allocs`/`block`/`mutex`/`threadcreate` 六个 profile 全量显式注册为 **GET**；
- 无尾斜杠 `/debug/pprof` 301 重定向到标准前缀（`go tool pprof` 与手工 curl 都可能省斜杠）。

刻意沿用标准库前缀：换前缀会让 `go tool pprof`、`go tool trace` 及现成排障文档全部失效。

### 3.2 安全边界论证

生产控制面，`SECURITY.md` 明确「建议不要把管理端口暴露到公网」。pprof **不裸暴露**，复用管理面既有鉴权链：

1. `adminAuthMiddleware`（登录令牌 / API 密钥）——缺 / 错 / 过期一律 401；
2. 叠加 `requireFullRole` —— 挡掉 readonly 角色与只读密钥。

理由：pprof 暴露进程内部态，**堆转储可能含内存中的配置明文与凭据**；且 CPU profile / trace 属「方法为 GET 但有真实副作用」（长时间占用 CPU、heap 取样会触发 GC），与仓库既有归类（`requireFullRole` 专用于「GET 但有写副作用」端点）一致。故**不放任**匿名访问，也不下放到只读角色。

未采用的备选：仅本机监听 / 独立端口——两者都要新增监听地址或端口，与「单二进制同端口」（ADR-0002）冲突，且事故时往往只能远程排障；仅 debug 开关启用——会在最需要它的时候（生产未开开关）恰好不可用。

### 3.3 取证用法

见 `docs/OPERATIONS.md` §6「排障」。

## 4. 独立评审后的收口（2026-10-10）

第一轮交付后由独立评审复检出三处 P1（§4.1~§4.3），复验中又发现并修掉一处更严重的归因错误（§4.4）、
清掉评审的 P2 清单（§4.5），以及两处阻断门禁的既有缺陷（§4.6）。
本节记录其处置与**为什么这样处置**（结论比过程重要）。

### 4.1 P1-1：健康探测没有预算

**问题**：包装层只有 `Ping()` 直通（无预算），而运行期调用方拿到的恰是**原生池**——
`main.go` 的 `db.DB()` → `GetDBConn()` → `p.inner` 注入给 `SystemService`；
归档侧 `archive_service.go` 也是 `s.archiveDB.DB()` 后 `sqlDB.Ping()`。
`(*sql.DB).Ping()` 内部走 `context.Background()`，池耗尽时**无界等待**。

**影响**：事故里「所有需 DB 的端点挂起」的症状会在 `/api/system/status` 与归档可达性探测上原样复现
——而这两条恰恰是排障时最需要的（§2A 的预算对它们完全不生效）。

**处置**：`store` 新增 `BoundedPinger` 接口 + `NewBoundedPinger(*gorm.DB)`：经类型断言取包装层
（继承 `call-timeout-ms` 预算）；拿不到时回退原生池的 `PingContext`（无本层兜底期限，但调用方 ctx 仍可中断）。
`service.dbPinger` 接口改为 `PingContext(ctx)`，`SystemService.Status()` 另给一层调用侧上限
（`systemDBPingBudget`，5s），使「本端点永不挂起」与注入的是哪种实现无关。
归档 `reachable()` 同样改为带预算探测。

**为什么保留无预算的 `Ping()`**：`gorm.Open` 末尾的**启动期自动 Ping** 只认 `Ping()`，
且启动期恰恰不能套秒级预算（慢启动 MySQL 首连、冷启 DNS 解析都可能超 5s，套上去就把「能起来」变成「起不来」，
与 §2A.4 的 bootstrapTimeout 同一口径）。故启动期保持无界、运行期走 `PingContext`。

### 4.2 P1-2：归档长查询被 call 预算误杀

**问题**：预算 ctx 直接下传给 `QueryContext/ExecContext`，而 `database/sql` 用它**同时**管
「等连接」「语句执行」「`Rows` 生存期」。`call-timeout-ms`（5s）是按**在线业务语句**定值的
（实测中位 1.482ms），但归档的存在理由就是大表，其验证步有按设计无 LIMIT 的全区间查询：
`countRows` 的 `COUNT(*)`、`orderedPKs` 的全量主键 `Pluck`、`hashRows` 的 `IN ?` 大集合、
`applyRange` 覆盖的「全部未归档历史」。

**影响**：防护上线后这些查询在 5s 处被掐断 → `runVerify` 失败 → item 判 failed → 归档任务失败。
**表越大越必然失败**，等于把归档从「慢」变成「不可用」。

**处置**：归档 runner 的 `hot` / `archive` 两库在**整单范围**内绑定 `archiveLongQueryTimeout`（30 分钟），
由 `bindLongBudget` 完成、任务收尾统一取消。取 30 分钟与 `store.bootstrapTimeout` 同源同理由
（都是「一条语句扫/改整表」这一类），仍**有界**——§2A 的全部价值就是「无限挂起变快速失败」，
无界等待会让一条病态语句永久占住连接。基线用 `context.Background()` 而非 worker 的关停 ctx：
归档单在关停时**刻意保持 running** 以便下次续跑，挂在关停 ctx 上会让一次重启掐断跑了一半的整单。

**为什么不用「分块 + 游标」这个备选**：`orderedPKs` 的全集是 `pickArchiveSample` 在**主键升序全集**上
确定性取样的输入，分块会改变取样集合、进而改变校验语义（同一任务重跑可能取到不同样本）；
`countRows` 的相等性判据本就是「整区间行数」，分块相加还要处理搬运期间的并发增删。
属校验设计重写而非防护层修补，风险收益不划算。

**为什么能"自带期限"就绕过本层预算**：`startBudget` 的判据是「ctx 无 deadline 则补 default」，
故**调用方自带期限时本层原样尊重**。这条约束本身必要（审批 / 导出路径的既有失败时机不能被改），
同时也正是归档表达「我是长查询」的合法通道——不必改包装层、不动其语义。

### 4.3 P1-3：归档服务仍持锁做 DB

**问题**：`archive_service.go` 三处持 `s.mu` 做 DB——`CreateJob` 的 `HasActiveJob`、
`RetryJob` / `CancelJob` 的 `GetJob`，以及前者锁内的 `hotDB.Transaction`。
该锁当时是普通 `sync.Mutex`，不受守卫观测；而 §2B 的省略理由写的是「其余锁持有期间不做 DB 访问」，
该断言对本服务不成立 → 「系统性防护」的说法在第二处服务上落空。

**处置（取评审给出的 B 路径）**：`mu` 改 `lockguard.Mutex` 并新增
`ArchiveService.AttachLockDBGuard()`，在 `main.go` 补挂第二份守卫；同时订正
`delivery_orchestrator.go` 的省略理由，明确观测面**仅覆盖本编排器**、归档那三处由第二份装配保证。

**为什么**不取 A 路径（DB 移出锁外 + CAS 定序）**——A 的前提在本处不成立：
单飞判据（「至多一个活跃任务」）目前只由 `HasActiveJob` 这个 check-then-act 维护，
表上**没有**能让数据库定序的唯一约束（活跃集是 `status IN (pending,running,cancelling)`
这一**条件集合**，sqlite 与 mysql 的可移植写法都表达不出部分唯一索引）。
若按 A 改，两个并发 `CreateJob` 会双双读到「无活跃任务」并各自插入一条——
把「至多一个」降级成「通常一个」。这是**语义削弱而非加固**。

**它为何不构成环（与 P0 的区别）**：本服务三处锁内 DB 都是**同 goroutine 顺序调用**
（取锁 → 查/写 → 放锁），全程不跨调用等另一把锁；而归档 worker 的搬运循环
（`runJob` / `runCopy` / `runVerify` / `runDelete`）**从不取本锁**，故不存在「持连接 → 等 mu」。
P0 事故的环是「审批持连接等 mu」×「tick 持 mu 等连接」，本服务缺了前一半——属**潜伏风险**而非现症。
纳入观测后，一旦真有对偶路径出现即被指认。

### 4.4 顺带修掉的一处更严重的归因错误（评审未提，实测发现）

**问题**：`wrapTimeout` 原先只看「本层预算是否到点」（`ours()`），不校验底层错误是否真由 ctx 期限驱动。
实测：**预算 300ms、`busy_timeout` 5000ms** 时，一次纯 sqlite **写锁竞争**
（另一未提交事务持写锁）耗时 **5.01s** 才返回 `SQLITE_BUSY`——此刻派生 ctx 早已过期、`ours()` 为真，
于是这条与连接池毫无关系的失败被贴上了
「等 DB 连接超时（可能是连接池耗尽或有长事务占用）」。对运维是方向性误导（该去看谁在长写，
而不是去调 `max-open-conns`）。

**处置**：`wrapTimeout` 增加 `errors.Is(err, context.DeadlineExceeded)` 判据——
只有真正由 ctx 期限驱动的失败才被认领。池排队那一路（`db.conn(ctx)` 返回 `ctx.Err()`）
仍是 `DeadlineExceeded`，故覆盖不受损。修复后同一场景返回裸 `SQLITE_BUSY`。

**取值建议**：`busy_timeout` 应**显著小于** `call-timeout-ms`，让「等文件锁」总能在本层预算之前自行了结，
两条超时语义各归其位。默认 DSN 的 `busy_timeout(5000)` 与默认预算 5000ms 恰好同值，
是该竞态的临界形态（归因侧已由上述判据兜住，但取值本身仍应在部署时拉开）。

### 4.5 P2 清单处置

| 编号 | 处置 |
|---|---|
| P2-1 注释与配置漂移 | 四处「`max-open-conns: 1`（config.example.yml 默认）」改为历史表述（「事故当时」），并**新增 `TestExampleYAMLMatchesBuiltinDefaults`** 钉住样例与内置默认不漂移——该漂移正是 P0 事故的直接成因（`Default()` 早是 4、样例写 1，而样例才是首启释放到生产的值） |
| P2-2 探针不统一 | 服务级用例改为**复用出厂守卫**（`registerTickMuWatch` → `orch.LockGuard()`），不再自造 TryLock 探针。两者误报方向不同，判据分叉会让「用例通过」与「生产不报警」同时成立 |
| P2-3 文件头与实现不符 | tick 用例的「控制操作允许持 mu 做 DB」订正为现状：`Pause`/`Cancel` **已不持 mu**（§2C），仍持 mu 的是 `apply*` 系列（生产入口全走 `*InTx`，直接调用者只有同包测试） |
| P2-4 措辞过高 | `DefaultTxTimeoutMs` 的「保证 60s 内**释放**连接」改为「**发起**回滚」，并说明回滚实际入队需经 `Conn.grabConn` 的 `closemu` 写锁、会被在途查询排队 |
| P2-5 归因错误 | 见 §4.4（含 `busy_timeout` 取值建议） |
| P2-6 Unlock 顺序 | `lockguard.Mutex.Unlock` 先 `Unlock` 再清 owner 与全局计数（两组不变量的方向相反：owner 必须在释放前归零，`heldTotal` 必须在释放后递减） |
| P2-7 运行期死代码 | `main.go` 装配点注明：守卫在生产观测的是**死代码**（`apply*` 系列只剩测试调用），它的身份是**回归绊线**而非运行期防护；常规防护在 `store` 层预算 |

### 4.6 两处阻断门禁的既有缺陷（验证过程中发现，非本轮引入）

复验「防护是否真的落地」时发现门禁本身有两处红灯。它们与本轮改动无关，但会让 §4 全部用例的保护**失效**
（红灯的守卫等于没有守卫），故一并修掉。两处都做了决定性实验确认归属。

**（1）P0 复现用例的时钟是定时炸弹**。`delivery_deadlock_repro_test.go` 把 fixture 时钟写死为事故时刻
`clock = 2026-10-10 02:41:00 UTC`。审批 worker 用该时钟写租约
（`claimNext` → `lease_until = clock + approvalWorkerLease(30s)`），而许可签发判据在
`authz/authorization.go:527` 用的是**真实** `time.Now()`：
`!time.Now().UTC().Before(req.LeaseUntil.UTC())` → `ErrForbidden`。
于是真实时间一旦越过「写死时刻 + 30 秒」，批准执行就再也拿不到许可，两条用例**必然**失败
——表现是「批准执行完成但单未进入 rolling」与「批准执行未在 5s 内到达闸门」，
与锁序、连接池毫无关系（后者那条「复现前提失效」的文案还会把人引向错误方向）。

*决定性实验*：仅把该写死值换成 `time.Now().UTC()`，两条用例立刻双双转绿（`ok 0.828s`）；改回即复红。
即失败完全由「写死时钟 vs 真实时间」驱动。

*处置*：改为 `p0FixtureClock()`——以真实当前时刻为基准、用例内恒定。
保留了「时钟受控、断言确定」的性质（`env.clock` 只读、中途不推进），又天然避免与真实时钟比较时过期。
**未采用**「给 `executionAdapter` 加可注入时钟」这个方案：那只是为了「让测试能写死日期」而改生产接口，
是把测试的人为约束泄漏进生产代码；而本组用例要判的是锁序，并不关心「现在是几点」。

**（2）换版验证期的确定性 data race**。`CheckAndAutoRollback` 在启动后台定时器**之后**才读包级
`verifyDuration`（`selfreplace.go`），而 `selfreplace_test.go` 用 `t.Cleanup` 恢复同一变量——
上一个用例遗留的定时器 goroutine 与恢复动作并发读写同一地址。`-race` 实测 3/3 确定性复现。

*处置*：把验证期在**启动 goroutine 之前**读入局部变量，后台定时器只用该快照。
这不只是为了消 race：**验证期本应在「进入验证那一刻」定下**，让定时器稍后再去读可能已被改写的全局值
本身就是错的语义（若运维中途改配置，睡在 `time.Sleep` 里的那个定时器行为将不可预测）。

这两处修完后，`go test ./...` 与 `go test ./... -race` **全绿**，§4 的全部用例才真正构成门禁。

## 5. 验收

- `store` 层：`applySQLiteTxLock` 注入与不覆盖语义、以及「池=4 下『先读后写』并发事务零失败」（移除注入即变红，已反向验证：失败 28/40）。
- 连接等待预算（§2A）：池占满时裸查询与事务均**准时失败**并带 `DB_WAIT_TIMEOUT` 可读原因；
  **长事务不误杀**（睡眠超过取连接预算、仍在事务寿命预算内必须成功提交——防退化为一阶段）；
  不覆盖调用方已有 deadline（语句与事务两路）；多轮事务（含回滚路径）后无连接泄漏；
  装配点全路径覆盖（包装层命中 > 0，事后赋 `db.ConnPool` 的实现会零命中）；预算语义（0=默认、负=显式关闭）。
- 健康探测预算（§4.1）：池占满时探测**准时失败**而非挂起（反向验证：去掉 `NewBoundedPinger` 的包装层分支
  即挂到 testing 超时）；池空闲时必须成功（排除「一律失败」的假实现）；调用方期限不被覆盖、
  且不得被误报成池耗尽。
- 长查询归属与归因（§4.2 / §4.4）：**文件锁竞争不得被报成池耗尽**（实测：预算 300ms + `busy_timeout` 5000ms 下
  写冲突耗时 5.01s，修复前被判成 `DB_WAIT_TIMEOUT`）；归档数据面语句的 ctx **必须带长期限、且热/归档两库都带**
  （反向验证：去掉 `bindLongBudget` 的两处 `WithContext` 即红并指认热库未带期限）；
  50ms 极紧预算下经 `CreateJob` + `drainActive` 的整单端到端成功。
- 归档锁内 DB 纳入观测（§4.3）：装配后句柄非空；服务自身「持锁查库」必被捕获（有牙）；
  `CreateJob` / `CancelJob` 的真实锁内 DB 计入违规数（命中）；观测关闭时零噪声（生产默认态）。
- 样例与默认不漂移（§4.5 P2-1）：`TestExampleYAMLMatchesBuiltinDefaults` 逐项比对
  `config.example.yml` 与 `Default()`（反向验证：把样例的 `max-open-conns` 改回 1 即红并指出两值差异）。
- 锁内 DB 访问守卫（§2B）：**先自校准再断言**——不持锁不误报、持锁必捕获；
  精确归属（他人持锁时本 goroutine 的 DB 访问不得被判违规）；六个处理器逐一覆盖；
  包装锁与 `sync.Mutex` 语义一致（含并发互斥与持有计数归零）。
  服务级用例**复用出厂守卫**而非自造探针（§4.5 P2-2），使用例结论与生产行为同源。
- 控制操作去锁（§2C）：`Pause` / `Cancel`（含 `Cancel` 收口在途目标的重分支）在观察窗内
  **零持锁违规**，且窗口内确有 DB 访问（防判据空洞）。已用负向对照验证判别力：
  把 `s.mu.Lock()` 加回 `Pause` 即失败并精确指认违规调用点。
- 门禁自身的健全性（§4.6）：P0 复现用例的 fixture 时钟**不得与真实时间比较时过期**
  （改用 `p0FixtureClock()`；反向验证：写回事故时刻即复红）；换版验证期不得读写同一包级变量的竞态
  （`-race` 曾 3/3 复现，现按启动前快照取值）。
- 路由层：诊断端点逐条注册、未鉴权 401（缺凭据 / 错令牌 / 错密钥）、有效令牌可取到真实 goroutine 栈、readonly 403、不污染管理面路由目录。
- 全量：`go build ./...`、`go test ./...`、`go test ./... -race`、`go vet ./...`、
  `gofmt -l`（空）、`golangci-lint run ./...`（0 issues）全绿。

## 6. 单证

- 实测程序：`apps/server/.tmp/sqlite-probe/`（gitignore 覆盖，不入库；用法见文件头注释）。
- 配置样例：`config.example.yml` `database` 段。