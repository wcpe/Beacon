# 功能规格：交付重载回执（Delivery Reload ACK）

> 状态：**已交付@v1.5.0**（2026-10-19 实现并通过真机验收，2026-10-24 随 v1.5.0 交付）　·　关联 PRD：**FR-274**（主体）、**FR-275**（§3.5 告警段）　·　分支：待建
> **真机验收清单与结果**：[delivery-reload-ack-acceptance](delivery-reload-ack-acceptance.md)
> （A1–A9 / B1–B4 已执行；验收中发现并修复两处链路断点，见该文 §8）
>
> **同时归真 FR-16**：本规格 §1.2 的 `fireChanged` 监听器隔离缺陷落在 Legacy FR-16
> 「下游 SDK 接入包」已交付的配置订阅桥能力内，该 FR 曾退回 `开发中`，现已随本 FR 一并重新交付（`v1.5.0`）。


## 1. 背景与目标

`hot_reload` 生效方式的当前语义是「文件已落盘 + 通知已派发」即回执 `success`，控制面据此把
`change_target` 置 `activated`。但**插件重载本身可能失败**——配置解析错误、依赖插件缺失、
监听器抛 `LinkageError`、重载卡死超时。这些失败目前**完全不可见**：面板是绿的，`alert_event`
没有任何记录，运维只有在玩家反馈后才可能发现问题。

本规格的目标：**让插件重载结果真实传导为交付 ACK**，失败即熔断停批并产出告警，
把「假成功」堵死。

范围归属：agent（`agent-api` / `agent-core` / `agent-bukkit` / `agent-bungee`）+ 控制面（交付编排 + 告警域）
+ 前端（交付单详情 + 告警页）。**不涉及 JianManager**。

### 1.1 核实得到的现状（实施前提）

```
CP: delivery_activate
 └─ DeliveryCommandExecutor.runActivate                      :291
     └─ runHotReload                                         :401
         └─ publishConfigChanged(...)                        :466
             │  ✅ 已 catch 异常 → failResultWithState（部分成功语义，:479）
             └─ adapter.publishConfigChanged                 PlatformAdapter.kt:128
                 ├─ BukkitPlatformAdapter.kt:41              直接透传
                 ├─ BungeePlatformAdapter.kt:102             直接透传
                 └─ EffectiveConfigView.fireChanged          :35   ❌ 裸 for 循环
                     └─ listener.onConfigChanged              ConfigChangeListener.java:16  ❌ void
```

两个既有事实决定了实施成本极低：

1. **失败通道已经存在**：`DeliveryCommandExecutor.publishConfigChanged`（:466）已具备
   「抛异常 → 回执 `failed`」并携带「文件已落盘、仅通知失败」的部分成功语义（:479）。
2. **协议可加性扩展**：agent 用 `buildMap` 逐字段写 JSON（`BeaconApiDelivery.kt:74`），
   控制面用强类型 struct 解码（`delivery_agent_handler.go:63`）——Go `json.Decode` 忽略未知字段，
   三方（老/新 agent × 老/新 CP）互操作均不报错。

### 1.2 附带发现的独立缺陷（本规格一并修复）

`EffectiveConfigView.fireChanged`（:35）是裸 `for` 循环、无逐监听器隔离：
**任一监听器抛异常 → 后续监听器全部收不到通知**。多插件共存的服务器上，这意味着一个插件的
bug 会让其它插件静默停留在旧配置。此缺陷与 ACK 无因果关系，属必修项（§5 任务 1）。

## 2. 需求（要什么）

- **逐监听器隔离**：一个插件重载抛异常 / 返回失败 / 超时，**不得**影响其余插件收到通知。
- **结果可上报**：新增可选的上报式监听器，插件能返回结构化的重载结果
  （成功 / 失败 + 可读原因 + 实际加载的 md5 与版本）。
