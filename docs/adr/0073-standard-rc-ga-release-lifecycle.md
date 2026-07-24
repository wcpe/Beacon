# ADR-0073：标准 RC/GA 发布生命周期与稳定更新链路

**状态**：已接受，发布流程部分被 [ADR-0074](0074-simple-rc-ga-release-flow.md) 取代（2026-07-20；关联 FR-182～FR-185）

> **当前阅读提示**：本 ADR 的通用 RC/GA 生命周期、开发构建不直接晋级与 GA-only 在线更新仍有效；受保护环境审批、分层准入、后置创建 tag 的强制编排及其复杂晋级约束，以 ADR-0074 的简化 RC/GA 流程为准。

## 背景

Beacon 当前同时存在三套不完全一致的发布语义：

1. 日常 `master` push 会移动 `dev` tag、覆盖 GitHub prerelease，并使用 `<基线>-dev.<提交距离>.g<sha>` 作为在线更新候选。
2. P10 为 `1.0.0` 单独恢复了不可变 `v1.0.0-rc.N`，要求最终 RC 与 GA 同 commit、同产品字节、同 OCI digest。
3. 正式 tag 与 RC tag 当前仍由人工预先推送，再由 workflow 被动响应；开发 Release 与 CI 又是并发触发，开发资产可能早于质量门发布。

这导致“开发态、候选态、正式态”边界模糊：`dev` 同时承担开发状态、Git tag、GitHub Release 和在线更新渠道；RC/GA 的不可变规则只对 `1.0.0` 特判；tag 的创建者不是完成门禁的 workflow；在线更新仍能消费 prerelease，并存在强制检查不强刷、检查状态覆盖下载进度、自动检查设置未真正生效等缺口。

项目需要一条适用于所有 major/minor/patch 的统一生命周期：开发构建不发布版本，任何版本先形成不可变 RC，验收通过后将同一字节晋级 GA，在线更新只消费 GA。

## 决策

### 1. `dev` 只表示开发状态，不再是发布对象

- 不再创建、移动或维护 `dev` Git tag。
- 不再创建开发态 GitHub Release 或 GitHub prerelease。
- 不再生成用于版本判新的 `X.Y.Z-dev.N.gSHA` 发布版本。
- 不再提供 `update.channel=prerelease` 的长期更新语义。
- 开发提交不得被用户在线更新自动消费。

`dev` 一词仍可出现在自然语言、分支说明或“开发态”描述中，但不能充当可安装版本标识、可移动引用或更新渠道。

### 2. 临时开发产物只使用 Actions Artifact

- PR 只运行质量门，不生成产品 Artifact。
- `master` 的质量任务通过后，按 Linux amd64、Linux arm64、Windows amd64、Darwin arm64 运行现有 `make package`。
- 临时产物以 source commit 与平台标识，上传到对应 GitHub Actions run，保留 7 天。
- Actions Artifact 不是 Release、不是候选版本、没有稳定 URL、没有在线更新资格，也不能被提升为 GA。
- 只有最终 CI 结果成功的 run 可作为开发验证来源；失败 run 中的部分 Artifact 不具备准入资格。

### 3. 所有 major/minor/patch 必须先 RC，再同字节晋级 GA

对任意目标版本 `X.Y.Z`：

1. 候选 tag 使用不可变 `vX.Y.Z-rc.N`，`N` 单调递增。
2. RC 发布后不得移动 tag、删除重建、替换产品资产或覆盖 OCI digest。
3. 候选失败或任何产品字节改变时，必须提交修复并创建 `vX.Y.Z-rc.(N+1)`。
4. 从该版本首个 RC 起，运行时版本、JAR/POM、版本化文件名和 OCI payload 均报告 `X.Y.Z`，不含 `-rc.N`。
5. GA tag `vX.Y.Z` 必须与最终 RC 指向同一 commit。
6. GA 只复制/提升最终 RC 的既有文件产品、Maven 暂存和 OCI digest；禁止重新编译、重新打包、重新构建镜像或重新生成产品字节。
7. 每个 major、minor、patch 均适用该规则，不允许 patch 直接跳过 RC。

ADR-0072 定义的不可变候选、同字节晋级、N-1、迁移、四平台、供应链和证据原则继续有效；本 ADR 将其从 `1.0.0` 专用流程推广为通用版本生命周期，并删除其中保留日常移动 `dev` 的例外。

