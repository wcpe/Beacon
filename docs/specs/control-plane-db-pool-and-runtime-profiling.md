# 功能规格：控制面连接池纵深防御与运行时取证（pprof）

> 状态：开发中　·　分支：fix/deadlock-single-connection　·　关联：生产 P0 死锁（2026-10-10 02:41:05）
> 配套：同批另有两组分别根治环的两边（审批事务不持连接等锁 / 推进器 tick 不持锁等连接）。
> **本文档只覆盖纵深防御（B）与取证能力（D）**：它们不根治锁序问题，但分别「抬高成环门槛」与「让下一次事故有栈可查」。

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

## 4. 验收

- `store` 层：`applySQLiteTxLock` 注入与不覆盖语义、以及「池=4 下『先读后写』并发事务零失败」（移除注入即变红，已反向验证：失败 28/40）。
- 路由层：诊断端点逐条注册、未鉴权 401（缺凭据 / 错令牌 / 错密钥）、有效令牌可取到真实 goroutine 栈、readonly 403、不污染管理面路由目录。
- 全量：`go build ./...`、`go test ./...`、`golangci-lint run ./...` 全绿。

## 5. 单证

- 实测程序：`apps/server/.tmp/sqlite-probe/`（gitignore 覆盖，不入库；用法见文件头注释）。
- 配置样例：`config.example.yml` `database` 段。
