# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/) 与[语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布版本

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

> 第一期骨架已落地、控制面可起服并返回环境列表；配置中心（建/发布/版本/回滚/合并）已实现，热更推送与管理台等能力随后续里程碑推进，尚未发布正式版本。
