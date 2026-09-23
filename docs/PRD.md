# Beacon 第二版产品需求文档（PRD）

> 活文档（入库于 `docs/`，随需求变更同 PR 更新）。本文只管 **WHAT / WHY**：产品目标、需求边界、功能需求与验收。HOW 见 `docs/ARCHITECTURE.md`，前端交互见 `docs/UX.md`，版本路线见 `docs/ROADMAP.md`，演进规则见 `docs/CONTRIBUTING.md`。
>
> **第二版基线**：`v0.1.0` 到 `v0.19.x` 归入 Legacy 第一版探索期，历史保留但不再作为后续验收基准。第二版从 `0.20.x` 的 P0 规格冻结阶段开始。

## 1. 背景与目标

### 1.1 背景

Beacon 的第一版围绕配置中心、文件树、服务发现、健康检查和管理台原型逐步扩张，功能堆叠后出现路线混乱、页面不可维护、部分 FR 与当前业务目标脱节的问题。

第二版需要重新聚焦：Beacon 是一个用于串联多个 BungeeCord 与 Bukkit / Paper 子服的**集群调度中间件控制面**。它要用 Web 管理以前只能靠配置文件硬维护的环境、BC 集群、大区、小区、子服关系，并让业务插件通过本机 agent 能力完成健康调度、跨服通信、审计告警与故障排查。

### 1.2 目标

- 建立清晰的环境 / namespace / BC 集群 / 大区 / 小区 / 子服权威模型。
- 让 BC 与 Bukkit agent 只靠 Beacon 地址与 namespace token 自连接中控；namespace 由 token 推导，serverId 在待确认流程中分配。
- 通过 agent 首启身份文件绑定真实服务器，避免运维误改 serverId 导致区数据隔离出错。
- 让首次注册、换区、解绑、身份冲突都必须在后台可见、可审计、可人工确认。
- 让业务插件只依赖本机 `agent-api`，禁止直接 HTTP 调 Beacon。
- 基于 TPS、CPU、在线人数、连接、告警、容量和延迟生成健康值，供业务插件选择健康服务器安排玩家。
- 可视化玩家流、连接流、调度决策、跨服消息、异常链路和审计记录。
- 对连接明细和跨服消息 payload 做可追踪存储，并提供热 / 冷数据生命周期。
- 重做核心后台页面，让 1000+ 子服规模下仍能在 1920x1080、浏览器 100% 缩放下高密度可操作。

### 1.3 非目标

- 配置中心、文件同步、文件树预览不沿用 Legacy 旧原型继续堆改。Legacy 前端随 P1 工程化基建整体冻结（FR-138），三项能力 P7-P9 按第二版模型重新设计与验收。
- Beacon 不实现具体游戏玩法，例如经济、匹配、排行、传送规则、跨服看人 UI。
- 业务插件不得绕过 agent 直连 Beacon；控制面不是业务插件的运行时依赖。
- 默认禁止跨 namespace 调度、通信和 Agent 操作；跨 namespace 只能通过后台显式互通信任关系开放。
- 不用 agent 命令通道传大文件。命令通道只做控制面编排，数据面文件传输必须走专门的流式传输通道。
- 不建插件制品库 / 仓库，不做自动依赖解析与蓝绿切换；变更单载荷只来自黄金模板源与配置中心。

## 2. 角色

| 角色 | 说明 | 主要诉求 |
|---|---|---|
| 运维管理员 | 通过 Web 管理 Beacon 集群 | 看清全局健康、接入新服、分配区服、排查异常、处理告警 |
| 集群管理员 | 负责环境、BC、大区、小区和调度策略 | 保证 namespace 隔离、调度可信、扩区换区可审计 |
| 业务插件开发者 | 在 BC / Bukkit 侧开发业务插件 | 通过本机 `agent-api` 获取健康服务器、发送跨服消息、查询本地缓存事实 |
| Beacon agent | 运行在 BC / Bukkit / Paper 上的数据面组件 | 自注册、上报指标、接收控制指令、提供本机 API、控制面不可用时降级 |
| 审计与安全人员 | 查看操作、消息、payload 访问与跨域行为 | 所有高风险操作有原因、有记录、可追溯 |

## 3. 核心约束

- **namespace 是强隔离边界**：默认禁止跨 namespace 调度、消息、Agent 操作。跨 namespace 互通必须后台显式配置，并额外审计。
- **首次接入必须人工确认**：新 agent 注册后只能处于待确认状态，未确认、未分配区服前不可调度。
- **identityId 绑定稳定 serverId**：agent 首启生成唯一身份文件，namespace 由接入 token 推导，serverId 由控制面在首次确认时分配并绑定。换区必须走后台受控流程；FR-205 交付后 serverId 不允许原地修改。
- **业务插件只走本机 agent-api**：业务插件不得直接 HTTP 调 Beacon。agent 负责缓存、降级、限流与控制面隔离。
- **控制面 / 数据面分离**：Beacon 做编排、审计、调度决策和事实存储；游戏服继续承担玩家入口与业务执行。
- **连接与 payload 查询可控**：连接明细和 payload 可查，但默认不做大范围慢查询；payload 查看必须填写原因并写审计。
- **热 / 冷数据分层**：热库保存近期数据；2 个月以上默认归档到同实例独立 database / schema，配置预留独立归档 DSN。
- **页面面向 1000+ 子服**：核心列表必须支持搜索、分页、虚拟化或服务端筛选，禁止一次性渲染不可操作的大列表。

## 4. 功能需求（FR）

> 状态流转：`计划` → `开发中` → `已交付@vX.Y.Z`。第二版新增 FR 从 `FR-138` 开始，不复用 Legacy 历史编号。带页面的 FR 其页面骨架统一在 P2 以 mock 拍板（FR-172），FR 所在阶段负责接真与真机验收。

