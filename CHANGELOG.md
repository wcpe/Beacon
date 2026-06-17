# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/) 与[语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布版本

### 新增
- 管理面鉴权（FR-11，自 P2 前移，见 [ADR-0009](docs/adr/0009-control-plane-auth-pulled-forward.md)）：新增 `POST /admin/v1/auth/login`（单操作者凭据 → 无状态 HMAC 签名令牌）；`/admin/v1/*`（登录除外）挂登录令牌中间件，缺/错/过期令牌一律 401；写操作 operator 改以认证身份为准写入审计，取代前端手填值；凭据/密钥走 env（`BEACON_ADMIN_USERNAME`/`BEACON_ADMIN_PASSWORD`/`BEACON_AUTH_SECRET`）不落库、不引 Redis；agent 侧共享 token 语义不变。
- agent 本地运维命令（FR-17，仅本地）：双端壳注册 `/beacon`（权限 `beacon.admin`）含 `status`（生命周期 / 连接 / 有效配置 md5 / 心跳周期 / endpoint）、`reload`（`forcePollNow` 以 md5=null 强制重拉并经幂等守卫 apply，不等长轮询超时）、`reconnect`（`reconnectNow` 重置退避重连、不清空 store/快照保 fail-static）、`resync`（依赖文件树托管 FR-14 未启用，占位提示）；命令经 `adapter.runAsync` 不上 MC 主线程、core 控制方法不碰平台库（守 ADR-0005）。并修复注册多触发点（心跳 404 / 长轮询 404 / 退避重试 / reconnect）无单飞保护导致的瞬时双注册——用 `AtomicBoolean` 单飞门 + 注册代标识收口，保证任意时刻只有一条 register→loops 在飞（并发单测覆盖连续 reconnect、reconnect 与 poll 并发、register 单飞不变量）。远程下发依赖鉴权（FR-11），本期不做。
- 文件树托管（通道B，控制面侧）：新增 `file_object` / `file_revision` 两表（整文件 blob + scope 维度 + append-only 版本，唯一键含 `path`、`content` 落 TEXT 经 GORM size 抽象不绑方言、软删哨兵同 config_item）；`internal/filetree` 纯函数包做 scope **整文件覆盖**（取覆盖链最高层那份、不深合并、不碰 merge 包）+ manifest（path→md5）+ 独立 `fileTreeMd5`（path 名纳入哈希防碰撞）；FileService（建/发布/回滚/软删，事务内 object+revision+audit 原子）+ FileEffectiveService（解析 + 文件长轮询，持独立 Hub）；admin 文件 CRUD/发布/历史/回滚端点与 agent `files/manifest`（长轮询）/`files/content` 端点；长轮询配置与文件**唤醒集合独立**（zone 改派同时唤醒两通道）；path 校验拒绝绝对路径/`..` 穿越/反斜杠防落盘逃逸。filetree 穷举单测（scope 整文件覆盖 + fileTreeMd5 幂等/防碰撞/内容敏感）全绿。agent 侧文件同步器与原子落盘（含父目录 fsync）尚未实现。
- 文件树托管（通道B，agent 侧）：`BeaconApiClient` 增 `pollFileManifest`（带本地 `fileTreeMd5` 长轮询 manifest）/`fetchFileContent`（按 path 取整文件），HTTP/JSON 仍只经 `HttpTransport`/`JsonCodec` 接口；新增 `agent-core` 的 `filetree` 包——`FileSyncer`（manifest 增/改/删纯差分）、`FileMirrorWriter`（原子写：临时文件→`FileChannel.force` 含父目录 fsync→`ATOMIC_MOVE`，`RelativePathGuard` 拒绝绝对/`..`/反斜杠逃逸目标根）、`AppliedFileManifestStore`（本地已落盘清单原子读写）、`FileTreeApplier`（先文件后清单的增量同步编排）；`AgentLifecycle` 注册成功后并行启文件树长轮询循环（独立 gen/退避、复用单飞、`adapter.runAsync` 不上 MC 主线程），**fail-static 比配置更保守**：控制面不可用/取内容失败/首启无目标态一律不动既有镜像、不写清单、绝不臆测删文件；目标根来自 config.yml `file-tree` 段（镜像落插件 plugins 基目录）。新增单测覆盖差分增改删、原子写+无 tmp 残留+路径拒绝、fail-static 不删既有/不写清单、manifest/content 解析。`resync` 命令的实际接线本期仍占位未做。
- 三方插件文件覆盖兼容（FR-15，见 [ADR-0011](docs/adr/0011-third-party-file-override-and-restricted-reload-command.md)）：在通道B 之上为无法改源码的三方插件提供 override-set（目标插件目录 + 成员文件 + 一条重载命令）。控制面新增 `file_override_set` / `file_override_set_revision` 两表（VARCHAR 枚举 + 软删哨兵 + 事务内 object+revision+audit 原子）与 admin CRUD/发布/历史/回滚 + **dry-run 只读预览**端点（全挂鉴权中间件后，FR-11）；目标根 / 成员路径 / 重载命令早校验。agent 侧新增 `override` 包：覆盖前备份（**回滚只还原不重放命令**）、`CommandWhitelist`（agent 本地白名单、默认空、单条、注入字符拒绝）、`OverridePathSecurity`（Path 级 `plugins/<plugin>/` 限定 + 拒 `..`/绝对/盘符/UNC/`.jar`/server 文件 + Windows 保留名与**段尾点/空格**专项）、`ReloadCommandExecutor`（经 `PlatformAdapter.dispatchConsoleCommand` 派发、不上 MC 主线程同步等结果、**禁 shell**——core/适配器不引入任何进程执行 API）、`ManagedFileTracker`（受管文件标记 + 外部改动告警防震荡环）。安全单测全绿，经一轮对抗式安全审查（未发现可达 RCE/穿越/鉴权绕过）。**注意：override 应用链当前“已建未接”——组件就位并单测，但未接入 `AgentAssembly`/`AgentLifecycle`、控制面亦无 agent-facing 下发端点，命令执行运行期物理断开（符合 ADR-0009 决策3“鉴权 gate 前命令执行不上线”）；接入那一刻才是命令执行真正上线点，须再过一轮命令执行触发路径的对抗评审。**
- 下游 SDK 接入包（FR-16，见 [ADR-0007](docs/adr/0007-versioning-and-release-channels.md)）：新增纯 Java8 零三方依赖的 `agent-kit` 便捷接入层（只依赖 `agent-api`）——`BeaconAccess` 封装下游样板：回退判据只看 `BeaconAgentProvider.isAvailable()`（绝不看 connected 防 split-brain）、读已合并配置 / 查发现便捷方法、配置订阅桥（注册即重放 + 由不可用转可用补注册重放）；非有状态静态单例、不碰线程调度，身份 / zone / ORM 仍归 CoreLib 不重复。`agent-kit` 随 `agent-api` 经 shadowed 打进 BeaconAgent / BeaconAgentProxy jar（下游 compileOnly、运行期由壳提供，免 NoClassDefFound）。`agent-api` 与 `agent-kit` 配 maven-publish（group `top.wcpe.beacon`、artifact `beacon-agent-api` / `beacon-agent-kit`、version 跟随根 `VERSION`，默认 mavenLocal、远程经 `beaconPublishUrl` property/env 可选注入，不硬编码 TabooLib 仓库）。首版随 0.y.z 不承诺向后兼容（ADR-0007）。
- 管理台前端增强（FR-18）：新增登录页 + `RequireAuth` 守卫 + 401 全局拦截，`operator` 改取登录态、`client` 单点注入 `Authorization` 头。新增文件树托管页（过滤 / 新建 / 列表 + 文件详情：编辑发布 / 历史 / 并排 diff / 回滚 / 软删）与三方覆盖集页（列表 + 详情：元数据 + **发布前 dry-run 只读预览**「将覆盖哪些文件 / 执行什么命令」+ 勾选确认门控发布 + 历史 / 回滚）。文本编辑用 `CodeEditor`（textarea + 行号，不引 CodeMirror / Monaco）；不引任何新运行时依赖；不做灰度向导 / 覆盖来源徽标等（轻档）。
- SDK 与 agent-api 接入文档（FR-19）：新增 [docs/SDK.md](docs/SDK.md)——SDK 组成（agent-api + agent-kit）、发布坐标与版本对齐矩阵、`compileOnly` 接入、最小接入示例、`BeaconAccess` / `BeaconAgent` API 参考、回退判据「只看 isAvailable 不看 connected」等接入纪律。

