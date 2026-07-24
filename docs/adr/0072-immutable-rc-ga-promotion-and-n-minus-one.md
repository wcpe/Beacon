# ADR-0072：不可变 RC/GA 同字节晋级与 N-1 兼容准入

**状态**：已接受，发布流程部分被 [ADR-0074](0074-simple-rc-ga-release-flow.md) 取代（2026-07-20）。

> **当前阅读提示**：本 ADR 中的不可变 RC、GA 与最终 RC 同 commit、产品资产原样晋级仍有效；N-1、迁移、四平台、供应链、Maven Central、OCI、审批和证据门仅保留为历史背景或测试参考，不再是当前 RC/GA 的硬条件。当前流程以 ADR-0074 及 FR-182～FR-185 为准。

## 背景

Beacon 在 P1-P9 完成第二版 43 条核心 FR，并以 `v0.30.0` 收口交付编排。进入 `1.0.0` 前，需要一条同时满足候选稳定性、产物可追溯、N-1 兼容、数据可恢复和正式发布不可变性的准入路径。

现有预发布决策服务于日常开发：

- [ADR-0052](0052-rolling-prerelease-channel.md) 取消长期维护的语义化 RC，保留移动 `dev` 常规预发布渠道和 stable/prerelease 两个更新通道。
- [ADR-0056](0056-rolling-prerelease-dev-distance-version.md) 把日常开发版本固定为 `<基线>-dev.<提交距离>.g<短提交>`，解决移动 `dev` 下版本判新问题。

日常滚动预发布适合持续集成，但不适合作为 GA 候选：移动 tag 会被后续提交覆盖，无法建立“该候选经过了 43 FR、四平台、N-1、迁移和稳定窗口验证”的封闭证据链；若 GA 再从同一源码重新构建，又无法证明正式资产与已验证候选逐字节一致。

因此需要在**不取消日常 `dev`、不增加第三个在线更新渠道**的前提下，为 GA 稳定窗口新增一类不可变候选，并把 N-1、迁移、平台和证据要求提升为正式阻断门。

## 与既有 ADR 的关系

1. **部分取代 ADR-0052 决策 1 的“移除 rc 概念”**：只在 GA 稳定窗口内恢复不可变 `v1.0.0-rc.N` 候选；日常开发仍不维护语义化 RC 线。
2. **扩展 ADR-0052 的发布触发与渠道边界**：日常 `dev` 和 stable 两条在线更新语义不变；RC 通过显式 tag/manifest/hash/digest 安装，不新增 `update.channel` 枚举，也不由普通 prerelease 解析器消费。
3. **保留 ADR-0052 的 dev 常规渠道、移动 tag、判新与 stable 行为**；与本 ADR 冲突的仅是“任何场景都不再有 RC”这一绝对表述。
4. **ADR-0056 继续完整适用于日常 `dev` 版本**；它不定义 RC/GA payload。RC/GA 的版本与晋级规则由本 ADR 新增。
5. **不复活 ADR-0046 的长期 RC 模型**：本 ADR 的 RC 只服务于一次 GA 稳定窗口，payload 从第一次 RC 起就是 `1.0.0`，并要求 GA 原样晋级，不建立可滚动维护的 RC 产品线。
6. **扩展 ADR-0071 而不改变其正文**：ADR-0071 继续定义业务配置灰度生效；本 ADR 只把配置、热库、归档库和 Agent 本地状态纳入产品版本升级/回滚准入，不改变变更单内的灰度模型。

## 决策

### 1. 保留日常 `dev`，新增仅用于 GA 的不可变 RC

- `dev` tag 与 prerelease 更新渠道继续保留，版本继续使用 ADR-0056 的提交距离格式。
- GA 候选使用 `v1.0.0-rc.N`，`N` 单调递增。
- 候选 tag 发布后不可移动、不可强推、不可删除重建；候选产品资产与 OCI digest 不可替换。
- 候选失败或产品字节发生任何变化时，只能新建 commit 并发布 `v1.0.0-rc.(N+1)`。
- RC 不是第三个控制面在线更新渠道；日常 prerelease 解析器必须继续只解析 `dev`，RC 使用显式候选引用。
- GitHub 元数据中 dev 与 RC 都可为 `prerelease=true`，但普通 prerelease selector 必须同时要求 `tag_name=dev` 与 ADR-0056 开发版格式，并显式拒绝 `v1.0.0-rc.N`。RC 即使发布时间更新、语义版本更高也不得被选择；没有合法 dev 时不得回退 RC。

