# 下游 SDK 接入指南（agent-api / agent-kit）

> 面向**业务插件开发者**：如何让 Bukkit/Bungee 上的业务插件接入 Beacon agent，读有效配置、查服务发现。
> **普通业务插件**：身份（serverId/zoneId）与数据库/ORM 走 **CoreLib**（经 `CoreLibApi`），本 SDK 只负责「读已合并配置 + 查发现」，两者不重叠。
> **注**：CoreLib 自身的 serverId/zone 来源方向是「优先取自 Beacon agent、不在场才降级本地」，它用下方 `awaitIdentity` 取身份；这不影响普通业务插件继续从 CoreLib 拿身份。

## 1. SDK 组成（两个工件）

| 工件 | 是什么 | 何时用 |
|---|---|---|
| `beacon-agent-api` | 纯 Java8 只读契约（接口 + 值对象），零第三方依赖 | 必需，直接面对 `BeaconAgent` 门面时 |
| `beacon-agent-kit` | 纯 Java8 便捷层，只依赖 `agent-api`，封装下游样板 | 推荐，省去回退判据/订阅竞态等踩坑 |

两者运行期都由 **BeaconAgent 插件**提供（已 shade 进 `BeaconAgent.jar`/`BeaconAgentProxy.jar`），下游只 `compileOnly` 依赖、**不打进自己的 jar**。

## 2. 发布坐标与版本对齐

- **坐标**：`top.wcpe.beacon:beacon-agent-api:<版本>`、`top.wcpe.beacon:beacon-agent-kit:<版本>`。
- **版本**：跟随仓库根 `VERSION`，与控制面 / 两个 agent jar **三组件恒一致**。`1.0.0` 起按 SemVer 管理公开契约。
- **消费仓库**：`https://repo.wcpe.top/repository/maven-public/`（正式发布的 SDK 坐标在此解析）。
- **本地开发**：可发 `mavenLocal()` 供本机联调；在仓库的 agent 构建目录执行 `./gradlew publishToMavenLocal`。发布到远程仓库由 `beaconPublishUrl` / `beaconPublishUsername` / `beaconPublishPassword` 或对应环境变量注入。
- **正式发布**：RC 在不可变 GitHub 产品资产发布后，自动发布 `X.Y.Z-rc.N` 的两个 SDK Maven 坐标；GA 先从最终 RC 原样复制 GitHub 产品资产并逐项核验文件名、大小和 SHA-256，再仅为 `agent-api` 与 `agent-kit` 的不同 `X.Y.Z` Maven 正式坐标重新生成并发布制品。GitHub 产品资产不重新生成、替换或补传——SDK Maven 坐标是唯一允许在 GA 阶段重新生成的产物。
- **版本对齐矩阵（硬约束）**：**部署的 BeaconAgent 版本必须 ≥ 下游编译所用 agent-api/kit 版本**（运行期提供方不得旧于编译期契约），否则可能 `NoSuchMethodError`。

RC/GA 的通用检查入口为 `make release-test`、`make release-check`、`make release-verify-rc` 和 `make release-verify-ga`；这些入口校验正式版本、RC/GA 标签、产品资产闭集、SHA-256 以及 RC/GA commit 一致性。

### 2.1 发布到私有远程仓库（Nexus / Artifactory）

仓库 URL 与凭据**全部经环境变量注入**（不入库、不硬编码；流水线用 secret）；未设 `BEACON_PUBLISH_URL` 时只发本机 `mavenLocal`。

```bash
# 远程 releases 仓库地址（WCPE Nexus）
export BEACON_PUBLISH_URL='https://repo.wcpe.top/repository/maven-releases/'
# 凭据（由发布流水线的 secret 注入；仓库无鉴权时可省，走匿名）
export BEACON_PUBLISH_USERNAME='<仓库账号>'
export BEACON_PUBLISH_PASSWORD='<仓库口令或令牌>'
```

随后进入仓库的 agent 构建目录（`gradlew` 所在的那一级），执行：

```bash
# 仅发远程仓库：
./gradlew :agent-api:publishAllPublicationsToBeaconRemoteRepository \
          :agent-kit:publishAllPublicationsToBeaconRemoteRepository
# 或一并发 mavenLocal + 远程：./gradlew :agent-api:publish :agent-kit:publish
```