### 变更
- 文件树托管端点写操作 `operator` 改取认证身份（`auth.Operator(ctx)`）而非请求体，与 override-set 写操作一致——`POST /admin/v1/files`、`PUT /admin/v1/files/{id}` 请求体移除 `operator` 字段，后端以登录令牌身份写审计。
- 管理台前端各写操作（配置/文件/zone/实例下线 的新建/发布/回滚/软删/指派/取消指派）不再向请求体/查询发送 `operator`——后端已统一取认证身份并忽略手填值，前端发送即冗余；同时移除随之失效的 `useOperator` hook 与 `requireOperator` 空值校验（登录后身份恒在、未登录由 `RequireAuth` 守卫拦截），登录身份仅保留侧栏「当前操作人」展示用途，行为不变。

## 0.1.0（2026-06-17）

### 新增
- 项目立项与第一期（MVP）设计定稿：确立"控制面（Go + 内嵌 React）/ 数据面（Bukkit/Bungee agent）"架构。
- 架构文档 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：控制面/数据面切分、MySQL+GORM 六表数据模型、scope 覆盖链合并、REST 长轮询热更机制、docker-compose 单节点部署。
- REST 契约 [docs/API.md](docs/API.md)：agent 侧与 admin 侧端点。
- 架构决策记录 [docs/adr/](docs/adr/)：自研而非用 Nacos、Go+内嵌 React 栈、MVP 去 Redis、zone 由控制面权威指派、agent 传输/序列化抽象层、REST 长轮询推送。
- 文档治理：PRD 入库为活文档（[docs/PRD.md](docs/PRD.md)）、新增演进与维护指南（[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md)）与文档同步规则（`.claude/rules/doc-sync.md`），确立"文档即代码、ADR 不可变只取代"的防漂移流程。
- 工程化补齐：版本来源与发布渠道（[ADR-0007](docs/adr/0007-versioning-and-release-channels.md) + 根 `VERSION`）、GitHub Flow 分支模型与 PR/Issue 模板、运维手册（[docs/OPERATIONS.md](docs/OPERATIONS.md)）与安全说明（[SECURITY.md](SECURITY.md)）、迭代技能补充（`publish-snapshot` / `hotfix` / `bump-dependencies`）。
- 可演进与静态检查：PRD 功能需求加"状态"列（计划 / 开发中 / 已交付@版本）作活路线图；CONTRIBUTING 增"文档如何长期演进"章节；新增代码静态检查规范（`.claude/rules/static-analysis.md` + 根 `.golangci.yml`）。
- 维护期操作手册：CONTRIBUTING 增"维护迭代周期"（工作项→技能路由 + 端到端循环）、ADR 实操指引（编号 / 何时写 / 取代示例）、文档冷热分层（高频 / 中频 / 低频 / 近乎不变）。
- 变更速查：CONTRIBUTING §10.1 加"一次变更各动哪些"表（feat/fix/重构/回滚/依赖/发版/快照/架构决策/开新阶段 → 动哪些文档与产物，含版本号 / ADR / 期数的变动频率），讲清"100 feat + 100 fix 也几乎不动期数"。
- 提交规范强化：git-commit 增两条强制约束——禁止阶段性词语（Phase/P0/MVP/Sprint/迭代，按功能点而非开发阶段描述）+ 最小提交粒度（独立可编译 / 只做一件事 / 不混 feat·fix·refactor），各附合格与不合格示例。
- 文档维护技能：新增 `update-docs`（纯文档工作：写 / 取代 ADR、原地更新架构 / API、修文档漂移、整理文档），并接入维护迭代周期路由表。
- 审计闭环修正：补 `.env.example` / `web/dist/.gitkeep`；新增 [ADR-0008](docs/adr/0008-config-soft-delete-and-effective-md5.md)（软删唯一键哨兵 + 有效配置 md5 取舍）；验证门权威判据改挂入库真源（PRD 验收 + 高风险区测试 + 组件测试绿），不再依赖不入库的实施计划；统一 agent 模块↔jar 命名；补测试分层、备份常态化与恢复演练、`govulncheck` 漏洞入口。
- 收尾：采用 [MIT 许可](LICENSE)；SECURITY 明确为内部项目、不对外接收漏洞报告；从 `.claude/rules` / `.claude/skills` 清除"M0 未落地"等过渡性措辞（稳态规则只陈述既定事实，过渡状态归 README 当前状态与 `.tmp` 计划）。
- ADR 导航：CONTRIBUTING §3.1 与 adr/README 补"ADR 保持稀少、现状看 ARCHITECTURE、取代修剪活跃集、不必通读、增长过快是滥写信号"说明。
- 功能规格：新增 `docs/specs/`（右尺寸 per-feature spec，单文件一功能 + `_template.md`）——非平凡功能开发前先写需求/设计/任务/验收；接入 `develop-feature`、CONTRIBUTING 文档地图与冷热分层（小改动免，PRD 与 spec 分工见 `docs/specs/README.md`）。
- PRD 分期去硬编码：§7 改为按主题描述各期 + 指向 §4「期」列为唯一来源，加 FR 不再需手改区间（与"状态列""ADR 编号"同理，消除双源）；并注明分期是少数粗粒度阶段、产品成熟后改按版本 + 功能组织，不会堆到上百期。
- 仓库骨架与最小可运行控制面落地：Go 控制面（chi + GORM）可起服连 MySQL、AutoMigrate、预置 prod/test 两环境并经 `GET /admin/v1/namespaces` 返回；分层 router→handler→service→repository，新增 render（统一响应/错误 + traceId）与 apperr（领域错误）两个叶子包；内嵌前端经根包 `//go:embed all:web/dist` + SPA 回退处理器提供。
- 前端骨架：Vite + React + TypeScript 空壳管理台 + apiClient（react-router + @tanstack/react-query）。
- 双端 agent 骨架：Kotlin/TabooLib 三模块 Gradle 工程（agent-core 纯 Kotlin、agent-bukkit 打包 BeaconAgent、agent-bungee 打包 BeaconAgentProxy），`gradlew build` 通过。
- 部署：多阶段 Dockerfile（node 构建前端 → go 内嵌编译 → 极小非 root 镜像）+ docker-compose（beacon + mysql，含健康检查与命名卷）+ `config.example.yaml`，`docker compose up` 可起全栈。
- 配置中心（无推送）：scope 覆盖链键级深合并内核（yaml/json/properties codec 键序稳定、标量覆盖/map 深合并/list 整替/null 删键、整体 md5 纳入 dataId 名）；config_item/config_revision/zone_assignment/audit_log 四表（软删唯一键用固定哨兵、content 经 GORM size 抽象不绑方言）；配置 CRUD/发布/历史/回滚/diff（事务内 item+revision+audit 原子，发布做格式/大小/解析与跨层 format 一致性校验），有效配置按覆盖链解析（收敛只看 md5）；`/admin/v1/configs` 全套端点；merge 穷举单测 + 真实 MySQL 全流程与四层合并集成测试。
- 注册/发现/健康 + zone 分配：`runtime.Registry`（内存 map+RWMutex、读返深拷贝、重复 serverId 按 lastHeartbeat 新鲜度守卫——故障换机不误杀）+ 单 goroutine 健康扫描（online→lost→offline，offline 保留）；agent 侧 register（解析回填 group/zone）/heartbeat/report/discovery（挂 X-Beacon-Token 中间件）；admin 侧实例列表/详情/下线、zone 指派 CRUD 与汇总（改派即时重算有效配置并刷新内存归属，长轮询唤醒留待 M3）；capacity/weight 顶层、metadata 自定义、无 canary；runtime 并发单测（-race）+ REST 与改派重算集成测试。
- 长轮询热更：`longpoll.Hub`（缓冲为 1 的 notify channel、按 serverId/namespace 唤醒）+ `config/effective` 长轮询入口"先注册 waiter 再算 md5、唤醒即重算比对"（变了 200、未变挂起、超时 304）；发布/回滚/软删/改派事务提交后按 scope 算最小受影响集合再唤醒（global→全 ns、group→查内存、zone→反查 DB、server/改派→单 serverId）；有效配置未分配回退 groupHint；Hub 并发单测 + 真实 MySQL 长轮询时序集成（立即返回/超时/唤醒/只唤醒受影响/改派热推）。
- 审计查询端点：`GET /admin/v1/audits`（按 namespace/action/targetType/targetRef/时间过滤 + 分页，时间倒序，返回 total+items），补齐管理台审计页所需后端；集成测试覆盖过滤/分页/排序。
- React 管理台：configs（过滤/新建/详情发布/历史/diff/回滚/软删）、instances（过滤 + 5 秒健康轮询 + 未分配高亮 + 下线）、zones（指派 CRUD + 汇总）、audits（过滤分页）、namespaces（列表/新建）；react-router 可深链 + @tanstack/react-query；纯 CSS 无新增依赖；经根包 `go:embed all:web/dist` + SPA 回退由单二进制同端口供 UI+API。深链刷新不 404、无头浏览器渲染确认 React 正常挂载。
- 双端 agent（Kotlin/TabooLib，五模块实现 ADR-0005）：agent-api（纯 Java8 只读 API：读有效配置 + 查发现）/ agent-core（零具体库依赖：HttpTransport·JsonCodec 接口 + BeaconApiClient 五调用 + 生命周期状态机 + 快照 + applier + 退避）/ agent-adapters（OkHttp + kotlinx，唯一碰具体库）/ bukkit·bungee 壳。生命周期 BOOTSTRAP→REGISTERING→RUNNING→DEGRADED 全异步不阻塞主线程；先点亮本地快照 fail-static、控制面挂不回退配置不阻断玩家；长轮询续杯（200 apply+report 续新 md5、304 续旧、连接失败退避重连）；插件主类 object+@Awake 不继承 JavaPlugin、身份缺失 fail-fast；okhttp/kotlinx 经 @RuntimeDependencies 运行期下载不打包、打包期 relocate 对齐其命名空间（产出 BeaconAgent / BeaconAgentProxy）。无 canary、有效配置无 version 代际号、register 的 capacity/weight 顶层、metadata 仅 map<string,string>。29 个单测（core 纯逻辑 + adapters 真实 codec 对假 transport）。真机 MC 端到端属 M6。
- 打包与端到端验收：多阶段 Dockerfile 全量构建 + `docker compose up` 起 beacon+mysql（M0/M4 已验）；新增 agent-e2e / agent-e2e-bungee 验收插件（以业务插件身份经 agent 纯 Java 只读 API `config().raw()/md5()/onChange()` 读配置并监听变更），用 TabooLib runServer/runBungee 自动下载并运行 Paper 1.20.4 + Waterfall 1.20（自动 EULA），双端跑通两条端到端时序——①首次接入（双端 instances online）②发布热更（业务插件经 API 读到 mode A→B→C，PUT 后约 3s 不重启 apply）——且审计可查（config.create/publish）。docs/OPERATIONS 增端到端验收跑法。