### 4. 发布准备必须通过独立 PR

- 每个目标版本先创建独立发布准备 PR。
- 该 PR 只修改根 `VERSION` 与 `CHANGELOG.md`。
- 版本准备不得与业务功能、缺陷修复、重构或发布 workflow 改造混在同一 PR。
- PR 合并后，目标 commit 才能进入 RC 准入。
- VERSION、CHANGELOG、目标 tag 与候选 manifest 必须一致；不一致时阻断 RC。

### 5. RC/GA tag 由 workflow 在门禁通过后创建

- 操作者不得预先人工推送 `vX.Y.Z-rc.N` 或 `vX.Y.Z` 来触发标准发布。
- RC workflow 先以目标 commit 和版本输入运行准入门；全部通过后才创建并推送不可变 RC tag，再发布候选资产。
- GA workflow 先校验最终 RC 的稳定窗口、证据 manifest、commit、文件 hash、Maven deployment 与 OCI digest；全部通过后才创建 GA tag并完成 promotion-only。
- workflow 必须检测目标 tag 已存在、候选序号倒退、commit 不一致或资产漂移，并在任何写操作前失败。
- 最小权限、受保护 secret 和并发锁继续作为发布写权限边界；GitHub Environment 人工审批闸门已由 ADR-0074 取消。

### 6. 在线更新只自动消费 GA

- 标准在线更新候选只来自 `prerelease=false`、tag 精确匹配 `vX.Y.Z` 的 GA Release。
- RC 不进入自动更新候选；需要验收 RC 时必须通过显式 tag、manifest、hash 或 digest 安装。
- 开发 Actions Artifact 不进入在线更新候选。
- `update.channel` 收敛为 `stable`；历史配置值 `prerelease` 在读取/迁移时自动归一为 `stable`，不得导致启动失败或继续请求 prerelease。
- API、设置元数据、前端枚举、文档和持久化值必须使用同一口径。

### 7. 自动检查采用前端低频轮询，状态职责必须隔离

- 自动更新检查由已登录管理台前端低频轮询 `GET /admin/v1/system/update-check` 实现；后端不新增常驻定时调度器。
- `update.auto-check-enabled` 关闭时停止轮询；开启时按 `update.check-interval-hours` 计算轮询节奏，并设置合理最小频率，避免前端重渲染或多标签页造成请求风暴。
- 手动“立即检查”的 `force=true` 必须绕过检查缓存并重新请求远端 GA 元数据。
- 检查结果只能更新“检查状态/可用版本”，不得覆盖正在进行的下载、校验、应用或回滚进度。
- 下载/应用状态与检查状态分别建模；进行中的更新操作拥有更高展示优先级。
- 轮询失败必须脱敏并可观测，但不得阻断管理台其他功能，也不得把已有下载进度重置为失败检查状态。

## 与既有 ADR 的关系

1. **完全取代 ADR-0056**：不再存在移动 `dev` tag、提交距离开发版本和对应判新规则。
2. **取代 ADR-0052 的滚动 prerelease、stable/prerelease 双渠道和“最新 prerelease”消费决策**；其正式 GA 作为稳定发布的基本语义继续保留。
3. **部分取代 ADR-0072**：删除“保留日常 `dev`”及普通 prerelease 继续解析 `dev` 的决策；保留并推广不可变 RC、payload 固定正式版本、GA 禁止 rebuild、N-1、迁移、四平台、供应链与证据门。
4. **不复活 ADR-0046 的长期人工 tag 触发模型**：RC 名称继续使用 `vX.Y.Z-rc.N`，但 tag 由 workflow 在门禁后创建，而非人工预推。
5. ADR-0054、ADR-0055 中已被 ADR-0056 取代的开发版本规则随 ADR-0056 一并失效，仅保留历史记录。

## 实施分层

- **FR-182**：删除开发 tag/Release；PR 仅质量门；master 成功后生成 7 天四平台 Actions Artifact；规范 VERSION/CHANGELOG 发布准备 PR。
- **FR-183**：实现通用不可变 RC 构建、分层准入、workflow 后置创建 `vX.Y.Z-rc.N`。
- **FR-184**：实现最终 RC 到 GA 的 promotion-only、workflow 后置创建 `vX.Y.Z`、发布保护与回拉验证。
- **FR-185**：在线更新只消费 GA；旧 `prerelease` 自动迁移 `stable`；修复 force 检查、状态污染与 auto-check；以前端低频轮询落地。