| 编号 | 能力 | 阶段 | 版本线 | 验收摘要 | 状态 |
|---|---|---|---|---|---|
| FR-138 | Legacy 前端整体冻结：`web/` 不再演进，第二版二进制仅嵌 `apps/web` 新管理台 | P1 | 0.21.x | 发布产物只嵌新管理台；`web/` 冻结边界写入 ARCHITECTURE；真机依赖旧功能时继续运行 v0.19 及更早版本并有文档说明 | 已交付@v0.21.0 |
| FR-139 | Agent 首启生成并持久化唯一 `identityId` | P1 | 0.21.x | 首启生成身份文件；重启不变；损坏身份文件不静默重生成 | 已交付@v0.21.0 |
| FR-140 | Agent 注册绑定：`identityId + namespace + serverId` 首次接入人工确认 | P1 | 0.21.x | 新 agent 进入 pending；管理员确认后转 active；确认、拒绝、重新申请、禁用、解绑入审计 | 已交付@v0.21.0 |
| FR-141 | 身份冲突、解绑、换区、禁用、重新绑定流程 | P1 | 0.21.x | Q3 占用需显式强制解绑；禁用 / 启用 / 解绑有状态机保护；换区完整工单在 P3 接真深化（FR-155），Q4 并发冲突可视化处置延后另立 FR-177 | 已交付@v0.21.0 |
| FR-142 | namespace / 环境 / BC / 大区 / 小区 / 子服权威模型与强隔离 | P1 | 0.21.x | namespace token 哈希落库；namespace_trust 单向授权 / 收回即时刷新；v2 权威表完成迁移 | 已交付@v0.21.0 |
| FR-143 | 未分配 Agent 后台分配到大区 / 小区 / 默认入口 | P1 | 0.21.x | approve 创建未分配 server；未分配 server 可批量首次分配到 zone / BC 集群；已分配直改返回 rezone_required | 已交付@v0.21.0 |
| FR-144 | Agent 1s 采样，Beacon 5s 批量入库基础指标 | P4 | 0.24.x | TPS、CPU、内存、在线、连接摘要按批入库；断连后可恢复；不阻塞请求主线程 | 已交付@v0.24.1 |
| FR-145 | 每连接明细采集、存储与查询 | P5 | 0.25.x | 支持按服务器、连接、时间范围查询；默认不扫全量；按日期表或等价分片存储 | 已交付@v0.25.2 |
| FR-146 | 调度决策记录：请求、候选、排除原因、最终选择、失败原因 | P4 | 0.24.x | 每次调度有 trace；可解释为什么选择 / 不选择某台服；失败可排查 | 已交付@v0.24.2 |
| FR-147 | 健康值模型：TPS、CPU、连接、告警、容量、延迟综合评分 | P4 | 0.24.x | 每台服务器输出健康分、等级、不可调度原因；权重可配置并可回放解释 | 已交付@v0.24.2 |
| FR-148 | 本机 `agent-api` 调度接口，业务插件禁止直连 Beacon | P4 | 0.24.x | 业务插件通过本机 API 取候选；Beacon 不可用时 agent 按缓存降级；直连 Beacon 不作为契约 | 已交付@v0.24.3 |
| FR-149 | 跨服消息追踪元数据：来源、目标、耗时、状态、链路 | P5 | 0.25.x | 能按消息 ID / 来源 / 目标 / 时间追踪消息链路；失败有原因 | 已交付@v0.25.2 |
| FR-150 | 跨服消息 payload 分日期存储，查看必须填写原因并审计 | P5 | 0.25.x | 默认只查元数据；payload 详情需权限 + 原因；查看记录写审计 | 已交付@v0.25.2 |
| FR-151 | 热 / 冷数据生命周期与归档库 | P6 | 0.26.x | 2 个月以上默认归档到同实例独立 database / schema；配置预留独立 DSN | 已交付@v0.26.1 |
| FR-152 | 冷查询路由：默认热库，显式包含归档后跨库查询 | P6 | 0.26.x | 页面默认快查热数据；勾选归档后可跨热 / 冷库查历史；慢查询有边界提示 | 已交付@v0.26.1 |
| FR-153 | 归档清理页面：预览、dry-run、归档校验、清理、审计 | P6 | 0.26.x | 清理前必须归档并校验；支持 dry-run；执行、失败、重试、删除都入审计 | 已交付@v0.26.1 |
| FR-154 | 运维总览 `/dashboard`：健康、玩家流、连接流、告警、调度概览 | P4 | 0.24.x | 1920x1080 一屏看到核心状态；健康与调度概览本期接真，玩家流 / 连接流与告警随 P5 补全 | 已交付@v0.24.4 |
| FR-155 | 集群管理 `/servers`、`/zones`：注册确认、身份绑定、区服分配、健康详情 | P3 | 0.23.x | 1000+ 子服可搜索、筛选、批量操作；详情不遮挡主流程；分配结果实时可见 | 已交付@v0.23.0 |
| FR-156 | 集群拓扑 `/topology`：BC / 子服链路、消息流、请求拓扑、异常链路 | P5 | 0.25.x | 能查看 BC 到子服、消息和请求拓扑；异常链路有明细和最近事件 | 已交付@v0.25.2 |
| FR-157 | 可观测页面：服务分析、命令观测、审计、事件告警贯通排查 | P5 | 0.25.x | `/service-analysis`、`/commands`、`/audits`、`/alert-events` 可相互跳转追踪同一问题 | 已交付@v0.25.2 |
| FR-158 | 系统设置：采样、保留期、归档、健康权重、namespace 信任关系 | P6 | 0.26.x | 所有保留期、归档策略、健康权重、跨 namespace 信任关系可配置并审计 | 已交付@v0.26.1 |
| FR-159 | 演示模式：前端产物内置 mock 数据，支持独立展示核心运维链路 | P2 | 0.22.x | demo 构建无需后端即可演示 Dashboard、集群、拓扑、可观测和设置核心链路 | 已交付@v0.22.0 |
| FR-160 | 配置中心 V2 权威模型：基于 namespace / BC / 大区 / 小区 / 子服的配置作用域与继承关系 | P7 | 0.27.x | 配置作用域与区服权威模型一致；能解释每个配置值来自哪一层；跨 namespace 配置不可串用 | 已交付@v0.27.1 |
| FR-161 | 配置中心 V2 编辑、校验、差异、版本与回滚 | P7 | 0.27.x | 支持结构化编辑、schema 校验、diff、版本历史和一键回滚；敏感值不泄露到日志与前端明文历史 | 已交付@v0.27.1 |
| FR-162 | 变更单模型：黄金模板源文件差异与配置变更组合为一单，影响预览、审批、撤回 | P9 | 0.29.x | 模板源文件差异与配置变更可绑成一个变更单；发布前预览目标服影响；审批、撤回、失败阻断入审计；跨 namespace 变更单被拒绝 | 已交付@v0.29.0 |
| FR-163 | 文件资产 V2：Agent 目录清单、文件哈希、大小、修改时间与搜索 | P8 | 0.28.x | 能按服务器查看插件目录 / 配置目录清单；支持搜索、分页、哈希比对；大目录不阻塞页面 | 已交付@v0.28.0 |
| FR-164 | 文件内容预览与安全边界：文本预览、二进制识别、敏感文件保护、权限审计 | P8 | 0.28.x | 文本文件可预览和 diff；二进制只展示元数据；敏感路径默认禁止查看；查看行为入审计 | 已交付@v0.28.0 |
| FR-165 | 交付数据面：流式 HTTP 传输、源清单扫描、哈希增量、目标本地备份 | P9 | 0.29.x | agent 命令通道只做编排；大文件走流式 HTTP；仅传输变更文件；覆盖前自动备份 | 已交付@v0.29.1 |
| FR-166 | 统一灰度编排引擎（载荷无关）：目标筛选、批次规划、人工推进门、暂停继续、紧急终止、自动熔断 | P9 | 0.29.x | 全量 / 大区 / 小区 / 单服混合筛选；每批生效并人工确认后才放行下一批；失败率或健康恶化超阈值自动暂停；批次进度实时推送 | 已交付@v0.29.1 |
| FR-167 | 变更单历史与整单回滚：任务、批次、单服状态记录与一键整单回滚 | P9 | 0.29.x | 可按变更单 / 批次 / 服务器查看与回滚；整单回滚 = 文件备份还原 + 配置版本回退 + 重新生效；页面刷新可恢复状态；回滚入审计 | 已交付@v0.29.2 |
| FR-168 | 交付能力统一权限与审计：配置、预览、变更单、payload 查看统一风险分级 | P9 | 0.29.x | 高风险操作需要权限、原因和二次确认；审计能串起配置编辑、文件预览、变更单发布、生效与回滚 | 已交付@v0.29.2 |
| FR-169 | RC 与 GA 发布收口：不可变 RC、同 commit 原样复制产品资产并核验 SHA-256 | P10 RC | v1.1.0-rc.1 → v1.1.0 | 发布提交与本地质量门已完成；最终 RC/GA 标签、跨平台资产与 SHA-256 原样复制校验须在同一提交由远端发布工作流完成 | 已交付@v1.1.0（待远端公开） |
| FR-170 | 「交付」大分类信息架构：与集群、系统并列的导航大域，聚合文件资产、配置中心、变更单与交付历史 | P2 | 0.22.x | 侧栏出现「交付」大分类；文件资产、配置中心、变更单、交付历史页面有明确挂载位；维护态旧入口不回流 | 已交付@v0.22.0 |
| FR-171 | 生效编排：批次内触发子服重启 / 配置热重载，采集生效结果与观察窗健康数据 | P9 | 0.30.x | 批次可配置生效方式（重启 / 配置热重载 / 仅推送）；`hot_reload` 只触发 V2 配置工件热更，普通文件与 JAR 仅落盘，含 JAR 应选重启；关服后超时未回归判生效失败并计入熔断；观察窗展示健康分、TPS、告警 | 已交付@v0.30.0 |
| FR-172 | 全量 mock 管理台：`docs/UX.md` 全部页面以演示模式实现并逐页评审拍板 | P2 | 0.22.x | UX.md §2 所有页面在 mock 数据下可点击、可演示；每页过 mockup 评审门并拍板留档；mock 覆盖空态 / 常规 / 超大量 / 异常；只依赖 API 契约草案，不接真后端 | 已交付@v0.22.0 |
| FR-173 | monorepo 工程化：pnpm workspace + Turborepo，`apps/`（server / agent / web / ui-wiki）+ `packages/`（ui / devmock / eslint-config / typescript-config）布局迁移 | P1 | 0.21.x | Go 迁 `apps/server` 且 go:embed、Makefile、CI、脚本全部打通；agent 迁 `apps/agent` 构建绿；`turbo run lint / test / build` 全仓一键；配套 monorepo 与前端栈 ADR 落地 | 已交付@v0.21.0 |
| FR-174 | 第二版 web 脚手架：`apps/web` 新建（Vite + React Router + TanStack Query + Zustand + react-i18next + MSW） | P1 | 0.21.x | 新台骨架可跑（路由 / 布局 / 主题 / i18n）；MSW 经 `packages/devmock` 双端可用（浏览器 + 测试共享 handlers）；服务器状态归 TanStack Query、客户端状态归 Zustand 的边界写入规范 | 已交付@v0.21.0 |
| FR-175 | UI 博物馆：`@beacon/ui` 提升 `packages/ui`，ui-wiki 提升 `apps/ui-wiki`，控件展示覆盖率门禁 | P1 | 0.21.x | 每个 `@beacon/ui` 导出控件在 ui-wiki 有展示页（覆盖率检查纳入 CI 必过）；新管理台组件一律取自 `packages/ui`，不允许页面内私建通用控件 | 已交付@v0.21.0 |
| FR-176 | 静态检查最严档三线：TS strictTypeChecked、Go golangci 全量启用档、Kotlin detekt 全规则 | P1 | 0.21.x | `packages/eslint-config` 落 strict-type-checked + stylistic-type-checked 且新台零违例；`.golangci.yml` 改全量启用档（禁用项集中声明并注明原因）后端零违例；detekt 全规则（存量走 baseline）新代码零违例；三线全部进 CI 门禁；`static-analysis.md` 同步并配 ADR | 已交付@v0.21.0 |
| FR-177 | Q4 并发身份冲突可视化闭环：bootId 交替检测、保留实例 / 解绑处置 | P8 | 0.28.x | 并发双实例（同 identityId 交替 bootId）在冲突窗口内检测转 conflict；resolve-conflict 保留指定实例、落败方持续 409 并指引；冲突双方明细可视化；单向切换（故障换机）不误判 | 已交付@v0.28.0 |
| FR-178 | env 映射体验：env 增删改与 env→namespace 映射管理台 | P8 | 0.28.x | env 增删改、整体替换 env→namespace 映射与审计已落地；页眉 env 过滤已由 FR-213/214 接入服务端权威观测范围与 env→namespace 级联，失效 scope fail-closed、不回退全量 | 已交付@v1.1.0（待远端公开） |
| FR-179 | 管理台登录鉴权：登录页、令牌注入、401 处理、登出 | P4 | 0.24.x | 登录页（新 SaaS 设计，过 mockup 评审门）用户名/口令换 `/admin/v1/auth/login` 令牌，存 localStorage 持久；所有 `/admin/*` 带 `Authorization: Bearer`；路由守卫未登录跳登录页并登录后回跳原路径；任意 401 / 令牌过期清令牌跳登录（无自动 refresh）；登出 `/admin/v1/auth/logout` 清令牌；真机浏览器登录后 FR-155 接真页真数据可用、登出回登录页。单 admin 凭据，不含 API-key 登录 / RBAC / 记住我 / 2FA。全站接真页真机可用前置 | 已交付@v0.24.0 |
| FR-180 | 跨服消息广播寻址（namespace / zone 级 fan-out）与 agent topic 门面复活 | P5 | 0.25.x | `publish` / `subscribe` 原接口真实可用（业务插件零改动）；本 namespace 全部在线服与 zone 级定向 fan-out；可丢语义（只投在线、离线不补、TTL 过期）；广播追踪行含聚合送达 / 失败计数、列表可按广播过滤且不含 payload；跨 namespace 广播拒绝；真机广播场景可用 | 已交付@v0.25.3 |
| FR-181 | 连接明细与消息链路查询页（/connections、/messages） | P6 | 0.26.x | 两页挂可观测组；查询防护同 spec §4.3（精确 ID 直查，否则 serverId / playerUuid + 时间范围 ≤168h，未满足不发请求给引导空态）；游标分页；「包含归档」冷查询（FR-152）；消息详情含逐跳链路 / 关联消息 / payload 受控查看（原因必填先审计）；四态齐备；mockup 经用户浏览器评审后接真 | 已交付@v0.26.3 |
| FR-182 | 开发构建与发布准备标准化：PR 只跑质量门，master 全绿后生成 7 天临时产物，发布准备 PR 固定版本与变更说明 | P10 RC | v1.1.0-rc.1 → v1.1.0 | 根 VERSION、CHANGELOG 与质量门已按发布准备收口；远端 Actions Artifact、跨平台产品资产与公开 Release 仍须在同一提交执行 | 已交付@v1.1.0（待远端公开） |
| FR-183 | 通用不可变 RC：所有 SemVer 版本先发布 prerelease 候选 | P10 RC | v1.1.0-rc.1 | 远端候选标签与不可变 GitHub 产品资产发布后不得移动、覆盖或补传；同时发布 `agent-api`/`agent-kit` 的 `X.Y.Z-rc.N` Maven 坐标 | 已交付@v1.1.0（待远端公开） |
| FR-184 | GA 原样复制：从最终 RC 复制产品资产并创建正式 tag | P10 RC | v1.1.0-rc.1 → v1.1.0 | 远端 GA 须与最终 RC 指向同一提交，并将 GitHub 产品资产逐项原样复制后核验；仅 `agent-api`/`agent-kit` 可为 `X.Y.Z` Maven 正式坐标重新生成制品 | 已交付@v1.1.0（待远端公开） |
| FR-185 | 稳定版在线更新收敛与关键链路修复：只自动发现 GA，兼容迁移旧 prerelease 设置并修复检查/进度缺口 | P10 RC | v1.1.0-rc.1 → v1.1.0 | 内置更新只消费 stable GA；旧 update.channel=prerelease 自动规范化为 stable，响应 channel 字段保留并固定为 stable；手动检查强制绕缓存；检查不得覆盖下载进度；前端按开关与周期低频轮询，RC 不得进入普通在线更新 | 已交付@v1.1.0（待远端公开） |
| FR-186 | 管理台侧栏图标轨折叠与动画：展开/折叠宽度过渡，折叠后保留图标轨，去掉侧栏底部重复身份 | 对齐中间版 | 0.31.x | 桌面展开约 232px、折叠约 60px 图标轨且有宽度动画；折叠态仅 logo+图标+tooltip 可导航；侧栏底部无操作人块；折叠钮贴侧栏右缘随宽动画；shell 测试绿；真机 Browser 点折叠/展开 | 已交付@v1.1.0（待远端公开） |
| FR-187 | 管理台双段页眉与身份收敛：对齐 ReClaude 式段 1 指标槽 + 段 2 工具条，身份只在页眉 | 对齐中间版 | 0.31.x | 页眉视觉两段；段 2 左环境过滤（FR-178 不回退）、右搜索/语言/通知 + 用户头像下拉（名/角色/登出）；真鉴权身份仅页眉一处；演示模式仍显 demo 徽标与场景切换；shell/auth 测试绿；真机可登出 | 已交付@v1.1.0（待远端公开） |
| FR-188 | 全局运维指标条真数据：段 1 固定展示控制面全局态并低频轮询 | 对齐中间版 | 0.31.x | 五项：控制面在线、Agent 在线数、待确认注册、未处理告警、进行中变更单；优先复用现有 list/status API 前端聚合，失败单项「—」不整条崩；15–30s 轮询不风暴；mock 与真机抽查量级不矛盾 | 已交付@v1.1.0（待远端公开） |
| FR-189 | 小屏抽屉侧栏：窄视口默认隐藏侧栏，汉堡打开抽屉导航 | 对齐中间版 | 0.31.x | `<md` 默认无占位侧栏；打开抽屉可导航，点遮罩或导航后关闭；与桌面图标轨态互不污染；真机或窄视口 Browser 验收 | 已交付@v1.1.0（待远端公开） |
| FR-190 | 站内开源许可页：侧栏底栏进入 /license 展示 MIT 全文 | 对齐中间版 | 0.31.x | 点底栏「开源许可」进 `/license`；页内 MIT 与版权；标题 `Beacon - 开源许可`；不进主导航 | 已交付@v1.1.0（待远端公开） |
| FR-191 | 第二段页眉与内容区视觉一体：去横线与半透明底 | 对齐中间版 | 0.31.x | 段 2 无 border-b、无毛玻璃异色底，与内容区同底；段 1 本轮不动 | 已交付@v1.1.0（待远端公开） |
| FR-192 | 环境选择改为截图式 Dropdown：无底触发器、淡入淡出、右侧类型徽标 | 对齐中间版 | 0.31.x | 替换 Select；切换 env 语义同 FR-178；菜单项左图标右类型徽标；当前项高亮；打开关闭有淡入淡出 | 已交付@v1.1.0（待远端公开） |
| FR-193 | 页眉全局搜索（命令面板 MVP）：Ctrl/Cmd+K + 导航跳转 | 对齐中间版 | 0.31.x | 页眉搜索可点；Ctrl/Cmd+K 打开面板；至少导航分组可键盘选择并回车跳转；Esc 关闭；真机可用 | 已交付@v1.1.0（待远端公开） |
| FR-194 | 页眉语言切换骨架：中/英 + localStorage 持久 | 对齐中间版 | 0.31.x | Globe 可选 zh-CN / en；刷新保持；缺键 fallback 不白屏；壳层文案可辨识英文化 | 已交付@v1.1.0（待远端公开） |
| FR-195 | 页眉通知入口：未处理告警角标 + 下拉摘要与一键处理 | 对齐中间版 | 0.31.x | 铃铛启用；openAlerts>0 角标；下拉最近未处理摘要（健康流转 i18n）；一键确认/已处理；点查看全部进 /alert-events；空态与失败不静默 | 已交付@v1.1.0（待远端公开） |
| FR-196 | 页眉刷新当前页：仅失效当前路由相关查询 | 对齐中间版 | 0.31.x | 刷新按钮可点；不整页 reload；按当前页 queryKey 重拉；URL/筛选尽量保留；换路由后作用域跟随 | 已交付@v1.1.0（待远端公开） |
| FR-197 | 管理台视觉打磨：列表健康列收敛、焦点环克制、字体加载稳定、总览页眉补齐、表格/KPI/侧栏/登录细节统一 | 对齐中间版 | 0.31.x | 服务器健康列主信号 ≤2 个，不可调度改弱文案/tooltip；Input/Select/Textarea focus 为 ring-2 且透明度降低；demo 下 Geist 正常加载不被 MSW 误拦；运维总览 PageHeader 有 icon+职责说明；表头弱化+行 hover 更轻；KPI 副文案降一级；侧栏分组标签更清晰；状态墙「未分配」为 off 弱标；登录错误区固定高度不抖；ui-wiki 与 mock 管理台可目视验收 | 已交付@v1.1.0（待远端公开） |
| FR-198 | 管理台交互与超大量性能：主从详情点外部关闭；区服树 huge 默认不展开叶子；选服列表截断；huge 场景可 benchmark | 对齐中间版 | 0.31.x | 列表详情抽屉点外部/Esc 关闭（内层 dialog 不抢关）；健康/待确认 Sheet 无遮罩可点外部关闭；区服树默认仅展开集群+大区，小区叶子默认上限 40 且切 ns 重置展开；服务分析选服列表渲染上限 80；huge 场景区服首屏 DOM 与切 ns 可交互；zones/servers 相关 vitest 绿 | 已交付@v1.1.0（待远端公开） |
| FR-199 | LobbyCluster 权威模型与独立管理台 | P10 RC | v1.1.0-rc.1 → v1.1.0 | 每个 namespace 唯一一个独立 LobbyCluster；大厅成员仅限同 namespace Bukkit 且与大区/小区归属互斥；迁移受 drain 门保护；`/lobby-clusters` 先过 mockup 评审再接真；规格见 [lobby-cluster-authority](specs/lobby-cluster-authority.md) | 已交付@v1.1.0（待远端公开） |
| FR-200 | BC 全 namespace 受管目录与玩家首次大厅落脚 | P10 RC | v1.1.0-rc.1 → v1.1.0 | BC 注册 namespace 内全部受管后端；玩家首次进入只从 LobbyCluster 复用现有健康/容量调度器选择大厅；不拦截后续业务切服；控制面不可用时按最后有效快照降级；规格见 [bc-namespace-directory-and-lobby-entry](specs/bc-namespace-directory-and-lobby-entry.md) | 已交付@v1.1.0（待远端公开） |
| FR-201 | BC 受管目录立即重同步 | P10 RC | v1.1.0-rc.1 → v1.1.0 | 支持 namespace 全量与单 BC 重试；仅在线 BC 可执行；重建受管目录与大厅候选快照；命令状态、失败原因和审计可见；规格见 [bc-managed-directory-resync](specs/bc-managed-directory-resync.md) | 已交付@v1.1.0（待远端公开） |
| FR-202 | BC 受管服务器查询命令 | P10 RC | v1.1.0-rc.1 → v1.1.0 | `/beacon servers [页码]` 分页查询受管目录，`/beacon server <serverId>` 查询单服详情；显示大厅/大区/小区归属、在线、健康、可调度与同步摘要；规格见 [bc-managed-server-query-command](specs/bc-managed-server-query-command.md) | 已交付@v1.1.0（待远端公开） |
| FR-203 | Agent 极简身份接入与既有绑定兼容迁移 | P10 RC | v1.1.0-rc.1 → v1.1.0 | 新安装本地必填仅 Beacon 地址与 namespace token；namespace 由 token 推导、角色自动识别、serverId 待确认时分配；旧 Agent 自动导入匹配绑定并保持 active；规格见 [agent-minimal-bootstrap-and-identity-migration](specs/agent-minimal-bootstrap-and-identity-migration.md) | 已交付@v1.1.0（待远端公开） |
| FR-204 | Agent 地址探测、BC 多 listener 与面板覆盖 | P10 RC | v1.1.0-rc.1 → v1.1.0 | Bukkit 保留单地址；BC 上报全部 listener 并保留首个有效地址兼容旧契约；控制面生成探测地址，管理台支持逐 listener 覆盖并展示探测/覆盖/生效来源；规格见 [agent-address-detection-and-proxy-listeners](specs/agent-address-detection-and-proxy-listeners.md) | 已交付@v1.1.0（待远端公开） |
| FR-205 | 稳定业务标识与可变显示名称 | 待排期 | 待排期 | namespace、env、BC 集群、大区、小区与服务器均具备不可修改的业务 code/serverId 和可重复的 displayName；旧技术名称原值冻结迁移；规格见 [stable-business-identifiers-and-display-names](specs/stable-business-identifiers-and-display-names.md) | 已交付@v1.1.0（待远端公开） |
| FR-206 | 人类与机器主体及语义能力授权 | 待排期 | 待排期 | 登录用户、API key 与 MCP client 统一映射主体；人类可提交并审批自己的请求，所有机器主体在服务层永久禁止审批；规格见 [human-and-machine-principals-and-capability-authorization](specs/human-and-machine-principals-and-capability-authorization.md) | 已交付@v1.1.0（待远端公开） |
| FR-207 | 危险操作统一审批核心 | 待排期 | 待排期 | 高危操作先形成不可变审批请求，人类“批准并执行”，数据库持久化执行器可靠执行；固定 24 小时过期、全链路留档、失败关闭；规格见 [dangerous-operation-approval-core](specs/dangerous-operation-approval-core.md) | 已交付@v1.1.0（待远端公开） |
| FR-208 | 身份、凭据、信任与拓扑危险操作适配 | 待排期 | 待排期 | 身份解绑/禁用、凭据与信任授予、拓扑迁移等按后果接入统一审批；撤销信任/凭据、禁用身份等止损动作可直接执行并强审计；规格见 [identity-credential-trust-and-topology-dangerous-operations](specs/identity-credential-trust-and-topology-dangerous-operations.md) | 已交付@v1.1.0（待远端公开） |
| FR-209 | Agent 命令与敏感内容访问审批 | 待排期 | 待排期 | 任意 Agent 命令、实时日志、文件内容、敏感明文与 payload 访问必须经批准的执行许可，结果与证据完整留档；规格见 [agent-command-and-sensitive-content-approval](specs/agent-command-and-sensitive-content-approval.md) | 已交付@v1.1.0（待远端公开） |
| FR-210 | 控制面升级、回滚与系统设置审批 | 待排期 | 待排期 | 升级/回滚与高影响设置已接统一审批冻结、资产/备份哈希和设置 CAS；重启对账已实现（reconcileActivatingRestart）；真机完整验收 external-blocked；规格见 [control-plane-upgrade-rollback-and-settings-approval](specs/control-plane-upgrade-rollback-and-settings-approval.md) | 已交付@v1.1.0（待远端公开） |
| FR-211 | 配置、文件、覆盖集与交付审批适配 | 待排期 | 待排期 | 配置/文件写入、覆盖、删除、投递、灰度与回滚复用既有变更单状态机，通过统一审批内核发放单次执行许可，不发生双重审批；规格见 [config-file-override-set-and-delivery-approval](specs/config-file-override-set-and-delivery-approval.md) | 已交付@v1.1.0（待远端公开） |
| FR-212 | 全局审批中心 | 待排期 | 待排期 | 顶级 `/approvals` 页面集中查看、筛选、批准并执行、驳回和追溯危险操作；审批中心不受页眉观测筛选影响；规格见 [approval-center](specs/approval-center.md) | 已交付@v1.1.0（待远端公开） |
| FR-213 | 权威观测范围契约 | 待排期 | 待排期 | env/namespace 观测范围由服务端查询契约权威执行，所有列表、汇总、订阅与导出一致；失效或无权范围失败关闭且不改变写目标；规格见 [authoritative-observation-scope-contract](specs/authoritative-observation-scope-contract.md) | 已交付@v1.1.0（待远端公开） |
| FR-214 | 页眉 env→namespace 级联筛选 | 待排期 | 待排期 | 页眉先选 env 再选 namespace，可只观测线上或灰度资源；选项、URL 与刷新一致，非法保存范围失败关闭；规格见 [header-env-namespace-cascade-filter](specs/header-env-namespace-cascade-filter.md) | 已交付@v1.1.0（待远端公开） |
| FR-215 | 服务器归档与恢复 | 待排期 | 待排期 | 服务器可归档并从默认观测/调度移除，保留身份绑定与 BC/小区归属；恢复复用原事实且归档/恢复均按危险操作审批；规格见 [server-archive-and-restore](specs/server-archive-and-restore.md) | 已交付@v1.1.0（待远端公开） |
| FR-216 | namespace 归档与恢复 | 待排期 | 待排期 | namespace 归档后对整棵子树产生有效停用而不改写子资源自身状态，恢复后按原状态重现；两步均审批并留档；规格见 [namespace-archive-and-restore](specs/namespace-archive-and-restore.md) | 已交付@v1.1.0（待远端公开） |
| FR-217 | 服务器永久删除与墓碑 | 待排期 | 待排期 | 归档服务器经批准后执行逻辑永久删除，保留不可复用 serverId、审计与历史关联墓碑，不提供冷却期或复活；规格见 [server-permanent-deletion-and-tombstone](specs/server-permanent-deletion-and-tombstone.md) | 已交付@v1.1.0（待远端公开） |
| FR-218 | namespace 永久删除与子树墓碑 | 待排期 | 待排期 | 归档 namespace 无额外冷却期，经批准后在单次原子操作中墓碑化权威子树；预览完整影响范围，所有业务标识永久不可复用；规格见 [namespace-permanent-deletion-and-tombstone](specs/namespace-permanent-deletion-and-tombstone.md) | 已交付@v1.1.0（待远端公开） |
| FR-219 | 内置 `/admin/v2/mcp` 与 OAuth Client Credentials | 待排期 | 待排期 | Beacon 进程内提供远程 Streamable HTTP MCP，公网仅经 TLS 反代访问；独立 OAuth 客户端短令牌、受众绑定、撤销/轮换可用；HTTPS 反代 + OAuth + MCP 初始化 + 吊销 + audience 隔离真机验收通过；规格见 [built-in-admin-v2-mcp-and-oauth](specs/built-in-admin-v2-mcp-and-oauth.md) | 已交付@v1.1.0（待远端公开） |
| FR-220 | MCP 显式领域工具与审批交接 | 待排期 | 待排期 | MCP 仅暴露显式领域工具；低风险按能力直执，高风险只创建审批请求并返回 ID；机器可查询/撤回自己的请求；审批决定工具默认不暴露，仅在显式开启 `allow-approval-decide` 时对 automation profile 放行（见 FR-223）；规格见 [mcp-domain-tools-and-approval-handoff](specs/mcp-domain-tools-and-approval-handoff.md) | 已交付@v1.1.0（待远端公开） |
| FR-221 | 拓扑建树 MCP 工具（feat，增强 FR-219/FR-220）：补齐 MCP 侧区服结构维护能力——`beacon.topology.bc-clusters.create/update/delete`、`regions.*`、`zones.*` 共九个工具，语义与既有 `/admin/v2` HTTP 端点逐一对齐。与分配/换区等高风险动作**刻意不同**：建树是低风险结构操作，按 FR-220 的「低风险按能力直执」原则直接执行并写审计，不走审批票据。observer profile 不暴露写工具 | 待排期 | 待排期 | 经 MCP 可创建/改名/删除 BC 集群、大区、小区；删除非空节点按既有约束拒绝；写操作落审计；observer 仅可见读工具；规格见 [topology-authoring-mcp-tools](specs/topology-authoring-mcp-tools.md)。**真机验收通过**：MCP `tools/list` 可见 9 个建树工具；实调 `bc-clusters.create`→`regions.create`→`zones.create` 三层结构建成并直接生效（非审批），`zone-tree.get` 回读结构一致 | 🔨 开发中·真机验收通过（待发版） |
| FR-222 | 内部信任通道与机器注册（feat，增强 FR-219）：新增 `mcp.allow-machine-register` 开关（默认 false，公网部署必须保持关闭）。开启后，持 `X-Beacon-Token` 共享 token 的受信内部调用方经 `POST /beacon/v1/agent/register` 提交的 agent 身份**直接置为 active 并完成绑定**，跳过 FR-220 的人工审批流；关闭时行为与现状完全一致（一律进 pending 待人工确认）。无论开关如何，机器注册均写强审计并记录调用来源 | 待排期 | 待排期 | 开关开启时：携共享 token 注册的 agent 直接 active 且绑定指定 serverId，审计可查；开关关闭时：同一请求仍落 pending 待审批（行为不变）；缺/错 token 一律 401；规格见 [internal-trust-channel](specs/internal-trust-channel.md)。**真机验收通过**：①开启开关 + 弱 token（出厂默认值）时启动期 fail-fast 拒绝（「agent-token 必须改为强随机值」）；②强随机 token 下携 `X-Beacon-Token` 注册**直接 active 并绑定**（响应回带 `machineRegistered:true` + `identityId` + `boundAt`）；③缺 token / 错 token 一律 401 拒绝 | 🔨 开发中·真机验收通过（待发版） |
| FR-223 | 审批决定工具与闭环自动化（feat，增强 FR-220，**归真项**）：FR-220 原定「机器无任何审批工具」，实操中内网单操作者部署无法闭环（每次分配都需人工到管理台点击）。现新增 `beacon.approvals.approve` / `beacon.approvals.reject`（拒绝须给理由），**仅 automation profile 可见**（observer 不可见），并新增 `mcp.allow-approval-decide` 开关（默认 false）控制是否放行。默认关闭时审批决定权仍归人类，保持原分权设计；内网单操作者部署可显式开启以打通自动化闭环。批准与拒绝均写强审计，批准后由 approval worker 执行领域动作。**真机验收通过**：`allow-approval-decide` 开/关两态对比 `tools/list`，工具集差集**精确为** `beacon.approvals.approve`/`reject` 两项（78→76），且 FR-221 建树工具不受该开关影响 | 待排期 | 待排期 | 开关关闭时 automation 调用审批工具被拒（分权不破）；开启时 automation 能闭环批准自己提交的申请且审计可查；observer 任何情况下不可见审批工具 | 🔨 开发中·真机验收通过（待发版） |
| FR-224 | MCP 客户端管理台页（feat，增强 FR-219）：管理台新增 `/mcp-clients`，承载 MCP OAuth 客户端的日常运维——清单（名称 / profile / secret 前缀与版本 / 状态 / 创建时间）、profile 能力说明、创建 / 轮换 / 启用 / 吊销四个生命周期动作，以及 MCP 入口部署配置的只读查看。创建 / 轮换 / 启用沿用既有 `POST /admin/v2/mcp-clients*` 审批申请端点（202 + 审批票据，明文 secret 仅在首次响应出现一次），吊销沿用直接止损动作；新增 `GET /admin/v2/mcp/config` 只读端点暴露启用状态、公网基址、信任边界与两个开关（启动项，无写入端点，不回显任何凭据）。本次一并修订 [built-in-admin-v2-mcp-and-oauth](specs/built-in-admin-v2-mcp-and-oauth.md) 原「不新增独立管理页面」的决定 | 待排期 | 待排期 | 客户端清单可见且四动作闭环：创建 / 轮换返回一次性明文 secret 且重放不静默，吊销二次确认后立即生效；配置卡在未启用时展示配置指引而非报错；空 / 常规 / 超大量 / 异常四态齐备；页面不引入新交互模式。**真机验收通过（后端与生命周期闭环；UI 视觉态未浏览器实测）**：以 HEAD 构建的 Beacon 实测（原隔离实例的二进制早于 FR-224，不含 `mcp/config` 端点，不可用于本项）——①`GET /admin/v2/mcp/config` 在未启用态正常返回 `enabled=false` + `publicBaseUrl`/`trustedProxyCidrs`/`allowedHosts`/`allowApprovalDecide`/`allowMachineRegister`/`directMode`（非报错，合「展示配置指引」）；②`/mcp-clients` 页面 200；③四动作闭环：create→202 + 一次性明文 `clientSecret`，rotate→新 secret 且 `secretVersion` 1→2，enable（revoked→active）→202 + 票据，revoke→即时 200；④同 `Idempotency-Key` 重放 create 返回同一 clientId（不静默再建）；⑤清单仅回显 `secretPrefix`、不回显全量 secret；enable 仅对 `revoked` 客户端有效（对 active 正确回 404）。**UI 浏览器实测通过**（HEAD 构建的 Beacon，headless 渲染）：登录→`/mcp-clients`（未登录时路由守卫跳登录页，登录后到达）；页头「MCP 客户端」+ 功能描述「OAuth 客户端清单、profile 能力说明、创建 / 轮换 / 启用 / 吊销」；配置卡在 `mcp.enabled=false` 时展示**配置指引而非报错**（「MCP 入口未启用，外部 Agent 无法连接。在配置文件设置 mcp.enabled 并完成公网基址与可信反代配置后重启控制面」+「这些是启动项…管理台不提供写入」+「文档路径：docs/OPERATIONS.md §9」）；统计卡 `客户端总数 1 / 生效 0 / 已吊销 1`；清单表（名称 / profile / Secret 前缀 / 版本 / 状态 / 创建时间）含行 `fr224-client｜自动化｜mcs_0EmPfvbj…｜v2｜已吊销｜2026/9/21 14:38:55`，**仅回显 `secretPrefix`、不回显全量 secret**。说明：headless 浏览器缺 CJK 字体致截图中文呈方块，属渲染环境限制、非页面缺陷——内容以 DOM 文本为准，截图仅验版式；「空态」另由后端空清单（`{"items":[]}`）确认 | 🔨 开发中·真机验收通过（待发版） |
| FR-225 | 入口 / 登录服作为 LobbyCluster 成员（feat，增强 FR-200 / **ADR-0075** / **ADR-0083**）：`login-1/2` 这类入口服**作为大厅集群成员**纳入既有单一调度器（显式指派，走 ADR-0075 的归属迁移与排空门；前置条件 **`kind=backend`**）；多入口分流由既有共享排序契约实现，**不新增入口服列表 / 轮询等第二落脚真源**（ADR-0075 明文禁止）；**`is_entry` 本期不做**（ADR-0083 决策 3）。**实施并入待办段 A1 / A2，不单列开发轨道** | 待排期 | 待排期 | 入口服可作为大厅成员被指派且大厅 `ready`；BC 首次落脚只经既有共享排序契约，无第二真源；`isDefaultEntry` / `zoneDefaultEntry` 语义未变；规格见 [entry-server-as-lobby-member](specs/entry-server-as-lobby-member.md)、决策见 [ADR-0083](adr/0083-entry-server-as-lobby-member.md) | ✅ 已落地（并入 A1/A2/A4：`/zones` 未分配篮可直接指派大厅成员 + `/lobby-clusters` 迁入迁出 + 拓扑大厅层可视；ADR-0083 已接受）。**待办段 A3「入口服实体 + 多入口轮询」经判定由本 FR / ADR-0083 吸收，不另立项**：A3 的目标（多入口、单入口不可用自动切换、拓扑可辨识）已由「入口服 = 大厅成员 + 共享排序契约 + ADR-0083 决策 3 的可辨识性」达成；A3 原稿的「入口服列表 + 轮询/算法排序」正是 ADR-0075 点名禁止、ADR-0083 备选方案 A 已否决的第二落脚真源。故障切换已由 `TestDecideLobbyMultiEntryFailover` 实测守护（失联入口自动切备用、全挂则 `no_candidate` 且不回退小区） |
| FR-226 | Agent 上报服务器工作目录（feat，增强 FR-208）：agent 在注册 / 心跳中上报其所在服务器的工作目录绝对路径；控制面存储并在身份 / 冲突视图回显，供运维分辨"哪台、哪个目录" | 待排期 | 待排期 | agent 上报 workDir 且控制面存储；身份 / 冲突视图可见目录 + `lastAddr` + `serverId` + `bootId` + 注册时间；旧 agent 未上报时降级为空；规格见 [agent-server-workdir-report](specs/agent-server-workdir-report.md) | ✅ 真机验收通过（2026-09-22：换新 agent jar 后 serverWorkDir 五项回显实测） |
| FR-227 | 服务器键值标签（feat）：server 支持 `key=value` 标签，与 FR-29 的 `tag.<key>=<value>` 发现过滤口径对齐；可增删、可在资产 / 拓扑展示、列表可按 tag 筛选、写操作落审计 | 待排期 | 待排期 | 可给服务器加 / 删 `key=value`；资产 / 拓扑展示标签；列表可按 tag 筛选；写操作落审计；规格见 [server-key-value-tags](specs/server-key-value-tags.md) | ✅ 真机验收通过（2026-09-22：真控制面 + 真 agent 实测下发与写闭环，另含验收发现的两处缺陷修复） |
| FR-228 | Agent 上报容量与在线人数（feat，增强 FR-32 健康打分）：agent 上报 `capacity`（最大人数）与 `playerCount`（在线），使健康因子 `capacity` 由 `applicable:false`（"不适用"）转为参与打分；**`conn` 维持 FR-32 母规格的 proxy 口径不变**（原稿「backend 饱和度」与母规格冲突且会与 `capacity` 重复计分，已否决——见规格 §7） | 待排期 | 待排期 | 健康详情 `capacity` 为 `applicable:true` 且参与打分；旧 agent 未上报时仍降级为"不适用"；`conn` 对 proxy 适用、backend 不适用（口径不变）；规格见 [agent-capacity-and-playercount-report](specs/agent-capacity-and-playercount-report.md) | ✅ 已落地（`capacity` 与 `conn` 语义定案 + 前端健康详情区分「角色不适用 / 未上报」，穷举单测 + 接线测试守护） |
| FR-229 | 告警批量处理（已读 = 已处理）（feat，**增强 FR-157 / FR-89**）：告警已落库（`alert_event`，ADR-0041）且已有处理工作流（`status` open/acknowledged/resolved + `handled_*`，ADR-0064）与管理页。本项只补三处缺口：①术语对齐「已读 = 已处理」（收敛到既有三态，**不新增第四态**）②**按当前筛选**一键批量（现状批量只作用于当前页勾选行，**批量端点缺失需补**）③补批量审计 | 待排期 | 待排期 | 按筛选一键后**跨页**全部命中条目变更；命中数与实际一致、重复执行幂等；落一条批量审计；UI 上"已读 / 已处理"不再指代不同状态；规格见 [alert-read-and-handle](specs/alert-read-and-handle.md) | ✅ 真机验收通过（2026-09-22：真控制面 + 真 agent 实测下发与写闭环，另含验收发现的两处缺陷修复） |
| FR-230 | 告警详情关联服务器近期状态与时间线（feat，增强 **FR-157 / FR-89**）：在既有 `/alert-events` 详情面板内嵌"该服近期状态（健康真源）+ 该服告警时间线（查 **`alert_event` 表**，建议最近 20 条 / 近 24h）"，供运维判断"已过去 / 需立刻处理" | 待排期 | 待排期 | 详情可见该服近期状态与 ≥1 条时间线（数据来自 `alert_event` 而非内存环）；有界查询命中索引；无 `serverId` 的集群级告警按默认退化；规格见 [alert-detail-server-context](specs/alert-detail-server-context.md) | ✅ 真机验收通过（2026-09-22：真控制面 + 真 agent 实测下发与写闭环，另含验收发现的两处缺陷修复） |
| FR-231 | 告警分级与人工升降（feat，增强 **FR-157 / FR-89**）：`alert_event.level`（`info`/`warning`/`critical`）**已存在**；本项补**判定矩阵**（健康级别 × 角色：`offline`/`lost` 的 proxy/entry/lobby → `critical`、backend → `warning`；`degraded` 的 proxy/entry → `warning`、其余 → `info`）+ 按角色自动定级 + 人工覆盖列（`severity_override` / `overridden_by` / `overridden_at`）与审计；与 FR-232 合并时取最高级 | 待排期 | 待排期 | 矩阵穷举单测全通；人工改级后排序 / 筛选随之变化且审计可查；级别色 + 文字双编码；规格见 [alert-severity-grading](specs/alert-severity-grading.md) | ✅ 真机验收通过（2026-09-22：真控制面 + 真 agent 实测下发与写闭环，另含验收发现的两处缺陷修复） |
| FR-232 | 告警收敛与防堆积（feat，增强 **FR-89 / FR-157**）：收敛在 **`alert_event` 表**上做——同一 `(serverId, type)` 未恢复期间**只保留 1 行并计数**（新增 `occurrence_count` / `last_at`）、实例回 `online` 时**自动把 `status` 置为 `resolved`**、已处理条目再触发不回退 `open`。**不再提 ADR-0019**（其"告警不落库"已由 **ADR-0041** 取代） | 待排期 | 待排期 | 连续触发 5 次仅 1 行且计数为 5；恢复后自动 `resolved`；已 `acknowledged` 不回退；待办数有界；重启后保留（已落库）；规格见 [alert-dedup-and-auto-resolve](specs/alert-dedup-and-auto-resolve.md) | ✅ 真机验收通过（2026-09-22：真控制面 + 真 agent 实测下发与写闭环，另含验收发现的两处缺陷修复） |

