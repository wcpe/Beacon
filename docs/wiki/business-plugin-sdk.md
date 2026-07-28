# 业务插件接入 SDK

Beacon 的业务插件集成点是本机 Agent API，而不是控制面 HTTP。这样控制面暂时不可用时，Agent 仍能使用本地快照提供配置、候选和降级能力，业务代码不会把玩家线程绑在网络请求上。

> **版本边界**：以下写法面向 v2。旧 v1 HTTP 接口及旧 Redis 消息配置只为兼容参考。跨服消息的当前路径是 Agent → 控制面 HTTP 单跳中转 → Agent；新插件不得新增 Redis 直连依赖。

## 1. 添加依赖

下游插件只使用 `compileOnly`，运行时由 `BeaconAgent.jar` 或 `BeaconAgentProxy.jar` 提供实现。部署的 Agent 版本必须不低于编译所用 API/Kit 版本。

```kotlin
repositories { mavenLocal() /* 或受控的私有 Maven 仓库 */ }

dependencies {
    compileOnly("top.wcpe.beacon:beacon-agent-api:<版本>")
    compileOnly("top.wcpe.beacon:beacon-agent-kit:<版本>") // 推荐
}
```

在插件描述中声明对 BeaconAgent 的软依赖或硬依赖，取决于你的业务是否允许 Beacon 缺席时降级。不要把 API 或 Kit shade 到业务插件 JAR 中。

## 2. 配置与发现的最小用法

```kotlin
import top.wcpe.beacon.agent.kit.BeaconAccess
import java.util.concurrent.CompletableFuture

class ExampleFeature {
    private val beacon = BeaconAccess()

    fun loadConfig(): String? {
        if (!beacon.isBeaconPresent()) return readBundledDefault()
        return beacon.rawConfig("example.yml").orElse(null)
    }

    fun subscribe() = beacon.subscribeConfig { dataId, content ->
        if (dataId == "example.yml") runAsync { reload(content) }
    }

    fun peersAsync(): CompletableFuture<List<String>> =
        CompletableFuture.supplyAsync {
            if (!beacon.isBeaconPresent()) return@supplyAsync emptyList()
            beacon.instancesInZone(currentGroup(), currentZone()).map { it.serverId() }
        }
}
}
```

调用纪律：

1. 以 `isBeaconPresent()`/`isAvailable()` 判断 Agent 是否存在，**不要**用 `connected()` 判断是否需要回退。断连时 Agent 仍可能持有可用快照。
2. 服务发现是同步 HTTP，必须放在业务插件自己的异步线程；配置回调也不应在其中做重活。
3. 订阅返回的句柄应在插件停用时关闭；需要补注册时按 Kit 契约调用 `pump()`。
4. 普通业务插件的身份、Zone 和 ORM 仍应走项目约定的身份服务（例如 CoreLib）；不要重复把 SDK 薄门面当成第二身份真源。

## 3. 调度与健康

通过 `BeaconAgentApi.scheduling()` 的 `BeaconScheduling` 读取候选、健康和调度结果。`acquireCandidate(zone, purpose)` 是异步接口：控制面可用时返回控制面决策；网络超时或服务端故障时自动使用本地快照，结果会标明 `CONTROL_PLANE` 或 `LOCAL_FALLBACK`。

```kotlin
import top.wcpe.beacon.agent.api.BeaconAgentProvider

val scheduling = BeaconAgentProvider.get().scheduling()
scheduling.acquireCandidate("zone-a", "matchmaking")
    .thenAccept { result ->
        val target = result.chosen()
        if (target == null) {
            showNoServerMessage(result.failReason())
        } else {
            connectPlayerAsync(target.serverId())
        }
    }
```

- 不要在 MC 主线程 `join()`、`get()` 或轮询 future。
- `candidatesInZone` 与 `healthOf` 读取本地快照，适合快速只读展示；它们不是强实时查询。
- `zone_not_found`、跨 namespace 和参数错误是业务/拓扑错误，应显式处理；不要把它们伪装成网络降级。
- 本地降级保证可用性，不保证最新拓扑。恢复后让 Agent 自动刷新和补报，不要自行直连管理 API。

## 4. 跨服消息

跨服消息应通过 Agent API 的消息门面发送、订阅和处理。选择明确的接收范围（服务器、玩家、Zone 或 namespace）并定义小而稳定的 payload 契约；处理器必须校验业务字段、幂等处理重复投递，并避免把大文件或敏感资料当作消息体。

当前消息链路由控制面保存受控的元数据并做单跳转发。业务插件不得：

- 直接请求 `/beacon/v2/agent/*`；
- 在插件内配置 Redis 地址、密码或消费组；
- 依赖消息投递来维护不可丢失的账务状态；
- 用消息绕过 namespace 隔离或权限检查。

涉及玩家位置时，使用 Agent 提供的只读名册能力；玩家离线、切服和控制面不可达都必须按返回状态做降级处理。

## 5. 发布与排障

上线前确认 Agent、SDK 和业务插件版本兼容；在测试 namespace 验证配置订阅、调度失败分支、消息幂等和 Agent 缺席时的业务回退。生产问题优先查看 Agent `/beacon status`、命令观测和调度 trace，再联系运维；不要要求业务插件暴露控制面 token。

完整 API、发布坐标与配置/发现示例见 [SDK 接入指南](../SDK.md)。调度真源见 [指标、健康与调度 V2](../specs/v2-metrics-health-scheduling.md)，消息真源见 [连接与跨服消息 V2](../specs/v2-connection-message-storage.md) 与 [ADR-0063](../adr/0063-cross-server-message-control-plane-relay.md)。