### 2. 从第一次 RC 起，所有 payload 版本均为 `1.0.0`

只有候选 tag 带 `rc.N`。以下版本面从首个 RC 起全部固定为 `1.0.0`，不得包含 `-rc.N`：

- 控制面运行时版本与版本端点。
- Bukkit/Bungee Agent JAR Manifest。
- `agent-api` / `agent-kit` Maven POM 与 JAR 版本。
- 所有带版本的产品文件名。
- 内嵌 Web 的版本展示。
- OCI image config、labels 与容器内控制面 payload。

这样 RC 与 GA 的可执行行为不存在由版本注入造成的差异，候选 tag 只表达“尚未完成 GA 准入”，不改变程序内版本。

### 3. RC 只构建一次，GA 禁止 rebuild

- 每个 RC 的候选流水线只构建一次完整产品资产，并记录文件名、大小、SHA-256、source commit、构建 run、Maven 坐标与 OCI digest。
- Maven `1.0.0` 产物在 RC 阶段进入不可变暂存仓；GA 只提升该暂存仓，不重新执行编译、打包或发布生成。
- OCI 在 RC 阶段生成生产索引与平台 digest；GA 只为同一 digest 增加稳定 tag。
- GA tag `v1.0.0` 必须与最终 `v1.0.0-rc.N` 指向同一 commit。
- GA 产品资产集合必须与最终 RC 相同，逐项文件名、大小和 SHA-256 相等；不得新增另一套可执行/可安装产物。
- GA 流水线只允许校验、复制/提升既有资产、增加稳定引用和执行发布后回拉 smoke。检测到编译、JAR/Web 打包、Maven 重新生成或 OCI build 即失败。
- 实现边界固定为：`prerelease.yml` 只构建移动 dev；`_build-release.yml` 只承载 dev/RC build；`release.yml` 改为 GA promotion-only；`Dockerfile` 只在 RC OCI 构建使用，GA 仅对同一 digest retag。
- GA 必须同时有 workflow 静态审计、job/日志禁 build 审计与故意注入构建动作的失败测试；不能仅靠代码评审声称“没有 rebuild”。
- 稳定窗口后生成的最终证据 manifest 是审计附件，不属于可执行产品资产；它可以在 GA 时新增，但不能替换候选产品资产。

### 4. 本次 N-1 固定为 `v0.30.0`

- 目标 N 为 `1.0.0`，本次完整 N-1 为 `v0.30.0`。
- V2 支持边界为 `v0.20.0+`；迁移说明必须明确来源版本与限制。
- `v0.19.x` 及更早版本属于 Legacy，排除原地升级、协议兼容、数据迁移和 N-1 准入。
- N-1 验收使用控制面 N/N-1 × Agent N/N-1 四格；每格同时覆盖 Bukkit 与 Bungee，Web 跟随控制面。
- 四格必须证明身份连续、注册/鉴权、指标/调度、连接/消息、资产/交付、配置与玩家链路 fail-static；若某格依赖恢复后的 N-1 schema，必须明确标记，不能冒充原地二进制回滚。

### 5. 升级面覆盖完整状态，迁移只前进

升级/回滚准入必须覆盖：Server、Bukkit Agent、Bungee Agent、内嵌 Web、热库、归档库、配置和 Agent 本地状态。

- 数据库采用 forward-only expand/backfill/contract；迁移可重入，不执行 down migration。
- 混合版本窗口优先使用 additive schema 与兼容读写。
- 若升级后 schema/config/local-state 与 N-1 不兼容，回滚模式必须标记为 `backup_restore`：停写，恢复同一 `backupSetId` 的热库、归档库、配置与受影响 Agent 本地状态，再部署 `v0.30.0`。
- 热库与归档库必须作为协调备份集合恢复；归档器在 schema 迁移和双库快照期间暂停，禁止只恢复一侧继续搬运。
- Agent `identity.yml` 不得因升级或降级静默重建；调度快照、交付备份 manifest、有效配置快照等本地状态必须纳入兼容/恢复证据。

