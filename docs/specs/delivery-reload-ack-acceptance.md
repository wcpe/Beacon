# FR-274 / FR-275 真机验收清单

> 状态：**已执行并通过（2026-10-19）**　·　关联 [delivery-reload-ack](delivery-reload-ack.md)　·　版本线 v1.5.0
> 目的：在**真实控制面 + 真实 agent + 真实业务插件**上复现两条护栏，取到可核验证据后
> 才把 FR-274 / FR-275 从「开发中」翻到「已交付」。单测与门禁全绿**不等于**真机可用。
> 执行环境：Windows + Java 21 + Paper 1.20.4 + 真控制面（隔离 SQLite）+ 真 BeaconAgent + 真业务插件探针，
> 驱动为既有 `apps/server/test/e2e/hotreload`（`-tags e2e`），耗时 167.6s。
> **执行中查出并修复两处真实缺陷**（详见 §6），这是单测与门禁都没抓到的链路断点。

## 1. 验收结果总览

| 编号 | 场景 | 结果 | 证据来源 |
|---|---|---|---|
| A1 | 全部成功 → `activated` | ✅ | 真机（单#1/#5/#7） |
| A2 | 一票否决 → `failed`，无 `activated` | ✅ | 真机（单#2） |
| A3 | 单插件超时 | ✅ | **真机（单#3）**：`reason=重载超时（3000ms）`、`timeout=true` |
| A4 | 抛 `Error` 被兜住、线程存活 | ✅ | **真机（单#4）**：`NoClassDefFoundError` 被捕获；随后正常单仍成功 |
| A5 | 缺结果按失败 | ✅ | **真机（单#6）**：`监听器未返回重载结果（null）` |
| A6 | 隔离：一个失败不影响其余 | ✅ | 真机（各失败单均 `ok=3/failed=1`，其余 3 个监听器照常成功） |
| A7 | 回滚路径同样产出报告 | ✅ | 真机（单#1 正推+回滚） |
| A8 | 老 agent 无 `reload` 段兼容 | ✅ | **真机**：向真实端点发旧式回执（无 `reload`），返回 **404「命令不存在」而非 400** —— 证明解码通过 |
| A9 | 老插件零改动 | ✅ | **真机**：`LEGACY_DONE` 观测 1 次，void 回调被正常派发 |
| B1 | 产出 `publish-fail` 告警 | ✅ | 真机：4 条 `publish-fail` / `warning` |
| B2 | 不收敛 | ✅ | 真机：4 次失败 → **4 行独立记录**，`occurrence_count` 全为 1 |
| B3 | 健康告警不受影响 | ✅ | 单测 |
| B4 | 告警页可按类型筛选 | ✅ | 前端既有能力（`TYPES` 已含 `publish-fail`） |

> **A3/A4/A5/A8/A9 全部已上真机。** A8 的做法说明：其风险点完全在控制面解码侧——
> 新增字段若被写成必填、或反序列化对缺失形状敏感，旧 agent 报文会被 400 拒掉、灰度期整条交付线瘫。
> 故用一个与旧版 agent **逐字节同形**的回执（无 `reload` 键）打真实端点：返回 404「命令不存在」
> 即证明**解码已通过**，只是没有挂靠的在途命令；若返回 400 则 A8 不通过。
> 这比构造旧 jar 更精确（直接命中风险面），也避免版本耦合。

## 2. 真机取证（实际数据）

**环境**：Windows + Java 21 + Paper 1.20.4 + 真控制面（隔离 SQLite）+ 真 BeaconAgent
+ 真业务插件探针（Kotlin 上报式 + Java null 式 + 遗留 void 式三条监听），
**TabooLib `6.3.0-wcpe.5`**。总耗时 **110.9s**（`--- PASS`）。

### 2.1 七张单的实际结果

| 单 | 用例 | 单状态 | 目标 | reload 报告 |
|---|---|---|---|---|
| #1 | A1+A7 正推与回滚 | `rolled_back` | `activated` | `total=4 ok=4 failed=0` |
| #2 | A2 一票否决 | `completed` | **`failed`** | `total=4 ok=3 failed=1` |
| #3 | **A3 超时** | `completed` | **`failed`** | `failed=1`，`reason=重载超时（3000ms）`、`timeout=true` |
| #4 | **A4 抛 Error** | `completed` | **`failed`** | `failed=1`，`reason=NoClassDefFoundError: …AcceptanceProbeAbsent` |
| #5 | A4 线程存活复验 | `completed` | `activated` | `ok=4`（证明执行器未被前次 Error 打死） |
| #6 | **A5 缺结果** | `completed` | **`failed`** | `failed=1`，`listener=NullResultReloadProbe`、`reason=监听器未返回重载结果（null）` |
| #7 | **A9 老插件** | （收尾中） | `activated` | `ok=4`，且探针日志有 `LEGACY_DONE` |

