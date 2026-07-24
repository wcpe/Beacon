# 功能规格：跨平台 Agent E2E 启动链路根因修复

> 状态：已实现 / 本地发布准入已通过；远端 Linux E2E run `29670927028` 已成功，2026-07-20 已补 failure/cancelled 的独立指纹扫描契约，剩余平台与真实 Actions Artifact 验收待完成　·　关联 PRD：FR-169　·　关联验收：FR-143 / FR-144 / FR-145 / FR-147 / FR-148 / FR-149 / FR-171

## 1. 背景与目标

已交付的 Go 真机 E2E 由控制面测试驱动 Gradle，分别启动 Paper 与代理进程，再验证 directory、override、指标、调度、连接消息和热重载等链路。修复前 `apps/agent` 使用 `xyz.jpenilla.run-paper` / `xyz.jpenilla.run-waterfall` `2.3.1`；两者的下载路径依赖已失效的 PaperMC API v2。Gradle 子进程可在服务就绪前退出，而旧 `GradleProc` 没有把子进程退出状态接入 HTTP/在线状态轮询，directory 与 override 会盲目等待最长 12 分钟，最终只报告业务条件超时，掩盖真正的启动失败。该启动链现已由 Maven 正式版 `mc-testkit 0.5.0` 与 guarded polling 取代。

本修复不改变 Beacon 产品能力，而是恢复并补强 P10 准入所依赖的跨平台验证链：

1. 删除失效的 run-paper / run-waterfall 启动器，统一锁定 `mc-testkit 0.5.0`。
2. 由 `agent-e2e` 声明稳定的 `servePaper`、`serveDirectory`、`serveProxy` 任务；代理使用原生 BungeeCord，不再以 Waterfall 代验。
3. Go E2E 继续作为控制面与断言的唯一驱动者，但能够立即感知 Gradle 早退并报告证据日志。
4. directory、override 与所有受旧任务名影响的既有断言原样保留；修复的是启动与诊断链，不是降低验收标准。
5. 在 Linux、Windows、macOS 上用同一生命周期语义完成启动、等待、失败和收尾；workflow 无条件执行敏感扫描，仅在两项 E2E、清理与扫描全部成功后归档日志。

该问题属于“已交付能力的验证基础设施回归”：FR-143、FR-144、FR-145、FR-147、FR-148、FR-149、FR-171 等已交付标记保持不变。`mc-testkit 0.5.0` 于 2026-07-18 发布，2026-07-19 本规格的单元、构建、正式 Maven 解析与本地 directory/override 真机最终准入已通过；远端 Linux E2E Actions run `29670927028` 已成功。FR-169 仍为 `开发中`；完整远端覆盖、Actions Artifact 真验收与 P10 其余门完成前，不得把 FR-169 或相关完整远端证据门标记为通过。

## 2. 需求（要什么）

### 2.1 Gradle 编排迁移

- 从 `apps/agent/settings.gradle.kts` 删除 `xyz.jpenilla.run-paper` 与 `xyz.jpenilla.run-waterfall` 的插件版本声明。
- 从 E2E 模块删除对 `RunServer`、`RunWaterfall` 及 `runServer` / `runBungee` 任务的使用。
- 在插件仓库中以精确版本应用 `top.wcpe.mc-testkit` `0.5.0`；禁止 `+`、动态范围、SNAPSHOT 或静默回退到旧启动器。
- `agent-e2e` 声明三个由 Go harness 调用的持久 serve 任务：
  - `:agent-e2e:servePaper`：单个 Paper 后端，供 Bukkit 类 E2E 使用。
  - `:agent-e2e:serveDirectory`：Paper 后端 + 原生 BungeeCord 代理；代理预置手工路由 `backend`，供 directory 验证“手工节点保留 + Beacon 动态节点注入”。
  - `:agent-e2e:serveProxy`：原生 BungeeCord 代理运行目标，供纯代理侧连接/消息探针使用；若 `mc-testkit 0.5.0` 的合法拓扑要求伴随后端，则使用仅承担路由就绪的最小测试后端，不赋予任何 Beacon 产品语义。