- 产出两件工件 `beacon-agent-api` / `beacon-agent-kit`（均含 sources jar）；正式发布分别以 `X.Y.Z-rc.N` 和 `X.Y.Z` 作为候选与正式 Maven 版本。
- 远程仓库选 **releases**（version 不含 `-SNAPSHOT` 即按 release 发，覆盖策略由仓库侧 release 规则约束）。
- Artifactory 同理：`BEACON_PUBLISH_URL` 填 `https://<artifactory>/artifactory/<repo-key>/`，凭据用账号 + API Key / 令牌。

## 3. 接入（下游 build + plugin.yml）

```kotlin
// 下游业务插件 build.gradle.kts
repositories {
    maven("https://repo.wcpe.top/repository/maven-public/")
}
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
        val raw = if (beacon.isBeaconPresent()) beacon.rawConfig("plugins/MyEconomy/config.yml").orElse(null)
                  else readBundledDefault("plugins/MyEconomy/config.yml")
        reloadEconomy(raw)

        // 订阅热更，只订阅本插件目录 plugins/MyEconomy/（别人的变更不会叫醒你）：注册即重放当前值；
        // agent 未就绪不丢订阅，周期 pump() 在其转可用后补注册重放
        val sub = beacon.subscribeConfig("plugins/MyEconomy/") { changedDataIds, _ ->
            // 回调是 Set 语义：给你「本次变了哪些 dataId」+ 变更后整体 md5（第二个参数这里不需要，用 _ 忽略），
            // **不是**「单个 dataId + 新内容」——新内容要自己用 beacon.rawConfig(...) 现读
            submit(async = true) { // 回调不在 MC 主线程，但重活/落地仍建议切线程
                if (changedDataIds.contains("plugins/MyEconomy/config.yml")) {
                    reloadEconomy(beacon.rawConfig("plugins/MyEconomy/config.yml").orElse(null))
                }
            }
        }
        // sub 由你保管，DISABLE 时 sub.close()；按需周期调用 sub.pump()

        // 若要上报重载结果（交付据此做一票否决），实现 ReportingConfigChangeListener 而非裸 lambda：
        // 无关变更必须 return ConfigReloadResult.ok()，见 §6 第 5 条
    }

    // 查发现务必在异步线程（同步 HTTP）
    fun sameZonePeers(): List<String> {
        if (!beacon.isBeaconPresent()) return emptyList()
        val zone = corelibZoneId() // ← 业务插件的 zone 经 CoreLib 取（CoreLib 自身优先取自 Beacon agent）
        return beacon.instancesInZone(corelibGroupId(), zone).map { it.serverId() }
    }
}
```

> 三个容易写错的点：① 回调参数是**集合**（`Set<String> changedDataIds` + `String newMd5`），不是「单个 dataId + 内容」，
> 新内容要自己按 dataId 现读；② `dataId` 是配置文件登记时的**逻辑名**，即目标机上的相对路径（如 `plugins/MyEconomy/config.yml`）；
> ③ **注册即重放**：注册成功时会立刻回调一次，携带当前**匹配前缀的**全部 dataId（无前缀时即全部 dataId）作为基线，
> 所以别指望「第一次回调 = 有人改了配置」；带前缀订阅若当前**零匹配**，这次重放会整次跳过（不回调）。

## 5. API 参考

### `BeaconAccess`（kit 便捷门面）
| 方法 | 说明 |
|---|---|
| `isBeaconPresent()` | agent 是否在场（**回退判据**，只看 `isAvailable()`） |
| `identity()` | 当前身份（薄转发）；不在场为空（**不阻塞**，zone 可能尚未回填） |
| `awaitIdentity(timeoutMillis)` | 有界等待首次注册完成（zone 已回填）后取身份；不在场或超时为空。会阻塞调用线程至多 timeoutMillis。CoreLib 以极大超时持续等待至取得确定身份（不使用超时降级路径）；普通业务插件不需要 |
| `rawConfig(dataId)` / `configFormat` / `configMd5` | 单项有效配置文本/格式/md5；不在场或无项为空 |
| `dataIds()` / `effectiveMd5()` | 全部 dataId / 整体 md5 |
| `subscribeConfig(listener)` | 订阅变更（**不过滤**），返回 `BeaconSubscription`（`pump()` 补注册、`close()` 注销） |
| `subscribeConfig(pathPrefix, listener)` | 同上，但**只订阅自己目录**：仅当本次变更里有以 `pathPrefix` 开头的 dataId 时才回调，且回调只带匹配项。前缀请以 `/` 结尾（`plugins/Foo/`）。`null`/空串等于不过滤 |
| `query(q)` / `instancesInZone(g,z)` / `instancesInGroup(g)` | 服务发现（**同步 HTTP，异步线程调用**）；不在场为空列表 |