## 5. 非功能需求（NFR）

- **规模**：面向 1000+ 子服、多个 BC 集群、固定大区 + 多小区结构；所有列表默认分页 / 筛选 / 虚拟化。
- **性能**：Agent 1s 采样，Beacon 5s 批量入库；写入与归档采用批处理，禁止请求主线程执行长耗时任务。
- **安全**：token、密码、payload 不写日志；payload 查看必须填原因；跨 namespace 行为必须有单独审计。
- **可用性**：控制面不可用时 agent 保留本地缓存与降级能力；存在最后有效大厅候选快照时不因控制面断连阻断玩家入口，未配置或无可用大厅候选时明确拒绝且不回退普通区服。
- **可审计**：注册确认、解绑、换区、调度、命令、payload 查看、跨域操作、归档清理都可追踪。
- **存储**：热库与归档库默认同 MySQL 实例不同 database / schema；配置预留独立归档 DSN，后续可迁出。
- **UI**：浏览器 100% 缩放、1920x1080 下优先保证单页高密度操作；不做大屏装饰、地图装饰或游戏化外观。
- **兼容**：1.0.0 前允许调整契约，但每个阶段必须写清迁移影响；1.0.0 后严格按 SemVer 执行。

## 6. 阶段验收

| 阶段 | 版本线 | 验收标准 |
|---|---|---|
| P0 | 0.20.x | PRD、路线图、核心边界确认；路线图 §5 规格清单与 API 契约草案全部完成 |
| P1 | 0.21.x | 工程化基建 + v2 控制面基础闭环：monorepo 布局迁移、全仓构建打通、静态检查最严档三线进 CI、UI 博物馆覆盖率门禁生效、apps/web 脚手架 + MSW 基建可跑、Legacy 前端冻结；Agent identity.yml、v2 注册确认、namespace/trust、区服权威表与首次分配链路可用 |
| P2 | 0.22.x | 全量 mock 管理台：UX.md 全部页面 mock 拍板、演示模式与「交付」大分类落地 |
| P3 | 0.23.x | 集群管理页接真深化：`/servers`、`/zones`、`/namespaces` 接入 P1 v2 基础 API，补齐换区工单 UI（解绑 + 重确认）、zone-tree 规模体验、draining / default-entry 页内可操作；Q4 冲突可视化与 env 映射延后（FR-177 / FR-178，已排入 P8） |
| P4 | 0.24.x | 基础指标、健康值、调度决策、本机 agent-api 形成闭环；`/dashboard`、`/service-analysis` 接真 |
| P5 | 0.25.x | 每连接明细、跨服消息追踪、payload 审计、异常链路可排查；`/topology` 与可观测页贯通接真 |
| P6 | 0.26.x | 热 / 冷归档、冷查询、归档清理可用且清理前必归档；系统设置页接真 |
| P7 | 0.27.x | 配置中心 V2 权威模型、编辑校验、版本管理接真完成 |
| P8 | 0.28.x | 文件资产 V2 资产索引、内容预览与安全审计接真完成；同期收编 P3 延后项 Q4 身份冲突可视化闭环（FR-177）与 env 映射体验（FR-178） |
| P9 | 0.29.x → v0.30.0 | 交付编排 V2 变更单、数据面、灰度生效编排、整单回滚与统一审计接真完成；v0.30.0 完成 FR-171 配置热重载发布验收并收口 P9 |
| 对齐中间版 | 0.31.x | 管理台壳层、FR-178 权威观测范围和 FR-213/214 级联已随 v1.1.0 本地发布验证收口；规格见 `docs/specs/admin-shell-redesign-0.31.md` |
| P10 RC | v1.1.0-rc.1 | 根 `VERSION=1.1.0`；FR-199～204 的大厅落脚、BC 运维与 Agent 极简接入闭环已完成；待远端工作流创建候选标签与不可变产品资产，候选公开后不得移动或覆盖 |
| GA | v1.1.0 | 待远端从最终 RC 同提交原样复制产品资产并逐项核验文件名、大小和 SHA-256；禁止 rebuild/repack/替换资产 |