- **失败即一票否决**：聚合报告中存在任一失败 → 该目标回执 `failed` → `change_target` 置 `failed`
  → 触发既有熔断（失败率 / 健康恶化）停推下一批。**不得**降级为「仅告警、仍判 `activated`」。
- **失败明细可见**：回执报文携带结构化 `reload` 段；控制面落库；交付单详情可展开到「哪个插件、什么原因」。
- **告警**：目标因重载失败而 `failed` 时，扇出一条 `publish-fail` 告警事件。
- **超时可控**：每个监听器独立超时（可配），超时按失败计并指明是哪个插件超时；
  超时值必须**小于** `activate_timeout_sec`，否则控制面先判整体超时，报文里看不出根因。
- **三条向后兼容**：老插件（仅实现 `onChange`）零改动可编译可运行；老 agent（不返回 `reload` 段）
  + 新 CP 行为等价于改造前；新 agent + 老 CP 字段被忽略。

范围内：重载结果传导、超时、回执结构、落库、告警接线、前端展示、以及 §1.2 的隔离缺陷修复。

不做（范围外）：
- **插件分级**（manifest 声明 `critical: true/false`）。本期一律一票否决；
  仅在确实被非关键插件拖垮整单后再单独提议。
- **自动回滚**。熔断只停批；回滚仍需填原因 + 二次确认（沿用既有语义）。
- **L5 业务可用性观测**（玩家是否真能参与活动）。属玩法层，不进 Beacon。
- 不改变 `hot_reload` 既有的「不保证 jar 被插件框架真正重载」边界——含 jar 替换仍应选 `restart`。
- 不新增告警类型的枚举（复用已预留的 `publish-fail`，见 §3.5）。

## 3. 设计（怎么做）

### 3.1 `agent-api`（对外契约，全部加性）

```java
// 新增：重载结果
public final class ConfigReloadResult {
    public static ConfigReloadResult ok();
    public static ConfigReloadResult ok(String detailJson);
    public static ConfigReloadResult failed(String reason);
    public static ConfigReloadResult failed(String reason, String detailJson);
    public boolean isOk();
    public String reason();          // 可读原因，成功时可空
    public String detailJson();      // 插件自定结构化详情，可为空
    // 自报实际加载的事实，供控制面校验「回执成功但版本不符」
    public ConfigReloadResult withAppliedMd5(String md5);
    public ConfigReloadResult withAppliedVersion(String version);
    public ConfigReloadResult withAppliedCount(int count);
}
```

```java
// 新增：可上报结果的监听器（继承既有接口，老实现不受影响）
public interface ReportingConfigChangeListener extends ConfigChangeListener {
    ConfigReloadResult onConfigChangedReporting(Set<String> changedDataIds, String newMd5);

    /** 默认实现委托给上报方法并丢弃结果，保证既有 void 语义可继续工作。 */
    @Override
    default void onConfigChanged(Set<String> changedDataIds, String newMd5) {
        onConfigChangedReporting(changedDataIds, newMd5);
    }
}
```

关键：`ReportingConfigChangeListener extends ConfigChangeListener`，因此
`EffectiveConfigView` 内部 `ConcurrentHashMap<Any, ConfigChangeListener>` 的类型**不变**，
老插件注册路径零改动。

### 3.2 `agent-core`：`EffectiveConfigView.fireChanged` 改造

```kotlin
fun fireChanged(changedDataIds: Set<String>, newMd5: String): ConfigReloadReport
```

- 遍历监听器，逐个执行；每个**独立 `try/catch`**。
- **必须 catch `Throwable` 而非 `Exception`**：插件侧 `NoClassDefFoundError` / `LinkageError`
  在 `agent-api` 版本不匹配时很常见，只兜 `Exception` 会让异常逃逸并打死调用线程。