- Bukkit Agent、Bungee Agent 与现有 E2E 探针仍由 Gradle 构建并按节点类型注入；不得把测试探针打入产品发布产物。
- directory 必须从运行时探针断言代理实现标识为 `BungeeCord`，而不是只根据 Gradle DSL 推定；不得再接受 Waterfall 作为等价通过。

### 2.2 Go E2E 调用契约

旧任务调用全部迁移，不能保留隐式兼容别名：

| 既有调用 | 新调用 | 受影响套件 |
|---|---|---|
| `:agent-e2e:runServer` | `:agent-e2e:servePaper` | override、metrics、metricsv2、schedhealth、schedagent、connmsg 消息、hotreload |
| `:agent-e2e:runServer` + `:agent-e2e-bungee:runBungee` | 单个 `:agent-e2e:serveDirectory` | directory |
| `:agent-e2e-bungee:runBungee` | `:agent-e2e:serveProxy` | connmsg 连接 |

- directory 不再分别管理 Paper 与代理两个 Gradle 生命周期；一个 `serveDirectory` 进程拥有完整拓扑，避免部分启动、部分早退和重复 Wait。
- Go 仍负责：启动控制面、创建 namespace/凭据、启动 Gradle serve、轮询控制面状态、执行断言、结束 Gradle 进程树。
- 不把业务断言迁入 Gradle，不引入第二套 E2E 判定真源。

### 2.3 运行目录契约

使用 `mc-testkit` 的 `RunLayout`，不再读取旧的 `apps/.tmp/run-paper*` / `run-waterfall*` 目录：

| 内容 | 新路径（相对仓库根） |
|---|---|
| Paper 单后端/Directory 后端运行目录 | `apps/agent/agent-e2e/build/mc-testkit/run/` |
| Directory/Proxy 的 BungeeCord 运行目录 | `apps/agent/agent-e2e/build/mc-testkit/run-proxy/` |
| mc-testkit 结果、pid 与编排侧日志 | `apps/agent/agent-e2e/build/mc-testkit/results/` |
| Go harness stdout/stderr | `.tmp/<log-prefix>.out.log`、`.tmp/<log-prefix>.err.log` |

- Go 断言读取的 `identity.yml`、探针快照和插件数据目录必须改到对应的新运行目录。
- 路径统一经 `filepath.Join` 从仓库根推导，禁止写死盘符、`/tmp` 或仅适用于 Unix 的分隔符。
- 每轮测试仍使用独立日志前缀；运行目录是否由 mc-testkit 清理由其契约负责，Go 不并行清理正在使用的目录。
- `servePaper` 的每次启动都是独立生命周期：mc-testkit 会清理并重建 backend 运行目录，`plugins/BeaconAgent` 下的本地 identity 文件不会跨轮保留。依赖两轮 Paper 的 override 验收必须把第二轮重新进入 pending 并重新审批视为预期步骤，不得声称复用第一轮本地 identity。

### 2.4 `GradleProc` 生命周期与唯一 Wait

`GradleProc` 必须成为 Gradle 子进程生命周期的唯一所有者：

1. `StartGradleTask` 成功启动后立即创建一个内部 waiter；整个进程生命周期中只有该 waiter 调用一次 `cmd.Wait()`。
2. waiter 保存退出结果并关闭只读完成信号；退出码为 0 但发生在 serve 就绪前，同样属于“意外早退”。
3. `Stop` 只负责把状态切换为 stopping、整树终止仍存活的进程，并等待同一个完成信号；不得再次调用 `cmd.Wait()`。
4. 日志文件只在 waiter 完成后关闭一次；启动失败时沿用现有失败清理，不遗留句柄。
5. 多次 `Stop`、进程已退出后 `Stop`、测试清理与早退并发发生均须幂等，不得触发 `Wait was already called`、数据竞争或句柄重复关闭。
6. `GradleProc` 暴露早退检查能力，返回至少包含任务名、退出结果、stdout 日志绝对路径、stderr 日志绝对路径的错误；不得把完整日志内容无界拼入错误。

### 2.5 directory / override 轮询早退保护

