# 下游 SDK 接入指南（agent-api / agent-kit）

> 面向**业务插件开发者**：如何让 Bukkit/Bungee 上的业务插件接入 Beacon agent，读有效配置、查服务发现。
> 身份（serverId/zoneId）与数据库/ORM 仍走 **CoreLib**，本 SDK 只负责「读已合并配置 + 查发现」，两者不重叠。

## 1. SDK 组成（两个工件）

| 工件 | 是什么 | 何时用 |
|---|---|---|
| `beacon-agent-api` | 纯 Java8 只读契约（接口 + 值对象），零第三方依赖 | 必需，直接面对 `BeaconAgent` 门面时 |
| `beacon-agent-kit` | 纯 Java8 便捷层，只依赖 `agent-api`，封装下游样板 | 推荐，省去回退判据/订阅竞态等踩坑 |

两者运行期都由 **BeaconAgent 插件**提供（已 shade 进 `BeaconAgent.jar`/`BeaconAgentProxy.jar`），下游只 `compileOnly` 依赖、**不打进自己的 jar**。

## 2. 发布坐标与版本对齐

- **坐标**：`top.wcpe.beacon:beacon-agent-api:<版本>`、`top.wcpe.beacon:beacon-agent-kit:<版本>`。
- **版本**：跟随仓库根 `VERSION`，与控制面 / 两个 agent jar **三组件恒一致**（[ADR-0007](adr/0007-versioning-and-release-channels.md)）。1.0.0 之前为 `0.y.z`，**接口不承诺向后兼容**（破坏性在 CHANGELOG 标明）。
- **发布仓库**：默认只发 `mavenLocal()`；远程仓库可选，URL/凭据走 gradle property（`beaconPublishUrl` / `beaconPublishUsername` / `beaconPublishPassword`）或同名大写 env 注入，缺省即只本机。命令：`./gradlew publishToMavenLocal`。
- **版本对齐矩阵（硬约束）**：**部署的 BeaconAgent 版本必须 ≥ 下游编译所用 agent-api/kit 版本**（运行期提供方不得旧于编译期契约），否则可能 `NoSuchMethodError`。

## 3. 接入（下游 build + plugin.yml）

```kotlin
// 下游业务插件 build.gradle.kts
repositories { mavenLocal() /* 或贵方远程仓库 */ }
dependencies {
    compileOnly("top.wcpe.beacon:beacon-agent-api:<版本>") // 只读契约
    compileOnly("top.wcpe.beacon:beacon-agent-kit:<版本>") // 便捷层（可选但推荐）
}
```

TabooLib 插件按惯例对 `BeaconAgent` 声明软/硬依赖（让下游 ClassLoader 能解析 `top.wcpe.beacon.agent.api.*` / `...kit.*` 并共享运行期门面）。

## 4. 最小接入示例

```kotlin
import top.wcpe.beacon.agent.kit.BeaconAccess

object MyEconomyPlugin : Plugin() {
    private val beacon = BeaconAccess() // 无状态门面，可自由 new

    @Awake(LifeCycle.ENABLE)
    fun enable() {
        // 读一份合并后的结构化配置；agent 不在场则回退本插件内置默认文件（回退由下游决定，kit 不做）
        val raw = if (beacon.isBeaconPresent()) beacon.rawConfig("economy.yml").orElse(null)
                  else readBundledDefault("economy.yml")
        reloadEconomy(raw)

        // 订阅热更：注册即重放当前值；agent 未就绪不丢订阅，周期 pump() 在其转可用后补注册重放
        val sub = beacon.subscribeConfig { dataId, content ->
            submit(async = true) { if (dataId == "economy.yml") reloadEconomy(content) } // 重活自行切线程
        }
        // sub 由你保管，DISABLE 时 sub.close()；按需周期调用 sub.pump()
    }

    // 查发现务必在异步线程（同步 HTTP）
    fun sameZonePeers(): List<String> {
        if (!beacon.isBeaconPresent()) return emptyList()
        val zone = corelibZoneId() // ← zone 来自 CoreLib，不来自 SDK
        return beacon.instancesInZone(corelibGroupId(), zone).map { it.serverId() }
    }
}
```

## 5. API 参考

### `BeaconAccess`（kit 便捷门面）
| 方法 | 说明 |
|---|---|
| `isBeaconPresent()` | agent 是否在场（**回退判据**，只看 `isAvailable()`） |
| `identity()` | 当前身份（薄转发）；不在场为空 |
| `rawConfig(dataId)` / `configFormat` / `configMd5` | 单项有效配置文本/格式/md5；不在场或无项为空 |
| `dataIds()` / `effectiveMd5()` | 全部 dataId / 整体 md5 |
| `subscribeConfig(listener)` | 订阅变更，返回 `BeaconSubscription`（`pump()` 补注册、`close()` 注销） |
| `query(q)` / `instancesInZone(g,z)` / `instancesInGroup(g)` | 服务发现（**同步 HTTP，异步线程调用**）；不在场为空列表 |

### `BeaconAgentProvider` / `BeaconAgent`（底层契约，直连用）
- `BeaconAgentProvider.isAvailable()` / `get()`：取门面（`get()` 不在场抛 `AgentUnavailableException`）。
- `BeaconAgent`：`identity()` / `config()` / `discovery()` / `connected()` / `effectiveMd5()`。

## 6. 关键纪律（踩坑红线）

1. **回退判据只看 `isBeaconPresent()`（= `isAvailable()`），绝不看 `connected()`**：控制面短暂不可用时 agent 仍以本地快照 fail-static、配置仍可读；误用 `connected()` 会把「在场但暂未连上」误判为不可用而回退本地，造成 split-brain。
2. **身份/zone/ORM 走 CoreLib**：`BeaconAccess.identity()` 仅薄转发，SDK 不重复 CoreLib 的身份与数据访问职责。
3. **发现是同步 HTTP**：务必在异步线程调用；变更回调在 agent 异步线程触发，重活自行切线程。
4. **本地文件回退由下游决定**：agent 不在场时便捷方法返回空，要不要读本地默认、怎么读由下游自理（kit 只用 `isBeaconPresent()` 告知是否在场）。