### 6.1 P10 RC 新增能力验收

- **FR-199**：升级后每个 namespace 自动创建空 LobbyCluster，不从小区默认入口推断成员；空集群明确显示未就绪；成员新增、移除与互斥归属校验可验证，在线迁移未通过 drain 时返回 409；`/lobby-clusters` 覆盖空、常规、超大量、异常四态并经浏览器 mockup 评审。
- **FR-200**：真实环境至少启动 1 个 BC、2 个大厅服和 1 个普通区服；首次连接只落入健康且有容量的大厅候选，指标刷新周期内允许短暂倾斜；后续业务插件切服不被拦截；控制面断连继续使用最后有效快照，无候选时拒绝进入并记录中文 WARN；小区默认入口仍对外保留但不再决定 BC 首次落脚。
- **FR-201**：namespace 级操作只触发其全部在线 BC，单 BC 操作可定点重试；离线目标明确失败且不积压命令；执行后 BC 受管目录与大厅候选快照收敛，管理台可查询命令结果与审计。
- **FR-202**：受管服务器列表必须分页且不输出无界全量；摘要与单服详情准确反映 BC 当前目录、拓扑归属、在线、健康、可调度和最近同步状态；无权限与未知 serverId 返回明确错误。
- **FR-203**：新 Agent 仅凭 Beacon 地址与有效 namespace token 进入 pending；无须本地填写 namespace、serverId 或 address；管理员分配 serverId 后转 active；既有 Agent 升级时导入原绑定且 identityId 匹配则不重新审批；机器相关设置仍可使用安全默认与可选本地覆盖。
- **FR-204**：Bukkit 单地址与旧 API 保持兼容；BC 的全部 listener 可查询，首个有效 listener 回填兼容 address；每个 listener 可独立覆盖，未覆盖项使用请求来源 IP 与上报端口；待确认流程和服务器详情可区分探测值、覆盖值、生效值及来源。