### `BeaconAgentProvider` / `BeaconAgent`（底层契约，直连用）
- `BeaconAgentProvider.isAvailable()` / `get()`：取门面（`get()` 不在场抛 `AgentUnavailableException`）。
- `BeaconAgent`：`identity()` / `config()` / `discovery()` / `connected()` / `effectiveMd5()`。
- `BeaconAgent.config()` → `EffectiveConfig`：`dataIds()` / `raw(id)` / `format(id)` / `md5(id)` /
  `onChange(listener)`（不过滤）/ `onChange(pathPrefix, listener)`（按路径前缀过滤，回调只带匹配项；
  前缀 `null`/空串等于不过滤）。匹配规则见 `DataIdPrefix`。

## 6. 关键纪律（踩坑红线）

1. **回退判据只看 `isBeaconPresent()`（= `isAvailable()`），绝不看 `connected()`**：控制面短暂不可用时 agent 仍以本地快照 fail-static、配置仍可读；误用 `connected()` 会把「在场但暂未连上」误判为不可用而回退本地，造成 split-brain。
2. **业务插件的身份/zone/ORM 走 CoreLib**：普通业务插件经 `CoreLibApi` 取 serverId/zone，不把本 SDK 的 `identity()` 当身份真源；`BeaconAccess.identity()` 仅薄转发，SDK 不重复 CoreLib 的数据访问职责。**例外（CoreLib 自身）**：CoreLib 的 serverId/zone 来源方向是**优先取自 Beacon agent**——agent 在场则用 `awaitIdentity` 持续等待至注册就绪、**必须取得确切 serverId + zone**（zone 未指派则打 ERROR 并中止启动，不兜底、不超时降级）；仅 agent **不在场**（`isBeaconPresent()`=false，须先用平台 API 探测插件在场再碰 SDK 类防 `NoClassDefFoundError`）才降级读取本地配置 + WARN。
3. **发现是同步 HTTP**：务必在异步线程调用；变更回调在 agent 异步线程触发，重活自行切线程。
4. **本地文件回退由下游决定**：agent 不在场时便捷方法返回空，要不要读本地默认、怎么读由下游自理（kit 只用 `isBeaconPresent()` 告知是否在场）。
5. **变更通知是广播的：与己无关必须返回 `ok()`**——最容易把整批交付打挂的一条。
   - agent 向**所有**注册监听器广播本次变更，不替任何人推断「你关心哪个 dataId」。插件必须**自己**判断相关性；下发集合里出现别人的配置是正常现象，不是异常。
   - **与己无关时必须返回 `ConfigReloadResult.ok()`**（老式 `void` 回调则直接 `return`）。返回 `failed()` 会触发交付的**一票否决**：整单目标判 failed 并**熔断停批**——等于一个插件的无关判断把整批交付打挂。同理也**别抛异常**（抛异常按 failed 计）。
   - 用带前缀的 `subscribeConfig(pathPrefix, listener)` 可让 agent **先把不相关的挡掉**（连回调都不发生），省掉这层判断与唤醒；但它是省事，不替代纪律——自己注册时仍可能收到同目录内的多种变更。
   - 相关性判断建议用**目录前缀**，且**前缀结尾带 `/`**：`plugins/Foo` 会误匹配 `plugins/Foo2/config.yml`。`dataId` 就是配置文件登记时填的**逻辑名**，即目标机上的相对路径（如 `plugins/Foo/config.yml`），可直接当前缀用。