- 每个监听器**独立超时**（`settings.configReloadTimeoutMs`，**默认 0 = 不启用**）：
  在专用执行器提交、`Future.get(timeout)` 取结果，超时则 `cancel(true)` 并记 `timeout` 失败。
  超时线程不得是 MC 主线程（沿用 core 既有「阻塞 IO 一律 async」纪律）。

  > **默认 0 是刻意的取舍，不是遗漏**：改默认值会让所有既有部署突然多出一组线程，
  > 属于无声行为变更。代价是「开箱即用时没有超时保护」——一个卡死插件仍会卡住
  > 交付命令线程，只能等控制面 `activate_timeout_sec` 整体超时，回执里看不出是哪个插件。
  > **运维须显式配置 `delivery.config-reload-timeout-ms` 才能获得该保护**，
  > 且该值必须小于控制面的 `activate_timeout_sec`，否则控制面先判整体超时。
- 返回聚合报告：

```kotlin
data class ConfigReloadReport(
    val changedDataIds: Set<String>,
    val newMd5: String,
    val results: List<PluginReloadOutcome>,   // 逐个监听器的结果
) {
    val hasFailures: Boolean get() = results.any { !it.ok }
}

data class PluginReloadOutcome(
    val listener: String,      // listener.javaClass.name，失败定位必用
    val ok: Boolean,
    val timeout: Boolean,
    val reason: String,        // 脱敏后可读原因
    val detailJson: String,
    val appliedMd5: String,
    val appliedVersion: String,
    val appliedCount: Int,
)
```

### 3.3 `PlatformAdapter.publishConfigChanged` 改返回报告（不抛异常）

**这是本规格相对初版方案的关键修正。** 经核实，`publishConfigChanged` 有**两个**调用方：

| 调用方 | 路径 | 抛异常的后果 |
|---|---|---|
| `DeliveryCommandExecutor.publishConfigChanged`（:476） | 交付生效 | ✅ 已有 catch → 回执 `failed`，符合预期 |
| `ConfigApplier.apply`（`config/ConfigApplier.kt:47`） | **周期配置同步**（非交付） | ❌ 异常会打进同步循环，可能中断后续配置应用 |

因此**不采用「抛异常」**，改为返回报告：

```kotlin
// PlatformAdapter.PlatformControl
fun publishConfigChanged(changed: Set<String>, newMd5: String): ConfigReloadReport
```

两个调用方各自显式处理：

- **交付路径**（`DeliveryCommandExecutor.publishConfigChanged`）：
  ```kotlin
  val report = adapter.publishConfigChanged(changed, configArtifactMd5(configFiles))
  if (report.hasFailures) {
      failResultWithState(orderId, phase, changedFileCount, backupPresent, error = buildReloadFailureError(report))
      return false
  }
  true
  ```
  复用既有 `failResultWithState`（:479）的部分成功语义，错误文案保留
  「文件已落盘（本单变更已生效于磁盘），仅配置变更通知失败：…」前缀，再接逐插件明细。

- **周期同步路径**（`ConfigApplier.apply`）：记录 `adapter.error(...)` 明细后**仍返回 `true`**
  （有效配置本身已生效、快照已写；插件重载降级不应让同步循环失败）。是否另行告警见 §7 待定。

签名变更波及测试桩：`RecordingPlatformAdapter`、`ThreadPoolPlatformAdapter`、`ManualAsyncAdapter`、
`SchedulingTestSupport` 等约 10 处 `override`，均为机械改动（返回 `ok` 空报告）。

### 3.4 回执报文（加性）

`DeliveryStageReport`（`DeliveryManifests.kt:126`）新增 `reload: ReloadReportPayload?`
（默认 `null`，保持既有构造点不变），`BeaconApiDelivery.kt:74` 的 `buildMap` 增加一段：

