# ADR-0039：agent 注册时自报构建版本，控制面只读暴露 + 集群版本不一致黄标

**状态**：已接受

## 背景

运维盲区：一台服可能装了**过期的 agent jar**，但控制面与管理台对此完全不可见——既有 `version` 字段是**业务版本**（运维在 `config.yml` 里手填的 `identity.version`，作发现过滤标签 FR-29 用，agent 自己不写它），并非「这台跑的是哪个 Beacon agent 构建」。结果是：升级 agent 时漏装一台、或某台回滚后忘了换 jar，运维只能上机 `ls` jar 名才知道，无法在管理台一眼看出「集群里谁还在跑旧版」。

FR-86 要补这个盲区：让 agent **注册时自报自身构建版本**，控制面存进内存注册表、`InstanceView` 暴露，管理台逐台展示，并在**同一集群（环境）内版本不一致时打黄标**提示运维去对齐。

## 决策

1. **agent 自报构建版本，新增注册 payload 字段 `agentVersion`（与业务 `version` 并列、语义不同）。**
   - `version`（既有）= 业务版本，运维手填，发现过滤标签，可空。
   - `agentVersion`（新增）= **agent 自身构建版本**，由 agent 自动取、非手填。两者职责正交、不复用同一字段。

2. **版本来源 = TabooLib 运行期 `pluginVersion`（壳层取，注入 core）。**
   - agent 构建版本的唯一真源是仓库根 `VERSION` 文件（[ADR-0007](0007-single-version-source.md)），经 Gradle 注入到插件 manifest；运行期 TabooLib 经 `taboolib.common.platform.function.pluginVersion` 暴露，双端（bukkit/bungee）通用。
   - **壳层**（`BeaconAgentBukkit`/`BeaconAgentBungee`）在 ENABLE 时读 `pluginVersion`，经 `AgentBootstrap.readIdentity(reader, role, agentVersion)` 注入 `AgentIdentity.agentVersion`。**core 不碰 TabooLib / 平台 API**——守 [ADR-0005](0005-agent-transport-codec-abstraction.md)（core 零具体库依赖）与 architecture-invariants（agent core 不依赖平台、不阻塞主线程：取版本是内存读，非 IO）。

3. **`agentVersion` 仅在非空时拼入注册报文（向后兼容）。**
   - 与 `backends`（FR-36）同范式：旧 agent / 未注入版本时缺键，控制面解析为空串，`InstanceView.agentVersion` 为空、管理台显「未知」。新增字段是**加法**，旧控制面忽略即可。

4. **控制面只存内存、只读暴露，不落 DB、不参与任何决策。**
   - `runtime.Instance` 加 `AgentVersion string` 字段，注册时写入；`InstanceView` 加 `agentVersion`。与 `version`/`backends`/`proxy` 同列——纯展示事实，不进调度 / 健康 / 覆盖链。随注册刷新（心跳 / report 不带，沿用注册时值，agent 重启即重注册刷新）。

5. **集群版本不一致黄标在前端派生，不进后端契约。**
   - 管理台按**环境（namespace）** 聚合所有在册实例的非空 `agentVersion`，**去重后多于一个**即认为该环境 agent 版本不一致，对版本与「集群多数版本」不同的行 / 详情打黄标。判定是纯前端展示派生（与未分配 zone 黄高亮、drain 黄标同范式），后端不算、不存、不返回「是否不一致」标志位。

## 理由

- **自报 vs 控制面推断**：agent 构建版本只有 agent 自己知道（jar 内 manifest）。控制面无从「推断」——既不能从业务 `version` 推（那是手填业务版本）、也不能从 address / 行为推。唯一可靠来源就是 agent 注册时带上。故选自报。
- **放 `AgentIdentity` 而非另起 DTO**：版本是注册身份/标签段的一部分，与 `version`/`role`/`metadata` 同属注册自描述，放一起最自然；core 用 `Map` 拼报文（ADR-0005），加一个可选键零成本。
- **不落 DB**：注册/健康真源是 Go 进程内存（architecture-invariants §3），agent 版本属此类瞬时事实，重注册即刷新，无历史留存需求，落 DB 是过度设计。
- **黄标在前端**：「不一致」是展示判断、随轮询数据实时算即可，无需后端多一个派生字段污染契约；与既有黄标范式一致。

## 后果

- 注册协议新增**可选** `agentVersion` 字段，向后兼容（旧 agent 缺键、旧控制面忽略）。
- 需重建并重新部署**双端 agent jar**（bukkit + bungee）才能看到真实版本；未升级的 agent 在管理台显「未知」——这本身就是 FR-86 要暴露的信号（谁还没升）。
- core 仍零平台依赖：版本由壳层注入，core 只透传字符串。

## 备选方案

- **复用既有 `version` 字段塞 agent 版本**：语义冲突（业务版本 vs 构建版本），且会污染发现过滤标签。被否。
- **控制面从别处推断 agent 版本**（如按已知发布映射、按行为指纹）：不可靠、维护成本高、本质是猜。被否。
- **agent 版本落 DB 留历史**：注册/健康真源是内存，agent 版本是瞬时事实、无历史需求，落库违背真源切分且属镀金。被否。
- **后端返回「集群是否不一致」布尔**：把展示派生塞进契约，前端能算的不该让后端存。被否。