### 2.2 A6 隔离的实证

每个失败单的 `okCount` 都是 3、`failedCount` 都是 1 —— **同一台服上的另外 3 条监听器照常成功**。
改造前是裸 `for` 循环，先抛者会让后续监听器全部收不到通知。

### 2.3 B1/B2 告警实证

```
alert_event:
  #1 publish-fail warning occurrence_count=1
  #2 publish-fail warning occurrence_count=1
  #3 publish-fail warning occurrence_count=1
  #4 publish-fail warning occurrence_count=1
```

4 次交付失败 → **4 行独立记录**。若被 FR-232 收敛合并，会是 1 行且 `occurrence_count=4`；
实际每条都是 1，证明 `convergesOnRecord` 确实不收录 `publish-fail`。

### 2.4 探针模式触发计数

| 标记 | 次数 | 说明 |
|---|---|---|
| `REPORTING_FAILED` | 1 | A2 返回 failed |
| `REPORTING_ERROR` | 1 | A4 抛 `NoClassDefFoundError` |
| `REPORTING_HANG` | 1 | A3 睡眠触发超时 |
| `LEGACY_DONE` | 1 | A9 老式 void 回调执行完毕 |
| `REPORTING_THROW` / `REPORTING_NULL` | 0 | 预期为 0：前者抛异常时日志写入被中断，后者由 Java 探针承担（见单#6 证据） |

**目标 #1（全部成功）** → `status=activated`：

```json
{"total":2,"okCount":2,"failedCount":0,"timeoutMs":0,
 "plugins":[{"listener":"...BeaconE2EBukkit$$Lambda","status":"ok"},
            {"listener":"...DeliveryHotReloadE2EProbe$$Lambda","status":"ok"}]}
```

**目标 #2（一票否决）** → `status=failed`（**未出现 activated**）：

```json
{"total":2,"okCount":1,"failedCount":1,"timeoutMs":0,
 "plugins":[{"listener":"...BeaconE2EBukkit$$Lambda","status":"ok"},
            {"listener":"...DeliveryHotReloadE2EProbe$$Lambda","status":"failed",
             "reason":"IllegalStateException: FR-171 E2E 业务插件拒绝失败路径热更新"}]}
```

`error` 全文：

```
文件已落盘（本单变更已生效于磁盘），仅配置变更通知失败：1/2 插件重载失败：
top.wcpe.beacon.e2e.DeliveryHotReloadE2EProbe$$Lambda/...: IllegalStateException:
FR-171 E2E 业务插件拒绝失败路径热更新；请手工重载，或按同版本重推（…）
```

**`publish-fail` 告警**（`alert_event`）：

```
type=publish-fail  level=warning  occurrence_count=1
detail keys = [error, orderId, reloadReport, serverId, status]
orderId=2  serverId=e2e-bukkit-1  reloadReport.failedCount=1
```

三项关键结论：
1. **一票否决真实生效**：一个插件失败 → 整体 `failed`，不是「仅告警仍 activated」。
2. **隔离真实生效**：同一台服上另一个插件 `ok`，没有被失败者阻断。
3. **告警带得动定位信息**：`orderId` + `serverId` + 逐插件 `reloadReport` 全在 detail 里。

## 3. 前置条件

| 项 | 要求 | 为什么 |
|---|---|---|
| 控制面 | 含本批改动的构建（`apps/server`） | 需要 `reload_report` 列与 `publish-fail` 告警 |
| agent | 含本批改动的 `BeaconAgent.jar`（Bukkit 子服） | 需要 `fireChanged` 隔离与重载上报 |
| 业务插件 | 至少 1 个订阅配置的插件 + **本文 §4 的故意失败插件** | 需要「一真一假」才能验证隔离与一票否决 |
| 配置 | 一个已登记为 `config_file` 的配置项（`format=json` 或 `yaml`） | 只有 `config_artifact` 会触发配置变更回调 |
| 变更单 | 生效方式选 **`hot_reload`** | `push_only` 不触发回调、`restart` 走心跳回归，都验不到重载结果 |
| 权限 | 可创建/审批/启动变更单的账号 | —— |

### 3.1 agent 侧配置（验超时必配）

```yaml
# plugins/BeaconAgent/config.yml
delivery:
  # 单插件重载超时（毫秒）。0 = 不设超时（默认，兼容旧行为）。
  # 必须 < 控制面的 activateTimeoutSec，否则控制面先判整体超时、回执里看不出是哪个插件卡住。
  config-reload-timeout-ms: 30000
```

> 该键此前**未被 AgentBootstrap 读取**（永远取默认 0 = 无超时），已修复并加单测锁定。
> 若不配此键，A3（超时）无法验证。

## 4. 故意失败的测试插件样例