### 6.2 待排期能力验收

- **FR-205**：新增资源与历史资源迁移后均同时具备内部数值 id、不可变 code/serverId 与可变 displayName；各层 code 按约定作用域唯一，displayName 可重复，运行寻址不依赖 displayName。
- **FR-206**：每次调用均可还原为唯一 human/api_key/mcp/system 主体与能力快照；人类可自提自批，任何机器主体调用审批接口均由服务层稳定拒绝并审计。
- **FR-207**：高危调用不直接产生业务副作用；批准动作原子签发一次性执行许可并可靠调度，重复提交不重放；24 小时过期、撤回、驳回、执行失败和证据均可查询。
- **FR-208**：危险目录覆盖身份、凭据、信任和拓扑动作且有自动覆盖检查；授权/启用/迁移走审批，撤销/禁用等止损动作直接执行并留下强审计。
- **FR-209**：Agent 命令、实时日志、文件内容、敏感明文和 payload 的所有入口在没有匹配执行许可时失败关闭；审批快照与实际目标、参数、结果完全可核对。
- **FR-210**：升级、回滚与高影响系统设置均可预览影响并由统一审批执行；失败不被误报为成功，审批记录可关联升级任务或设置版本。
- **FR-211**：既有变更单继续持有领域状态，统一审批只负责授权执行；创建人可自批，批准并执行不再要求二次启动，也不会叠加旧审批门。
- **FR-212**：`/approvals` 覆盖空态、常规、超大量与异常态，列表和详情可按状态/风险/来源/申请人/时间筛选；批准并执行、驳回、审计追溯经浏览器 mockup 评审后接真。
- **FR-213**：同一观测范围在列表、计数、趋势、SSE 与导出中得到一致结果；无效、越权或已删除范围返回明确错误，不回落全局，也不成为写操作目标。
- **FR-214**：env→namespace 级联选项只显示有权范围；刷新、深链和跨页保持选择，失效保存值触发失败关闭；切换观测范围不改写任何命令或表单的目标。
- **FR-215**：归档服务器立即停止调度并从默认列表隐藏，身份绑定与拓扑放置保持；经审批恢复后回到原事实，历史指标、审计和引用不断链。
- **FR-216**：归档 namespace 后所有后代有效停用但各自状态未被覆盖；恢复后只恢复此前可用的资源，归档/恢复预览和审批证据完整。
- **FR-217**：仅归档服务器可申请永久删除；批准后落不可逆墓碑，原 serverId 永久拒绝复用，所有历史引用仍能解析到删除摘要。
- **FR-218**：仅归档 namespace 可申请永久删除且无额外冷却期；批准前预览整棵权威子树，执行要么全部墓碑化要么全部不变，所有受影响 code/serverId 永久不可复用。
- **FR-219**：标准 MCP 客户端可在 `/admin/v2/mcp` 完成初始化、工具发现与调用；OAuth 客户端凭据不落明文，短令牌受众固定，撤销/轮换即时阻断后续换令牌，后端直连被部署门禁拒绝。
- **FR-220**：工具清单不存在通用 HTTP/SQL/文件代理；observer 只读，automation 低风险直执；每个高危工具只返回 approvalRequestId 并可轮询或撤回自己的请求；服务端默认无机器审批通路，仅在显式开启 `allow-approval-decide` 时向 automation 放行审批决定工具（FR-223）。
- **FR-221**：经 MCP 可创建、改名、删除 BC 集群 / 大区 / 小区；删除非空节点按既有约束拒绝；建树为低风险结构操作，直接执行并落审计，不产生审批票据；observer profile 不可发现写工具。
- **FR-222**：`allow-machine-register` 关闭时，携共享 token 的注册请求仍落 pending 待人工审批（行为与现状一致）；开启时同一请求直接置 active 并完成绑定，且强审计可查调用来源；缺/错 token 一律 401。
- **FR-223**：`allow-approval-decide` 关闭时 automation 调用审批决定工具被拒，分权设计不破；开启时 automation 可闭环批准/拒绝自己提交的申请，拒绝须给理由，两者均写强审计；observer 任何情况下不可见审批决定工具。
- **FR-224**：`/mcp-clients` 可见全部客户端及其 profile / 状态 / secret 版本与创建时间；创建与轮换返回一次性明文 secret（幂等重放不返回明文时给出重新申请提示，不静默），吊销为二次确认后的直接止损；`GET /admin/v2/mcp/config` 在任何启用状态下都返回 200、字段集合固定且不含任何凭据（agent token 不回显），未启用时页面展示配置指引而非报错；空 / 常规 / 超大量 / 异常四态齐备。

