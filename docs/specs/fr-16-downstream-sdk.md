# 功能规格：下游 SDK 接入包（FR-16）

> 状态：开发中　·　关联 PRD：FR-16　·　分支：见 worktree

## 1. 背景与目标

业务插件要接入 Beacon，目前只能直接 `compileOnly` 依赖 `agent-api` 的裸只读契约，自己写一堆样板：判断 agent 是否就绪、捕获 `AgentUnavailableException`、订阅配置变更时还要处理「subscribe 时 agent 尚未就绪」的竞态、agent 由不可用转可用后又要补订阅并重放当前值。这些样板每个下游都重复写、还容易写错（最危险的是误用 `connected()` 当回退判据，导致控制面短暂不可用就丢配置——split-brain）。

目标：发布一套可被 Maven 依赖的 SDK 接入包，含
- `agent-api`（已有，纯只读契约）发布坐标；
- 新增 `agent-kit`（便捷门面），把上面那些样板收口成几个正确的便捷方法，杜绝下游踩坑。

属 P2。

## 2. 需求（要什么）

范围内：
- 新增 `agent-kit` 模块：纯 Java 8、零第三方依赖、只依赖 `agent-api`。
- 一个 `BeaconAccess` 便捷门面，封装下游样板：
  - **回退判据只看 `BeaconAgentProvider.isAvailable()`，绝不看 `connected()`**（防 split-brain：控制面短暂不可用时 agent 仍用本地快照可读配置，不应被判为「不可用」而回退本地文件）。`isBeaconPresent()` 对外只暴露这一个判据。
  - 读已合并结构化配置的便捷方法（取某 dataId 的合并后文本 / 格式 / md5 / 列出 dataId / 整体 md5），agent 不可用时返回空而非抛异常。
  - 查服务发现的便捷方法（按 zone / group 列在线实例、按 query 过滤），agent 不可用时返回空列表。
  - 配置变更订阅桥：注册即重放当前值；agent 由不可用转可用后补注册并重放，覆盖 subscribe 时 agent 未就绪的竞态。
  - `identity()` 仅薄转发（身份/zone/ORM 仍归 CoreLib，kit 不重复实现）。
- `BeaconAccess` **不得做成有状态静态单例**：订阅返回的句柄由调用方保管、自行 `close()`。
- `agent-kit` 必须像 `agent-api` 一样被 agent-bukkit / agent-bungee 的 shadow 配置打进最终 jar（否则下游 compileOnly kit 运行期 NoClassDefFound）。
- `agent-api` 与 `agent-kit` 配 `maven-publish`：group=`top.wcpe.beacon`，artifact=`beacon-agent-api` / `beacon-agent-kit`，version 跟随根 VERSION。发布仓库默认 `mavenLocal()`，远程 URL/凭据走 gradle property 或 env（如 `beaconPublishUrl`）可选注入，缺省即只 mavenLocal。

不做（范围外）：
- kit **不碰线程调度**（不引 okhttp/kotlinx/TabooLib）——切线程仍由下游自理；kit 只封装 `isAvailable` 判据 + dataId 约定 + 订阅桥纯逻辑。
- **本地文件回退不放进 kit**：回退到本地文件的具体读法留给消费方（kit 只告诉它 `isBeaconPresent()` 为 false，要不要回退、怎么读本地文件由下游决定）。
- 不在 kit 重复实现身份/zone/ORM（守 ADR-0005：实现不进 api/kit）。
- 不发到 `repo.tabooproject.org`（TabooLib 的、无写权限）。

## 3. 设计（怎么做）

新增模块 `agent/agent-kit`（`java-library`，纯 Java 8，依赖 `api(project(":agent-api"))`）。