### 6. 支持四个平台，并以生产 OCI digest 为部署事实

`1.0.0` 支持矩阵固定为：

- Linux amd64
- Linux arm64
- Windows amd64
- Darwin arm64

四个平台必须使用候选原生产物在实际 OS/arch 上完成控制面、内嵌 Web、Bukkit/Bungee 注册与基础业务 smoke，交叉编译成功不能代替运行验证。

生产容器以 Linux amd64/arm64 OCI 索引 digest 为不可变部署事实；RC 与 GA 必须解析到同一 index digest 和平台 manifest digest。

### 7. 43 FR、迁移、平台与证据成为无豁免阻断门

GA 前必须完成：

- P1-P9 全部 43 条核心 FR 在最终候选上的逐条复验。
- N-1 四格 4/4，Bukkit/Bungee 双端通过。
- `v0.30.0 → 1.0.0` 升级、原地二进制回滚（若声明支持）和备份恢复回滚。
- 热库、归档库、配置与 Agent 本地状态的一致性校验。
- 四平台 4/4 原生产物 smoke 与生产 OCI 按 digest smoke。
- 稳定窗口；窗口要求在首个 RC 前冻结，产品字节或阻断问题变化会重置窗口并要求下一 RC。
- 最终证据 manifest，记录产品资产、测试命令、43 FR、四格、迁移、平台、OCI、风险、审核与 `promotion.rebuilt=false`。
- R0/R1 风险为零。任一门失败都不得以口头批准、允许失败或手工改状态绕过。

### 8. 签名、SBOM 与 provenance 是产品准入证据，不进入运行时

- GitHub Release 产品资产与 OCI 默认采用 GitHub Actions OIDC + Sigstore keyless；签名身份绑定受信仓库/workflow/ref，验证显式给出 issuer、身份与信任根。生成/验证工具只存在于 CI/审核环境，不增加 Server、Web 或 Agent 运行时依赖。
- 每个产品 subject 固定生成 CycloneDX JSON 1.6 SBOM，以及 `in-toto Statement v1` + `SLSA Provenance v1`；SBOM/provenance 必须绑定 subject SHA-256 或 OCI digest并经发布位置回拉验证。
- 签名缺失、bundle 无法验证、签名者身份/issuer/信任根不符、SBOM 格式/subject 不符、provenance subject 不符都属于 R0，不允许豁免。
- GA 复用最终 RC 已验证的产品签名、SBOM 与 provenance；promotion-only 流程可生成单独的晋级证明，但不得重建或用重签掩盖产品字节变化。

**Maven Central 实现接缝**：`agent-api` / `agent-kit` 使用 Gradle 内置 `signing` 与内存 PGP 密钥，对主 JAR、POM、sources、javadoc 及其签名生成本地 Maven 布局。RC 将该目录封装为 Central Portal bundle，以 `USER_MANAGED` deployment 上传并停在 `VALIDATED`；GA 只发布同一 deployment。PGP 私钥、口令与 Portal token 仅由受保护 secret 注入，Sigstore 不替代 Central 的仓库文件签名。

详细字段、步骤、清册与门定义以 P10 执行规格为准。

## 原因

1. **验证对象唯一**：不可变候选把 commit、产品字节、OCI digest 与所有测试证据绑定到同一个对象，避免移动 `dev` 带来的证据漂移。
2. **消除“RC 通过、GA 变了”**：GA 不重建，正式用户获得的就是已经通过稳定窗口的字节。
3. **保留日常开发效率**：`dev` 仍可滚动，不要求每个提交维护语义化 RC，也不改变现有 prerelease 使用习惯。
4. **运行时无候选分支**：payload 始终是 `1.0.0`，不因 RC/GA 注入不同版本条件，降低最后阶段的行为差异。
5. **回滚诚实**：forward-only 迁移不伪装可逆；能原地回滚就证明兼容，不能则用受控备份恢复并明确停机代价。
6. **兼容边界明确**：`v0.30.0` 是可复验的 N-1；Legacy 不再拖入 1.0 准入，避免无限兼容矩阵。
7. **供应链证据绑定同一 subject**：签名、SBOM、provenance 与测试证据共同指向最终 RC 字节/digest，避免只校验 checksum 却无法证明来源与材料。