```json
{
  "phase": "activate",
  "status": "failed",
  "changedFileCount": 3,
  "skippedFileCount": 0,
  "backupPresent": true,
  "error": "文件已落盘（本单变更已生效于磁盘），仅配置变更通知失败：1/3 插件重载失败：<插件> schedule.json 解析失败",
  "reload": {
    "total": 3,
    "okCount": 2,
    "failedCount": 1,
    "timeoutMs": 30000,
    "plugins": [
      { "listener": "top.wcpe.allin.bp.ReloadHook", "status": "ok",
        "appliedMd5": "…", "appliedVersion": "v7", "appliedCount": 12 },
      { "listener": "top.wcpe.allin.aie.ReloadHook", "status": "failed",
        "reason": "schedule.json 解析失败：unexpected token at line 8",
        "detailJson": "" }
    ]
  }
}
```

- `reload` 段缺失（老 agent）→ CP 按「无重载信息」处理，行为等价改造前。
- 只在 `phase ∈ {activate, rollback}` 时携带；`upload` / `push` 阶段为 `null`。

### 3.5 控制面：接收、落库、告警

**接收**（`handler/delivery_agent_handler.go:63`）：`deliveryResultRequest` 加
`Reload *reloadReportDTO `json:"reload"``（指针，缺省 `nil`），与既有字段一同映射进
`service.DeliveryResultInput`（`delivery_blob_agent.go:356`）新增同名字段。

**落库**：`change_target` 新增可空列 `reload_report TEXT`（JSON 文本；**禁 JSON 列类型**，
守 DB 可移植），存原始 `reload` 段供前端展开。AutoMigrate 加列必须带默认（可空即可），
不写 ENUM / 分区语法。

**状态机**：不新增状态。重载失败走既有 `failed` 路径
（`delivery_orchestrator_advance.go` 的 `activating → failed`），随后由既有
失败率 / 健康恶化熔断停推后续批次。**不做自动回滚**。

**版本核对（后续补入，已实现）**：生效回执 `success` 时控制面并不直接判成功，而是用
`delivery_config_artifact` 重算「配置工件整体摘要」并与插件自报 `appliedMd5` 比对，
不一致即判 `failed` 并落可读原因「回执成功但版本不符：插件 X 自报 md5=… ≠ 期望 …」。

- 重算入口 `DeliveryBlobService.ExpectedConfigArtifactMD5`，算法与 agent
  `DeliveryCommandExecutor.configArtifactMd5` **逐字一致**（按 path 升序拼接 `path "\n" sha256 "\n"` 后取小写 md5）。
- **刻意不比 `EffectiveService` 的有效配置 md5**：那是**周期同步**通道交给插件的值
  （`ConfigApplier` 传 `result.md5`）；交付通道交给插件的是工件摘要（`configArtifactMd5`）。
  两者语义不同，用错一侧会把按契约回显 `newMd5` 的合规插件**系统性判成不符**。
- 三条不叠加/放行边界：插件已报 `failed`（原因归插件）、插件未自报 `appliedMd5`（无从核对）、
  期望值不可得（无工件 / 查库失败 / 未装配）——一律不判不符，后者记 WARN 降级放行。
- 比对实现见 `reconcileActivatingByAck` 的 `done` 分支与 `reloadMD5MismatchReason`。

**告警**：复用已预留枚举 `model.AlertEventTypePublishFail = "publish-fail"`（`enums.go:780`，
注释「预留枚举，当前无触发点」）。改动：

1. 扩展 `alert.Alert`（`alert.go:13`）使其承载非健康事件：新增 `Type string` 与
   `Detail map[string]string`（或等价通用字段），`PrevStatus` / `Status` 对交付类事件留空。
   既有健康路径行为不变（`PersistAlerter` 显式写 `health-transition`）。
2. 新增 `DeliveryFailAlerter`：`Notify` 映射为 `AlertEvent{Type: publish-fail, Level: warning,
   ServerID, Namespace, Message: "<orderId> 重载失败：N/M 插件", Detail: 逐插件明细 JSON}`。
3. 接线点：交付编排器在 `change_target` 落 `failed` 后扇出（**注意：编排器当前无任何告警引用**，
   需新注入 `*alert.Dispatcher`；DB IO 必须在锁外，沿用既有纪律）。
