# 功能规格：RC/GA Maven SDK 发布

> 状态：已实现，待远端公开 · 关联 PRD：FR-183、FR-184 · 分支：master

## 1. 背景与目标

Beacon 的 GitHub RC/GA 资产已有不可变晋级流程，但 `agent-api` 与 `agent-kit` 未自动发布到远程 Maven 仓库。下游业务插件无法在 RC 阶段验证候选 SDK，也无法在 GA 阶段直接解析正式 SDK。

本功能为既有 P10 RC/GA 发布流程补齐两套 Maven 坐标：RC 发布候选版本，GA 发布正式版本。

## 2. 需求（要什么）

- RC workflow 创建 `vX.Y.Z-rc.N` 后，自动向受控 Maven releases 仓库发布：
  - `top.wcpe.beacon:beacon-agent-api:X.Y.Z-rc.N`
  - `top.wcpe.beacon:beacon-agent-kit:X.Y.Z-rc.N`
- GA workflow 在现有 GitHub Release 资产原样晋级成功后，自动发布：
  - `top.wcpe.beacon:beacon-agent-api:X.Y.Z`
  - `top.wcpe.beacon:beacon-agent-kit:X.Y.Z`
- 发布 URL、用户名、口令和可选签名凭据仅由 GitHub Actions Secrets 注入，名称复用既有 `BEACON_PUBLISH_*` 约定。
- 范围内：两个 SDK 的 Maven 坐标、RC/GA workflow、发布约束与运维说明。
- 不做（范围外）：发布 Agent 插件 JAR 到 Maven、引入 Maven Central Portal/Nexus Staging 专用 API、变更 GitHub 产品资产的 RC/GA 原样复制规则。

## 3. 设计（怎么做）

- 新 ADR 取代 ADR-0074 中「GA 不重新运行会生成产品字节的构建或发布任务」的绝对表述，仅允许 API/Kit 为不同的正式 Maven 版本坐标重新生成 Maven 制品；GitHub Release 产品资产仍由 `promote_ga.sh` 原样复制并校验 SHA-256。
- Agent Gradle 构建保留根 `VERSION` 为默认版本来源，并只允许发布 workflow 通过受限 Gradle property 覆盖 Maven publication 版本。
- RC workflow 从已创建的候选 tag 推导 `X.Y.Z-rc.N`，在远程发布任务中只发布 API/Kit。
- GA workflow 完成既有 `promote_ga.sh` 后，在独立 Maven job 中从同一 RC commit 发布 API/Kit 的 `X.Y.Z` 坐标。
- 发布失败必须使对应 workflow 失败；已发布 Maven release 坐标不覆盖、不删除、不重传。

## 4. 任务拆分

- [x] 为 Maven publication 版本覆盖增加回归验证。
- [x] RC workflow 发布带候选后缀的 API/Kit。
- [x] GA workflow 发布正式 API/Kit，并保持 GitHub 资产原样晋级。
- [x] 修复发布静态审计对 CRLF 工作流的兼容性。
- [x] 失败 E2E 也经脱敏扫描后归档诊断日志。
- [x] 文档同步：PRD、ADR、ARCHITECTURE、OPERATIONS、SDK、CHANGELOG。

## 5. 验收标准

- RC workflow 的 Gradle 发布命令显式使用 `X.Y.Z-rc.N`，且仅涉及 API/Kit。
- GA workflow 在 GitHub GA Release 公开后，显式使用 `X.Y.Z` 发布同一 commit 的 API/Kit。
- 两个 workflow 均只从 Secrets 读取发布地址与凭据；仓库中不出现 URL、账户、口令或私钥。
- `make release-test`、`make release-check RELEASE_RC_TAG=vX.Y.Z-rc.N` 与 `actionlint` 通过。
- 本地 API/Kit 发布到临时 `file://` Maven 仓库后，可验证 RC 与 GA 两组坐标、POM、主 JAR、sources JAR 和 javadoc JAR 均存在。
- 失败的 Agent E2E 只在敏感扫描成功后归档日志；真实 E2E 重跑通过，或以明确的外部环境证据由用户书面豁免。

## 6. 风险 / 待定

- Maven releases 仓库通常不可覆盖：RC 或 GA 发布中断后，不得重传同一坐标，必须先核验远端实际状态。
- GitHub Release 与 Maven 仓库不存在跨系统原子事务；GA Maven 发布失败会使 GitHub GA 已公开而 workflow 失败，需由运维按日志核验后决定是否重试。