## 后果

### 正面

- GA 资产具备同 commit、同 hash、同 digest 的可审计证明。
- 发布缺陷不能通过覆盖同一候选被隐藏；旧 RC 始终可追溯。
- N-1、双数据库、配置和本地状态从“文档承诺”变为发布硬门。
- 四平台与容器部署事实一致，减少“交叉编译绿但原生不可运行”。
- 43 条 FR 的历史验收被重新绑定到最终候选，而不是依赖分散的旧 release note。
- 普通 prerelease 用户不会因 GitHub 的 `prerelease=true` 混淆而误装 RC。
- 产品来源、依赖构成与构建材料可通过签名、固定 SBOM 和 provenance 审计，且不增加运行时依赖。

### 代价与约束

- RC 流水线必须一次性产出完整资产；漏产物只能废弃候选并递增 RC。
- `1.0.0` Maven 坐标通过 Central Portal `USER_MANAGED` deployment 不可变暂存；GA 不能重新跑 Gradle 发布任务，只能发布最终 RC 的同一 deployment。
- 运行时只报告 `1.0.0`，无法仅凭程序版本区分 RC 与 GA；候选身份必须依赖 tag、manifest、hash/digest 和部署记录。
- 在线更新实现必须把日常 `dev` 与显式 RC 分开解析，不能继续把“最新 GitHub prerelease”视为唯一 prerelease 事实。
- 发布 CI 需要 OIDC/Sigstore、CycloneDX 1.6、provenance 生成与回拉验证能力，但这些工具不得进入运行时。
- schema 不兼容时回滚会产生停写与备份恢复时间，迁移说明必须诚实披露。
- 四平台真机、双 Agent、四格和完整稳定窗口会增加发布成本；这是 GA 准入成本，不得为了提速跳过。
- `ARCHITECTURE.md`、`API.md`、`SDK.md`、`OPERATIONS.md`、`CONTRIBUTING.md` 与 `CHANGELOG.md` 必须与发布实现同步；实际 RC/GA 是否通过只能由本次运行证据决定。

## 否决方案

### 方案 A：继续只用移动 `dev` 作为 GA 候选

无法冻结验证对象，后续提交会改变 tag/资产，43 FR 与稳定窗口证据无法证明对应最终 GA。否决。

### 方案 B：RC payload 使用 `1.0.0-rc.N`，GA 再改成 `1.0.0`

即使源码相同，版本注入也会改变二进制、JAR、Web 或 OCI 字节，需要重新构建，破坏“已验证字节原样晋级”。否决。

### 方案 C：RC 通过后，从同 commit 重新构建 GA

同 commit 不等于同字节；工具链、时间戳、依赖仓与构建环境都可能造成差异。否决。

### 方案 D：使用可移动 `rc` tag 覆盖候选

无法追溯失败候选，也无法证明稳定窗口期间资产未变化。否决。

### 方案 E：新增 `update.channel=rc`

扩大控制面/API/SDK 契约和运维复杂度，且不是验证不可变候选所必需。RC 通过显式 tag/manifest 安装即可。否决。

### 方案 F：为兼容 Legacy 扩大到 `v0.19.x`

Legacy 与 V2 在前端、身份、API、数据模型和中间件边界上已分叉，会把 P10 从发布准入变成跨代迁移项目。否决。

### 方案 G：用 down migration 支持回滚

破坏性逆向 DDL 难以保证数据不丢，双数据库和日表场景更不可控。采用前向迁移 + 兼容窗口；不兼容时恢复协调备份。否决。

### 方案 H：继续按“最新 GitHub prerelease”选择普通 prerelease 更新

RC 与 dev 都可标 `prerelease=true`，按最新项会让普通 prerelease 用户误装 GA 候选，并破坏“RC 只显式安装”的边界。必须精确过滤移动 `dev` 与开发版格式。否决。

### 方案 I：只发布 checksum，不做签名、固定 SBOM 与 provenance

checksum 只能发现字节变化，不能证明签名者、构建来源、依赖构成和材料；也无法满足无豁免供应链准入。否决。