4. **`convergesOnRecord`（`alert_event_service.go:238`）保持不收录 `publish-fail`** ——
   交付失败必须逐条可见、逐条处置，收敛会掩盖问题。

### 3.6 前端

- **交付单详情**：目标行失败原因可展开 `reload` 明细（逐插件状态、原因、appliedVersion）。
  数据来自既有 targets 端点新增的 `reloadReport` 字段；渲染在 `target-status-wall.tsx` 的
  `ReloadOutcomeCell`（`<details>` 展开，三态区分：null=无重载信息 / 全成功 / 有失败）。
- **告警页**：`publish-fail` 类型文案与筛选**既有已具备**（`alert-events.tsx` 的 `TYPES` 与
  `i18n/observability.ts` 的 `publish-fail: '发布失败'`），本轮无需改动；告警 detail 含
  orderId 与逐插件清单，经既有 `detail` 展示。
- 走 `packages/ui` 既有控件，不新建通用控件（守 UI 一致性铁律）。

> **实现状态（2026-10-19）**：已完成并通过前端质量门。
> - 契约：`packages/contracts` 新增 `ChangeReloadReport` / `ChangeReloadPluginOutcome` 与
>   `ChangeTarget.reloadReport`；`packages/devmock` 的 mock 目标补齐该字段（失败台给出真实形状明细，
>   供演示展开）。
> - 渲染：`target-status-wall.tsx` 新增 `ReloadOutcomeCell`（`<details>` 展开，三态区分
>   null / 全成功 / 有失败；`appliedCount` 未自报显示「未自报」而非 0）。
> - 文案：中英文各 13 个键，键路径与代码逐一对齐。
> - **门禁**：`pnpm typecheck` 5/5、`pnpm lint` 5/5、`pnpm test` 5/5（web 580 例 + devmock 118 例全通过）、
>   `pnpm build` 5/5。

## 4. UX / 交互

- **用户任务**：运营发布配置 → 观察发布结果 → 失败时立刻知道「哪台服的哪个插件、为什么失败」。
- **进入路径**：交付域 → 变更单详情 → 目标列表 → 展开失败行（告警页为旁路入口）。
- **操作闭环**：看（批次进度 + 目标状态）→ 做（暂停 / 终止 / 回滚）→ 验证（`activated` 与
  `reload` 明细齐备才算真成功）。
- **状态设计**：`reload` 段缺失显示「无重载信息（agent 版本较旧）」而非空白；
  空态 / 加载 / 错误沿用交付域既有规范；`plugins` 列表可能较长，需虚拟化或折叠。
- **IA 挂载**：不新增页面，仅在既有交付单详情与告警详情内扩展（无需 mockup 评审门）。

## 5. 任务拆分

- [ ] `EffectiveConfigView.fireChanged` 逐监听器隔离（catch `Throwable`）（**P0，可独立合入**）
- [ ] `agent-api`：`ConfigReloadResult` + `ReportingConfigChangeListener`
- [ ] `agent-core`：`fireChanged` 返回 `ConfigReloadReport` + 逐监听器超时（`configReloadTimeoutMs`）
- [ ] `PlatformAdapter.publishConfigChanged` 改签名返回报告；Bukkit / Bungee 两壳实现 + 约 10 处测试桩
- [ ] `DeliveryCommandExecutor.publishConfigChanged`：报告有失败 → `failResultWithState`
- [ ] `ConfigApplier.apply`：报告有失败 → `adapter.error` 但不抛（同步循环不中断）
- [ ] `DeliveryStageReport` + `BeaconApiDelivery` 加 `reload` 段
- [ ] CP：`deliveryResultRequest` / `DeliveryResultInput` / `change_target.reload_report` 落库
- [ ] 告警：扩展 `alert.Alert` + `DeliveryFailAlerter` + 编排器接线（`publish-fail`，不收敛）
- [ ] 前端：交付单详情展开 reload 明细 + 告警页 `publish-fail` 文案与筛选
- [ ] 文档同步：PRD 状态、ARCHITECTURE（§5 通信 / §7 表）、API、CHANGELOG