`BeaconAccess`（实例化门面，非静态单例）：
- 构造无参，内部不持有 agent 引用——每次调用都现取 `BeaconAgentProvider`，避免缓存到一个可能已注销的实例。
- `isBeaconPresent()` → `BeaconAgentProvider.isAvailable()`。
- 读配置便捷方法在 agent 不可用时返回 `Optional.empty()` / 空集合（吞 `AgentUnavailableException` 属正常降级，非吞业务异常）。
- 发现便捷方法在 agent 不可用时返回空列表（不触发同步 HTTP）。
- `identity()` → `Optional<AgentIdentity>`（agent 不可用时空）。
- `subscribeConfig(listener)` 返回 `BeaconSubscription`（实现 `AutoCloseable`）：
  - 注册时若 agent 已就绪：立刻向 `EffectiveConfig.onChange` 注册底层监听，并**同步重放一次当前值**（用当前 dataIds + 整体 md5 回调一次）。
  - 注册时若 agent 未就绪：只记录待注册状态，等后续 `pump()` 被驱动时再补注册 + 重放。
  - **agent 由不可用转可用的补注册靠 `pump()` 驱动**：kit 不自起线程（零调度承诺），由下游在其既有 tick / 心跳里周期调用 `subscription.pump()`（或 `BeaconAccess.pumpAll()`），kit 内部用 `isAvailable()` 边沿检测决定是否补注册重放。这样既覆盖竞态，又不违反「kit 不碰调度」。
  - `close()` 注销底层监听、置为已关闭，重复调用安全。

为什么补注册要下游驱动 `pump()` 而非 kit 自己起线程：kit 纯 Java8 零三方依赖、零调度承诺；agent-api 的 `BeaconAgentProvider` 是静态门面、没有「就绪事件」回调可挂。下游本来就跑在 MC 主线程 tick / 异步心跳里，顺手调一次 `pump()` 成本极低，且把线程切换留在下游可控处（符合 ADR-0005 精神：调度/IO 不下沉到无依赖契约层）。

发布：在 `agent-api` / `agent-kit` 各 apply `maven-publish`，配 `publishing { publications { ... } repositories { mavenLocal 默认 + 可选远程 } }`，artifactId 固定 `beacon-agent-api` / `beacon-agent-kit`，groupId 覆盖为 `top.wcpe.beacon`（区别于构建期 `top.wcpe.beacon.agent` 的 Gradle group，发布坐标对外更短）。

双端壳：`shadowed(project(":agent-kit"))` + `compileOnly(project(":agent-kit"))`，jar 任务已 `from(shadowed...)`，自动打进。

## 4. 任务拆分
- [ ] 新增 `agent-kit` 模块骨架（build.gradle.kts + settings include）
- [ ] 测试先行：`BeaconAccess` / 订阅桥单测（isAvailable 回退判据、订阅重放、agent 未就绪补注册重放、close 幂等）
- [ ] 实现 `BeaconAccess` + `BeaconSubscription`
- [ ] 双端壳 shadow 打包 kit
- [ ] `agent-api` / `agent-kit` 配 maven-publish（mavenLocal 默认 + 可选远程）
- [ ] 文档同步：PRD 状态、ARCHITECTURE、CHANGELOG

## 5. 验收标准
- `gradlew :agent-kit:test` 绿：覆盖 isAvailable 回退判据、订阅注册即重放、agent 未就绪时订阅延迟到 pump 后补注册重放、close 幂等。
- `gradlew build` 双端壳 jar 内含 `top.wcpe.beacon.agent.kit.*` 类（与 api 同被打包）。
- `gradlew publishToMavenLocal` 产出 `top.wcpe.beacon:beacon-agent-api` 与 `top.wcpe.beacon:beacon-agent-kit` 坐标，version = 根 VERSION。
- kit 编译产物零第三方依赖（POM 仅含 agent-api）。

## 6. 风险 / 待定
- kit 首版随 `0.y.z`，按 ADR-0007 不承诺向后兼容（破坏性变更仍记 CHANGELOG）。
- 远程发布仓库凭据不入库，仅占位说明走 property/env。