- directory 的 identity、pending、active、在线节点与目录快照轮询，以及 override 每轮 identity/active/online 轮询，都必须携带对应 `GradleProc` 的早退 guard。
- override 的两轮 `servePaper` 是两个独立生命周期：第一轮停止后仍执行 `OfflineInstance` + `CancelOfflineInstance`，但其目的仅是清除陈旧 online 与主动下线产生的粘性拒绝态；第二轮必须等待本轮新 identity 的 pending，审批时解绑占用同一 serverId 的第一轮旧 active identity，再确认本轮 identity active 且实例 online。
- identity 列表等待必须同时按 namespace、精确 serverId 与目标 status 筛选；即使 API 返回同 serverId 的历史 active/unbound 记录，也不得让 pending 查询错误命中旧记录。
- 每次探测业务条件前以及等待下一次重试前均检查 guard；一旦 Gradle 完成且测试尚未进入主动 Stop，立即终止轮询。
- 早退错误必须优先于后续 HTTP 超时，并明确输出两条 Go harness 日志路径；若 mc-testkit 节点日志路径已知，也一并输出。
- 仍保留现有业务超时上限，用于首次下载、慢启动或控制面状态收敛；不得通过缩短 12 分钟上限掩盖生命周期缺陷。
- guard 必须区分“测试主动收尾”与“就绪/断言前意外退出”，避免正常 `defer Stop()` 被误报为失败。

### 2.6 日志归档与凭据安全

- `.github/workflows/e2e.yml` 在成功、失败、取消三种结果下均以 `if: always()` 执行归档前扫描；directory、override、控制面清理或扫描任一失败时都禁止上传 Artifact，不能改成失败时上传日志。
- 扫描与上传使用完全相同的候选路径：
  - `.tmp/*.log`；
  - `apps/agent/agent-e2e/build/mc-testkit/**/*.log`；
  - `apps/agent/agent-e2e/build/mc-testkit/results/**`。
- 路径不存在时扫描不得覆盖原测试结论；artifact 保留期遵守仓库现有短期开发证据策略。动态凭据指纹文件、测试启动标记与凭据生成状态文件均不属于上传路径。
- namespace token、access token、bootstrap token、DSN、密码等敏感值不得出现在 Gradle 命令行参数、任务描述、日志路径、错误文本或归档内容中。动态 access token 原文不得写入 `GITHUB_ENV`、普通文件、步骤输出或跨步骤环境，只能在当前 Go 测试进程内登记、通过 `add-mask` 遮蔽，并经 `StartGradleTask` 的子进程环境注入。
- E2E job 注入基于 `github.run_id` 的唯一环境哨兵，使 Gradle 与 ServerLauncher 子进程自然继承。scanner 继续按原文覆盖该哨兵、管理员口令、签名密钥、数据库 DSN、bootstrap token 与 PostgreSQL 密码；动态 access token 改用 SHA-256 与 UTF-8 字节长度组成的不可逆指纹扫描，不再依赖 workflow 获取 token 原文。
- `CreateV2Namespace` 在确认响应 token 非空后、返回调用方前调用 `registerArtifactSecret`。Actions 中每个新 token 必须先向当前测试步骤专属的生成状态文件原子、同步追加固定 `generated` 记录，再把严格格式 `<64 位小写 SHA-256> <正整数字节长度>` 原子、同步写入 workflow 指定的 `.tmp/e2e-secret-fingerprints`，文件权限为 `0600` 且重复指纹去重；只有持久化成功后才输出 `add-mask` 并把 token 登记到当前 Go 进程内存。任何错误均不得包含 token、命中片段或哈希。
- directory 与 override 在执行 `go test` 前分别写入固定内容的“测试步骤已启动”标记；scanner 严格校验启动标记、生成状态记录数与指纹记录数。两个测试都在创建任何 namespace 前失败时，指纹文件允许为空或不存在；一旦生成状态表明已取得动态 token，指纹缺失、为空、格式损坏、权限错误、重复或记录数不匹配都必须 fail-close。
- 独立 scanner 对每个候选文件的原始字节，按登记长度滑动窗口计算 SHA-256 并与指纹集合比较，因此即使 Go 测试被取消或强杀、最终清理未执行，也能发现 token 嵌入 JSON、HTTP header 或任意字节位置。动态命中只报告候选文件相对路径与“动态凭据指纹”，不得输出 token、命中片段或哈希。