## 6. 验收标准

- [ ] **隔离**：一个监听器抛异常，其余监听器仍收到通知（对照改造前会静默丢失）。
- [ ] **一票否决**：一个插件返回 `failed` → 目标 `change_target` 置 `failed`，批次熔断不再推下一批，
      **不出现** `activated`。
- [ ] **明细可见**：`error` 与 `reload` 段含插件标识与可读原因；前端交付单可展开查看。
- [ ] **告警**：`alert_event` 出现 `type=publish-fail` 行；**连续 N 次失败产生 N 行**（未被收敛合并）。
- [ ] **超时**：单插件卡死时，在 `activate_timeout_sec` 之前判定失败，并指明超时插件；
      其余插件结果仍被采集。
- [ ] **向后兼容（老插件）**：仅实现 `onChange` 的插件零改动可编译、可运行，行为不变。
- [ ] **向后兼容（老 agent + 新 CP）**：回执无 `reload` 段时不报错，目标按既有规则推进。
- [ ] **向后兼容（新 agent + 老 CP）**：新字段被忽略，不报 400。
- [ ] **回滚路径**：`runRollback` → `runHotReload(phase=rollback)` 同样产出 reload 报告并按失败处置。
- [ ] **周期同步不中断**：非交付路径（`ConfigApplier`）在监听器失败后仍继续后续配置应用。
- [ ] **DB 可移植**：新列在 MySQL / sqlite 上行为一致，无 ENUM / JSON 列 / 分区语法。

## 7. 风险 / 待定

1. **`configReloadTimeoutMs` 默认值待定**：建议 30000，但**必须小于实际 `activate_timeout_sec`**
   （若后者配置得更小，前者须同比下调）。实施前需确认 `activate_timeout_sec` 的线上取值。
2. **周期同步路径是否也应告警**：本规格只记 `error` 日志、不产告警（避免与交付告警混淆）。
   若运维希望「非交付的配置漂移」也告警，应另立规格。
3. **`PlatformAdapter` 签名变更的影响面**：约 10 处测试桩需机械改动；因不涉及外部契约，
   不需要新的 Maven 坐标版本。但 `agent-api` 的新增类型属公开契约，需随下个 `X.Y.Z` 发布。
4. **超时线程模型**：监听器在专用执行器上跑，若插件内部又切回 MC 主线程并阻塞，
   超时只能取消 `Future`，**无法强杀**已进入主线程的代码。此时表现为「agent 判定超时失败」
   但主线程仍卡——需要在插件侧遵守「重载不阻塞主线程」的纪律，agent 侧只能尽力而为。
5. **`appliedVersion` 校验是否本期落地**：`ConfigReloadResult` 预留了字段，但「回执成功但版本不符」
   的判定建议放到后续（需控制面知道每个 config_file 的期望版本语义），本期只透传展示。
   > **后续进展**：`appliedMd5` 一路已落地（见 §3.5「版本核对」），取的是**交付工件摘要**口径；
   > `appliedVersion` 仍是纯透传——「每个 config_file 的期望版本语义」尚未定义，语义未定前不做判定。
6. **多插件同 `dataId` 交叉影响**：两个插件订阅同一配置项时，一个 reload 成功、另一个失败的组合
   仍按一票否决处理；运维需从明细里分别判断，不提供自动区分。
7. **`appliedMd5` 语义边界（v2 核对引入）**：核对只认「回显 agent 交给插件的 `newMd5`」这一种语义。
   插件若自报别的 md5（如自算加载内容哈希），会被判不符而目标失败——这是刻意把契约显式化；
   `appliedMd5` 属新增可选上报、生产插件尚未使用，故误判面可控。