## 7. Legacy 策略

- `v0.1.0` 到 `v0.19.x` 作为第一版探索期冻结，称为 Legacy。
- Legacy FR、旧 P1/P2/P3 分期、旧页面原型不再作为第二版验收基准。
- 需要保留的能力必须在第二版重新立 FR、重新写规格、重新验收。
- Legacy 前端（`web/`）随 P1 工程化基建整体冻结，第二版管理台另起 `apps/web`；配置中心、文件同步、文件树预览在 P7-P9 重新设计、重新立规格、重新验收。
- 历史实现、CHANGELOG、ADR、spec 保留在仓库中，用于追溯，不再驱动新路线。

## 8. 术语表

| 术语 | 含义 |
|---|---|
| Beacon 控制面 | 独立运行的中控服务，负责注册、调度、审计、存储和后台管理 |
| agent | 运行在 BC / Bukkit / Paper 上的接入插件 |
| namespace | 强隔离边界，默认禁止跨 namespace 调度、消息和 Agent 操作 |
| env / 环境 | 面向运维展示的环境维度，可映射到 namespace 或 namespace 分组 |
| BC 集群 | 一组 BungeeCord / Waterfall / Velocity 代理节点 |
| 大区 | BC 集群下的逻辑区域 |
| 小区 | 大区下承载一批子服的调度单元 |
| 子服 | Bukkit / Paper 服务器实例 |
| identityId | agent 首启生成并持久化的唯一身份，绑定 namespace + serverId |
| serverId | 运维可读的服务器标识，必须与 identityId 绑定后才可信 |
| schedulable | 服务器可被调度，区别于 online；未确认、未分配、禁用、排空或不健康都不可调度 |
| agent-api | 业务插件调用的本机 API，负责调度候选、消息、缓存和降级 |
| 热库 | 保存近期高频查询数据的主业务库 |
| 归档库 | 保存冷数据的独立 database / schema，默认与热库同实例 |
| 变更单 | 一次交付的最小编排单元：黄金模板源文件差异与配置变更的组合，统一走影响预览、审批、灰度、生效与回滚 |
| 黄金模板源 | 被指定为载荷来源的运行中子服，插件与配置先在其上装好验证，再扫描差异生成变更单 |