### 2.7 范围边界

**范围内：** Gradle E2E 插件/任务声明、Go E2E harness 生命周期、受旧任务名影响的测试路径、directory/override 早退保护、E2E workflow 日志归档与测试凭据传递。

**不做（范围外）：**

- 不修改控制面业务逻辑、HTTP API、Agent 产品协议、数据库 schema/迁移、前端或已交付业务断言。
- 不删除、跳过、放宽 override 的任何既有业务断言；包括 inert、filetree、ordering、回滚与 fail-static。两轮 Paper 必须保持独立，第二轮按 mc-testkit 干净运行目录语义重新审批新 identity。
- 不以 Velocity/Waterfall 替代本次要求的原生 BungeeCord。
- 不新增 Redis、外部队列、Docker-in-Docker、远程 E2E 服务或第二套编排器。
- 本规格阶段不更新 `CHANGELOG.md`；实现与真机验收完成后再同步变更历史。

## 3. 设计（怎么做）

### 3.1 模块职责

| 模块 | 职责 | 禁止承担 |
|---|---|---|
| `agent-e2e` Gradle 配置 | 声明 Paper/BungeeCord 拓扑、节点插件注入、固定 serve 任务名与运行目录 | 控制面业务断言 |
| Go `harness.GradleProc` | 启动、唯一 Wait、早退信号、整树收尾、日志证据定位 | 判断 Agent 业务是否正确 |
| Go directory/override 测试 | 驱动控制面并执行既有业务断言；轮询时观察 GradleProc | 下载/直接管理 Paper 或 BungeeCord jar |
| E2E workflow | 选择套件、设置环境、无条件扫描敏感内容并在全部门禁成功后归档证据 | 从日志推断通过、覆盖测试退出码 |

这仍符合既有“Go 控制面 E2E 驱动 + Kotlin Agent 真进程”的分层；mc-testkit 只替换失效的 Minecraft 进程编排依赖，不进入生产运行时。

### 3.2 进程状态模型

`GradleProc` 最小状态语义如下：

- `running`：子进程已启动，waiter 尚未收到退出结果。
- `exited`：waiter 已完成；若未标记 stopping，则 guard 返回意外早退错误。
- `stopping`：测试主动收尾已开始；guard 不再把随后退出解释为启动失败。
- `stopped`：waiter 已完成且 Stop 收尾结束。

状态转换必须由同步原语保护。实现可以使用 channel + `sync.Once`/mutex，但不为了未来场景新增通用进程框架；目标仅是保证唯一 Wait、幂等 Stop 与无竞争的错误读取。

### 3.3 Guarded polling

轮询原语采用“业务探测 + 进程 guard + deadline”组合：

1. 检查 Gradle 是否意外退出；若是，立即返回带日志路径的根因错误。
2. 执行一次 HTTP/文件业务探测；满足条件则成功。
3. 若业务探测返回不可重试错误，立即返回该错误。
4. 在重试间隔中同时等待 timer 与 Gradle 完成信号；进程先退出则不等待下一轮。
5. deadline 到期时返回现有业务超时，并附最近一次业务错误与日志路径。

不得在每个测试复制一份 select/轮询实现；扩展现有 harness 等待助手或新增一个最小的受 guard 轮询原语，由 directory 与 override 复用。

### 3.4 BungeeCord directory 验收拓扑

- `serveDirectory` 的静态代理配置包含手工节点 `backend`，指向本轮 Paper 后端。
- Bungee Agent 以既有 Beacon server ID 注册并接收控制面目录，动态节点与 `backend` 名称不同。
- directory 保留既有断言：pending → 审批/分配 → active/online；静态 `backend` 不丢失；Beacon 动态节点被加入；重复同步幂等；失联/恢复语义不退化。
- 增加运行时实现断言：代理探针报告 canonical implementation `BungeeCord`；出现 `Waterfall` 或无法识别实现均失败。

### 3.5 测试先行与红转绿