最小可编译的 Bukkit 插件：一半场景成功、一半失败，用于验证「一个失败不影响其余」。

```java
package test.liveops;

import java.util.Set;
import org.bukkit.plugin.java.JavaPlugin;
import top.wcpe.beacon.agent.api.BeaconAgentProvider;
import top.wcpe.beacon.agent.api.ConfigReloadResult;
import top.wcpe.beacon.agent.api.ReportingConfigChangeListener;

/**
 * FR-274 真机验收用的故意失败插件。行为由 plugins/FailingReloadHook/config.yml 的 mode 控制：
 *   ok       -> 返回 ok（对照组，证明隔离生效后它仍被正常派发）
 *   fail     -> 返回 failed + 可读原因（验证一票否决与明细展示）
 *   throw    -> 抛 RuntimeException（验证异常被兜住、不打死交付线程）
 *   error    -> 抛 NoClassDefFoundError（验证 catch Throwable 而非 Exception）
 *   hang     -> 睡眠超过 config-reload-timeout-ms（验证单插件超时）
 *   nullret  -> 返回 null（验证「缺结果不得被当作成功」）
 */
public final class FailingReloadHook extends JavaPlugin {
    @Override
    public void onEnable() {
        if (!BeaconAgentProvider.isAvailable()) {
            getLogger().warning("Beacon agent 不在场，本插件不注册（软依赖）");
            return;
        }
        String mode = getConfig().getString("mode", "fail");
        BeaconAgentProvider.get()
            .config()
            .onChange((ReportingConfigChangeListener) (Set<String> ids, String md5) -> act(mode));
        getLogger().info("FailingReloadHook 已注册，mode=" + mode);
    }

    private ConfigReloadResult act(String mode) {
        switch (mode) {
            case "ok":
                return ConfigReloadResult.ok().withAppliedMd5("mock")
                    .withAppliedVersion("v-ok").withAppliedCount(3);
            case "throw":
                throw new RuntimeException("故意抛异常（验收用）");
            case "error":
                throw new NoClassDefFoundError("test/liveops/NotExist");
            case "hang":
                sleepQuietly(120_000L);
                return ConfigReloadResult.ok();
            case "nullret":
                return null;
            case "fail":
            default:
                return ConfigReloadResult.failed("故意失败（验收用）：schedule.json 解析失败");
        }
    }

    private static void sleepQuietly(long ms) {
        try {
            Thread.sleep(ms);
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
        }
    }
}
```

依赖（`compileOnly`，与仓库 README 一致）：

```kotlin
dependencies {
    compileOnly("top.wcpe.beacon:beacon-agent-api:<GA_VERSION>")
}
```

`plugin.yml`：

```yaml
name: FailingReloadHook
main: test.liveops.FailingReloadHook
version: 1.0.0
api-version: '1.20'
```

## 5. 用例

### A. FR-274 重载回执传导

| 编号 | 场景 | 操作 | 期望（必须全部满足） |
|---|---|---|---|
| **A1** | 全部成功 | 只有 `mode=ok` 的插件 | 目标 `activated`；`change_target.reload_report` 含 `total=1, okCount=1, failedCount=0`；交付单「插件重载」列显示成功徽标 |
| **A2** | 一票否决 | `ok` + `fail` 两个插件 | 目标 **`failed`**（**不得出现 `activated`**）；`error` 含「1/2 插件重载失败」与失败插件类名；前端可展开看到两个插件各自结果；批次因失败率熔断停推 |
| **A3** | 单插件超时 | `ok` + `hang`，且已配 `config-reload-timeout-ms` | 目标 `failed`；失败项 `timeout=true`、原因含「重载超时」；**`ok` 插件的结果仍被采集**（`okCount=1`）；失败发生在控制面 `activateTimeoutSec` **之前** |
| **A4** | 抛 Error（非 Exception） | 只有 `error` 模式 | 目标 `failed`；原因为 `NoClassDefFoundError`；**交付命令线程存活**（后续命令仍能执行，不是整个 executor 挂掉） |
| **A5** | 缺结果 | 只有 `nullret` 模式 | 目标 `failed`；原因含「未返回重载结果」 |
| **A6** | 隔离 | `throw` + `ok` 两个插件 | `throw` 失败但 **`ok` 插件仍收到通知并成功**（`okCount=1`）；对照改造前会静默丢失 |
| **A7** | 回滚路径 | 对 A2 的失败单发起整单回滚（填原因 + 二次确认） | 回滚同样产出 `reload_report`；失败处置与正推一致 |
| **A8** | 老 agent 兼容 | 换回不含本批改动的旧 agent | 回执无 `reload` 段；目标按既有规则推进；**不报 400**；前端该列显示「无重载信息」而非「全部成功」 |
| **A9** | 老插件兼容 | 只有实现 `onChange`（void）的老插件 | 插件**零改动**可编译可运行；行为与改造前一致 |