### 变更
- agent 包路径从 `com.beacon.agent` 迁移到 `top.wcpe.beacon.agent`（e2e 模块 `com.beacon.e2e` → `top.wcpe.beacon.e2e`）；Gradle group 同步为 `top.wcpe.beacon.agent`，第三方库经 `@RuntimeDependencies` 运行期下载不打包、其打包期/运行期 relocate 命名空间随之为 `top.wcpe.beacon.agent.lib.*`。**对 agent-api 消费方为破坏性变更**：业务插件需同步更新 `compileOnly` 坐标 group 与 `import`。并对齐 CoreLib 的 TabooLib 用法——bungee 改枚举式 `install`、双端 TabooLib 版本统一为 `6.3.0-afd75a7`、移除冗余的 `relocate("taboolib", …)`（jar 内 taboolib 命名空间不变）。
- 控制面默认 HTTP 端口由 `8080` 改为 `8848`（管理台与 agent 同端口）；web 开发代理、agent 默认 endpoint、`.env.example` / `docker-compose.yml` / `Dockerfile` 及运维文档同步收敛到 `8848`。
- 明确 P1 范围：心跳响应 `configDirty` 优化提示位有意不在 P1 实现、恒返 `false`（变更感知由长轮询负责，agent 不依赖），归档 P2；代码注释与 `docs/API.md` / `docs/PRD.md` 同步标注。
- 集成测试加 `//go:build integration` 标记与单测隔离：`go test ./...` 只跑单测、`go test -tags=integration ./...`（+ `BEACON_TEST_DSN`）才跑集成，杜绝集成被静默 `t.Skip` 误判"全绿"；运行方式见 `docs/OPERATIONS.md` §8，`testing-and-quality` 规则同步。

### 修复
- 审计来源 IP 恒空：config / zone / instance 三处审计此前未写 `client_ip`，现由 handler 提取请求来源 IP（X-Forwarded-For 首跳 → X-Real-IP → RemoteAddr）按既有显式传参约定透传至 service 审计写入，管理台"来源 IP"列可用（FR-7 / PRD §6⑧）。

> 第一期（MVP）首个正式版本：配置中心、服务注册/发现/健康/zone 分配、长轮询热更、React 管理台、双端 agent 全部落地并验收——集成测试真跑全 PASS（`-tags=integration` + MySQL），PRD §6 八条真机真集群逐条通过（秒级 apply / 回滚 / 三层合并 / zone 热推 / 健康 TTL / 杀控制面 fail-static + 重连 / 同 serverId 守卫 + 故障换机不误杀 / 审计可查），FR-1~8 已交付@v0.1.0。