实现必须先落能复现根因的失败测试，再修改生产测试 harness：

| 阶段 | 旧失败（红） | 修复后（绿） |
|---|---|---|
| Gradle 启动 | run-paper/run-waterfall 2.3.1 请求失效 API，Gradle 在节点就绪前退出 | `mc-testkit 0.5.0` 的 `serve*` 任务成功拉起精确拓扑，端口与 Agent 状态可达 |
| Wait 所有权 | 清理路径和退出观察均可能调用 `cmd.Wait()`，且轮询不知道进程已死 | 每个 Gradle 子进程精确一次 Wait；并发/重复 Stop 测试无竞争、无二次 Wait |
| 早退诊断 | 子进程数秒内退出，directory/override 仍等到业务 deadline | 夹具进程退出后，受 guard 轮询在下一检查周期内失败，并包含任务名及 stdout/stderr 路径 |
| directory | Paper/Waterfall 分别启动，任一早退都可能表现为 12 分钟在线超时 | 单个 `serveDirectory` 启动 Paper + 原生 BungeeCord；全部既有目录断言和实现断言通过 |
| override | 第二轮误等第一轮旧 active identity，未审批干净运行目录生成的新 pending identity | 每轮 `servePaper` 早退立即失败；第二轮明确等待并审批本轮新 identity 后再等 active/online，全部既有 override 业务断言不删且通过 |
| CI 证据 | 失败/取消后最终清理可能不执行，workflow 又无法识别动态 token | 任意 job 结论均运行独立指纹扫描；仅 directory、override、清理与扫描全部成功时上传日志 |

建议的自动测试层次：

- Go harness 单元/小型集成夹具：唯一 Wait、早退（非零与零退出）、主动 Stop、重复 Stop、日志路径、等待中进程退出。
- Gradle 配置测试：三个任务存在、旧任务不存在、版本精确锁定、Directory 代理平台为 BungeeCord、运行目录符合契约。
- Go 真机 E2E：directory、override 必跑；其余受任务名迁移影响的 metrics、metricsv2、schedhealth、schedagent、connmsg、hotreload 至少逐包执行一次，证明没有遗留旧任务/路径。
- workflow 契约测试或静态检查：归档步骤为 always、矩阵 artifact 命名唯一、敏感值不作为 `-P` 参数。

## 4. 任务拆分

- [x] 新增 GradleProc/guard 回归测试，覆盖唯一 Wait、早退、主动/重复 Stop 与 guarded polling。
- [x] 验证 `mc-testkit 0.5.0` 插件坐标、Maven 仓库可解析性、serve DSL 与运行目录 API。
- [x] 删除 run-paper/run-waterfall 声明与任务实现，精确锁定 mc-testkit 0.5.0。
- [x] 在 `agent-e2e` 声明 `servePaper`、`serveDirectory`、`serveProxy`，完成 Bukkit/Bungee 探针按节点注入与原生 BungeeCord 实现标识输出。
- [x] 重构 `GradleProc` 为唯一 Wait 所有者，增加退出结果缓存、early-exit guard、幂等 Stop 与日志路径错误。
- [x] 更新全部 Go E2E 旧任务名和运行目录；directory 合并为单个 serveDirectory 生命周期。
- [x] 将 directory/override 轮询接入 guard，保留原业务 deadline 和全部既有断言。
- [x] 将动态 E2E 凭据从 Gradle `-P` 参数迁到不回显的子进程环境，并增加归档候选文件泄漏扫描。
- [x] 更新 E2E workflow 为 always 扫描；仅两项 E2E、清理与扫描全部成功时归档 Go/Gradle/mc-testkit 日志。
- [x] 动态 access token 已改为当前 Go 进程内登记与子进程环境注入；Actions 仅持久化 `0600` 的 SHA-256 + 字节长度指纹及无敏感内容的生成状态，不写 `GITHUB_ENV`。
- [x] 通过 Go harness race、全部 E2E 包编译、`:agent-e2e:tasks --all`、完整 Agent build 与本地 directory/override 真机验收。
- [x] 同步 PRD 状态、运维文档与 `CHANGELOG.md`，明确本地发布准入与远端待验收边界。
- [x] 真实 GitHub Actions 的 Linux E2E 已成功：run `29670927028`。
- [x] 2026-07-20 已补 failure / cancelled 的独立指纹扫描、损坏状态 fail-close 与取消模拟自动测试。
- [ ] 补齐剩余平台、真实 failure / cancelled Actions run、Artifact 真验收及四平台进程门。