在 FR-182～FR-185 全部完成前，仓库处于迁移期；任何尚未落地的决策必须在对应 spec 中标记，不能把提议状态描述成已可用能力。

## 理由

1. **开发态不再冒充版本**：短期 Artifact 足够支持开发验证，无需可移动 tag 和用户可见 Release。
2. **候选对象唯一**：每个 RC 都绑定唯一 commit、产品字节与证据，失败只能递增候选序号。
3. **GA 不引入新变量**：同字节晋级消除“RC 验过、GA 重建后变化”的供应链缺口。
4. **tag 所有权与门禁一致**：由完成校验的 workflow 创建 tag，避免人工先推 tag 后才发现门禁失败。
5. **更新面更安全**：生产实例只自动消费 GA，RC 与开发构建必须显式安装，不会因 GitHub prerelease 排序误装。
6. **职责更简单**：CI 负责质量与短期开发产物；RC 负责候选构建；GA 负责晋级；在线更新只负责稳定发布。
7. **自动检查无需后端常驻任务**：前端低频轮询复用现有 API 与设置，改动面小，同时可以直接避免检查状态污染下载状态。

## 后果

### 正面

- Git tag、GitHub Release 与在线更新候选均变为不可变、可审计对象。
- PR 不再产生容易被误用的产品包，master 开发产物自动过期，不占用 Release 命名空间。
- 每个版本都有一致的 RC/GA 过程，patch 不再成为绕过候选验证的例外。
- 操作者不能通过先推 tag 抢跑门禁；失败不会留下“看似已发布”的标准 tag。
- 在线更新只看 GA，减少渠道配置、版本比较和远端 Release 选择复杂度。
- 强制检查、下载进度与自动检查设置拥有明确且可测试的职责边界。

### 代价与约束

- 日常测试者不能再通过在线 prerelease 自动更新，需下载 7 天 Actions Artifact 或显式安装 RC。
- 所有 patch 也必须经历 RC，发布速度取决于分层验收策略和受保护 workflow。
- 发布 workflow 需要写 tag/Release 权限，必须依赖最小权限与并发锁严控；不再要求 GitHub Environment 人工审批。
- 前端轮询只在管理台会话活跃时执行，不等价于无人值守服务器后台定时检查；这是刻意选择，后台自动升级不在本决策范围。
- 旧 `prerelease` 配置必须兼容迁移，否则升级后可能因非法枚举失败。
- 历史 `dev` tag/Release 可保留为历史记录或由维护者另行归档，但任何清理操作不得由本 ADR 自动执行，也不得改写历史版本证据。

## 否决方案

### 方案 A：保留移动 `dev` Release，但让它等待 CI

仍然把开发态暴露为用户可见版本和在线更新候选，移动引用也无法形成稳定证据。否决。

### 方案 B：PR 与 master 都上传产品 Artifact

PR 产物未经合并，容易被误当成可发布候选，也增加存储和认知成本。PR 只跑质量门。否决。

### 方案 C：只要求 major/minor 先 RC，patch 可直接 GA

patch 同样可能包含安全、数据、兼容或发布链修复；按版本类型绕过不可变候选会让流程重新分叉。否决。

### 方案 D：人工创建 tag 后由 workflow 校验

门禁失败时标准 tag 已存在，删除重建又破坏不可变性。必须先验门、后由 workflow 创建 tag。否决。

### 方案 E：GA 从最终 RC 的同 commit 重新构建

同 commit 不保证同字节；工具链、依赖、时间戳和构建环境可能变化。GA 必须 promotion-only。否决。

### 方案 F：在线更新保留 stable/prerelease 两渠道

开发产物已不再发布，RC 又必须显式安装，`prerelease` 没有合法自动消费对象，只会保留歧义和误装风险。收敛 stable。否决。

### 方案 G：由后端常驻 scheduler 执行自动检查

需要新增生命周期、并发、持久化和关闭语义；当前目标只需管理台提示更新，前端低频轮询更直接。否决。

### 方案 H：检查与下载继续共用一个状态字段

检查请求可能覆盖下载/应用进度，导致用户看到回退或错误状态。必须分离检查状态与操作状态。否决。