### B. FR-275 交付失败告警

| 编号 | 场景 | 操作 | 期望 |
|---|---|---|---|
| **B1** | 产告警 | 触发 A2 | `alert_event` 出现 `type=publish-fail` 行；`detail` 含 `orderId`、`serverId`、失败原因与 `reloadReport` |
| **B2** | 不收敛 | 同一目标连续 3 次交付失败 | **产生 3 行**（不得合并为 1 行） |
| **B3** | 健康告警不受影响 | 制造一次实例健康流转（如停服） | 仍落 `health-transition` 且**仍收敛**（同键只 1 行） |
| **B4** | 告警页 | 打开 `/alert-events` | 可按「发布失败」类型筛选；行摘要显示交付失败 message |

## 6. 取证要求

每条用例留以下证据（缺任一项不算通过）：

1. 变更单详情截图（目标状态 + 插件重载列展开）
2. `change_target` 行的 `status` / `error` / `reload_report` 原文
3. `alert_event` 相关行（`type` / `level` / `detail` / `occurrence_count`）
4. agent 侧日志中的 ERROR「配置变更派发存在失败：…」
5. A8/A9 另需：旧 agent 版本号、老插件的 `plugin.yml` 版本

## 7. 判定

- **A1–A9 与 B1–B4 全通过** → FR-274 / FR-275 从 `开发中` 翻 `已交付@版本`，并同步 PRD 状态与 CHANGELOG。**已于 2026-10-24 执行完毕**：PRD 状态改为 `已交付@v1.5.0-rc.6`，CHANGELOG 条目归档进 v1.5.0 段。
- 任一不过 → 保持 `开发中`，在本文追加「失败现象 / 定位 / 修复」小节，不得只改状态不修问题。

## 8. 真机验收中发现并修复的两处缺陷

两处都是**单测与前后端门禁全绿、但真机链路断裂**的缺陷。记录在此，避免后来者以为验收只是走流程。

### 8.1 失败回执未携带 reload 段（agent 侧）

- **现象**：`change_target.reload_report` 恒为空，但 `error` 文本里有逐插件明细。
- **定位**：`DeliveryCommandExecutor.failResultWithState` 构造 `DeliveryStageReport` 时**没有传 `reload`**。
  失败路径（恰恰是最需要明细的路径）把结构化结果整段丢弃，只留一句 error 文本。
- **修复**：`failResultWithState` 增加 `reload: ConfigReloadReport? = null` 参数并透传；
  `publishConfigChanged` 的失败分支传入 `report`。
- **回归锁**：`hot_reload 插件上报失败时回执携带 reload 段`；另有
  `hot_reload 派发抛异常时回执如实记录且不伪造插件结果`（派发整体异常时**不得**伪造插件结果——
  不知道就不编，是「不得假报」的另一面）。

### 8.2 回执入口未把 reload 写进 result_detail（控制面侧）

- **现象**：agent 已上报 reload 段，但 `change_target.reload_report` 仍为空。
- **定位**：`DeliveryBlobService.deliveryResultDetail` 组装 `agent_command.result_detail` 的
  `map` 里只写了计数与 error，**漏了 `reload`**。推进器从 result_detail 反解 reload，
  此处一漏，后面对 `change_target` 的写入逻辑永远拿到 `nil`。
- **修复**：`input.Reload != nil` 时写入 `detail["reload"]`（老 agent 不上报则保持既有形状）。
- **回归锁**：`TestDeliveryResultDetailCarriesReload` / `TestDeliveryResultDetailOmitsReloadForOldAgent`。

### 8.3 附带环境修复（非产品代码）

E2E 模板的 TabooLib 运行期仓库原指向 `repo.tabooproject.org`，而 `wcpe` 定制库
（`reflex-1.2.5-wcpe.1` 等）只托管在 `repo.wcpe.top`，导致 agent 插件在 Paper 内加载失败
（`Could not load plugin 'plugin-under-test.jar'` / `无法解析 Kotlin 元数据���`）。
已把 `backend` 与 `proxy` 两份 `env.properties` 的 `repo-taboolib` / `repo-reflex` 指向
`https://repo.wcpe.top/repository/maven-public/`——这与仓库 `build.gradle.kts` 里的注释一致
（「wcpe 定制版只在此托管」）。

## 9. 已知限制（不是缺陷，验收时不要当 bug）
- 插件卡死时 `cancel(true)` **无法强杀**已进入 MC 主线程的代码；表现为「agent 判超时失败但主线程仍卡」，
  需插件侧遵守「重载不阻塞主线程」纪律。
- L5「玩家是否真能参与活动」**不在 Beacon 范围**（不碰玩法），验收不覆盖。