## 5. 验收标准

### 5.1 自动验收

1. 仓库中不再出现 `xyz.jpenilla.run-paper`、`xyz.jpenilla.run-waterfall`、`RunServer`、`RunWaterfall`、`:agent-e2e:runServer`、`:agent-e2e-bungee:runBungee` 的有效构建/测试引用。
2. `mc-testkit` 插件版本精确为 `0.5.0`，三个新任务可由 `apps/agent/gradlew[.bat] :agent-e2e:<task> --no-daemon` 解析。
3. `servePaper` 使用 Paper；`serveDirectory` 和 `serveProxy` 使用原生 BungeeCord；directory 的运行时探针值精确表明 BungeeCord，不含 Waterfall 代验。
4. GradleProc 夹具证明每个子进程只执行一次 `cmd.Wait()`；重复/并发 Stop 通过 Go race 检查支持的平台验证，无 panic、无僵尸、无日志句柄泄漏。
5. 夹具子进程在业务条件满足前以非零或零退出时，guarded wait 均在一个轮询周期内失败；错误包含任务名、退出结果、stdout 与 stderr 的绝对路径。
6. directory 全部既有断言通过，并额外通过 BungeeCord 实现断言；不再并行启动两个 Gradle 任务。
7. override 全部既有业务断言一项不删并通过；任一 servePaper 早退不会继续等待到 12 分钟 deadline；第二轮在 mc-testkit 重建运行目录后获得不同于第一轮的新 identity，完成 pending → 审批（解绑旧占用者）→ active → online。
8. metrics、metricsv2、schedhealth、schedagent、connmsg、hotreload 不再引用旧任务/旧运行目录，并在迁移后逐包通过。
9. E2E workflow 在 `success`、`failure`、`cancelled` 三类结论下均执行稳定 id 的归档前扫描；只有 directory、override 与扫描 outcome 全部为 `success` 才上传对应日志。清理阻断标记、动态指纹命中或扫描状态损坏均令 job 失败且 Artifact 不上传。
10. 扫描与上传共用 `.tmp/*.log`、`apps/agent/agent-e2e/build/mc-testkit/**/*.log`、`apps/agent/agent-e2e/build/mc-testkit/results/**` 三组候选路径；固定敏感值按原文扫描，动态 token 按严格的 SHA-256 + 字节长度记录做滚动窗口比对。指纹文件本身不上传；缺失/空文件仅在无生成记录时允许，格式、权限、重复与计数不一致均 fail-close；动态命中输出只含相对路径与“动态凭据指纹”。

### 5.2 跨平台真机验收

- **Linux amd64**：directory、override 与全部受迁移影响套件通过；进程树收尾后端口释放。
- **Windows amd64**：使用 `gradlew.bat`，路径和整树终止正确；无二次 Wait、僵尸 Java/Gradle 进程或文件句柄占用。
- **Darwin arm64**：使用 Unix wrapper，Ctrl/kill 收尾路径与 Linux 同语义；日志完整归档。
- 任一平台模拟 Gradle 早退时，测试在秒级暴露 Gradle 根因与日志路径，而不是只显示 identity/online 的 12 分钟超时。

### 5.3 FR-169 证据门

- 本规格全部验收通过前，`Q-E2E-P4`、`Q-E2E-P5`、`Q-E2E-P9` 以及依赖该启动链的四平台 Agent smoke 不得标记 passed。
- 已交付 FR 的产品状态不因测试基础设施回归而回退；但新的 RC/GA 证据必须来自修复后的同一 commit/产物，旧的 Waterfall/run-paper 成功记录不得替代。

### 5.4 2026-07-18 发布与 2026-07-19 本地发布准入记录

- `mc-testkit 0.5.0` 已于 2026-07-18 发布到 `maven.wcpe.top`，并推送上游 `origin/master` 与 tag `v0.5.0`。
- Beacon 未设置 `MC_TESTKIT_INCLUDE_BUILD` 时，`:agent-e2e:tasks --all` 与完整 Agent build 成功；Go harness race 与全部 E2E 包编译通过。
- directory 使用 Maven 正式版 `0.5.0` 于 2026-07-19 最终复验通过，耗时 `240.775s`：原生 BungeeCord 与插件加载成功，双 identity online，动态目录与固定 `backend` 路由同时成立；杀控制面后持续 `12s` 验证 fail-static。
- override 使用 Maven 正式版 `0.5.0` 于 2026-07-19 最终复验通过，耗时 `280.526s`：两轮独立 Paper 均重新进入 pending 并审批本轮 identity，inert / filetree / ordering 全部通过；杀控制面后持续 `35s` 验证 fail-static。
- 本地端口 `8848`、`25566`、`25577` 均无 `LISTENING` 残留。
- 远端 Linux E2E Actions run `29670927028` 已成功；2026-07-20 已在本地补齐 failure / cancelled 独立指纹扫描与取消模拟自动测试。剩余平台、真实 failure / cancelled Actions run、Artifact 验收及四平台进程门尚未完成，不改变 `Q-E2E-P4`、`Q-E2E-P5`、`Q-E2E-P9` 或 FR-169 的未完成状态。

## 6. 风险 / 待定

- **mc-testkit 版本可用性已解除（2026-07-18）**：`mc-testkit 0.5.0` 已发布到 `maven.wcpe.top`，上游 `origin/master` 与 tag `v0.5.0` 已推送；Beacon 未设置 `MC_TESTKIT_INCLUDE_BUILD` 时，`:agent-e2e:tasks --all` 与完整 Agent build 均成功，证明默认 Maven 正式工件可解析。禁止静默改用 0.4.x、snapshot 或恢复旧插件。
- **0.5.0 API 契约已确认**：`servePaper` / `serveDirectory` / `serveProxy` 任务名、原生 BungeeCord 平台、运行目录消费点、唯一 Wait 和验收标准已按正式工件落地；后续变更不得漂移这些契约。
- **首次下载时长**：业务 deadline 仍需覆盖冷缓存下载；early-exit guard 只缩短“进程已经退出”的失败，不把慢启动误判为退出。
- **归档与密钥**：always 扫描会扩大失败证据覆盖面，因此不能靠删日志解决泄漏。动态 namespace access token 仅在当前 Go 测试进程登记并遮蔽，随 Gradle 子进程环境注入，绝不写入 `GITHUB_ENV`、普通文件或步骤输出；Actions 只保存 `0600` 的 SHA-256 + 字节长度指纹和无敏感内容的生成状态。Go 最终清理与 workflow 独立 scanner 构成两道门，后者不依赖测试进程正常退出。目录、覆盖、清理或扫描任一失败时均不得上传 Artifact；远端仍须验证真实 failure / cancelled run。
- **ADR 对齐**：本修复不改变生产架构、产品协议或发布模型，不需要新增 ADR；实现必须保持 [ADR-0024](../adr/0024-bc-backend-membership-as-fact.md) 的 BC 后端成员事实模型、[ADR-0025](../adr/0025-bc-proxy-metrics-and-netty-traffic.md) 的代理指标边界、[ADR-0028](../adr/0028-allow-hosting-agent-self-dir.md) 的 Agent 自目录规则、[ADR-0031](../adr/0031-zone-default-entry-and-bc-injection.md) 与 [ADR-0067](../adr/0067-default-entry-v2-authority.md) 的默认入口/BC 注入权威，以及 [ADR-0035](../adr/0035-backend-reachability-tcp-connect.md) 的后端 TCP 可达性语义；RC/GA 证据继续受 [ADR-0072](../adr/0072-immutable-rc-ga-promotion-and-n-minus-one.md) 与 [ADR-0073](../adr/0073-standard-rc-ga-release-lifecycle.md) 约束。发现 mc-testkit 需要改变这些边界时必须停下并另行决策，不能在实现中静默扩范围。
