# FR-274 / FR-275 真机验收清单

> 状态：待执行　·　关联 [delivery-reload-ack](delivery-reload-ack.md)　·　版本线 v1.5.0
> 目的：在**真实控制面 + 真实 agent + 真实业务插件**上复现两条护栏，取到可核验证据后
> 才把 FR-274 / FR-275 从「开发中」翻到「已交付」。单测与门禁全绿**不等于**真机可用。

## 0. 前置条件

| 项 | 要求 | 为什么 |
|---|---|---|
| 控制面 | 含本批改动的构建（`apps/server`） | 需要 `reload_report` 列与 `publish-fail` 告警 |
| agent | 含本批改动的 `BeaconAgent.jar`（Bukkit 子服） | 需要 `fireChanged` 隔离与重载上报 |
| 业务插件 | 至少 1 个订阅配置的插件 + **本文 §1 的故意失败插件** | 需要「一真一假」才能验证隔离与一票否决 |
| 配置 | 一个已登记为 `config_file` 的配置项（`format=json` 或 `yaml`） | 只有 `config_artifact` 会触发配置变更回调 |
| 变更单 | 生效方式选 **`hot_reload`** | `push_only` 不触发回调、`restart` 走心跳回归，都验不到重载结果 |
| 权限 | 可创建/审批/启动变更单的账号 | —— |

### 0.1 agent 侧配置（必配，否则验不到超时）

```yaml
# plugins/BeaconAgent/config.yml
delivery:
  # 单插件重载超时（毫秒）。0 = 不设超时（默认，兼容旧行为）。
  # 必须 < 控制面的 activateTimeoutSec，否则控制面先判整体超时、回执里看不出是哪个插件卡住。
  config-reload-timeout-ms: 30000
```

> 该键此前**未被 AgentBootstrap 读取**（永远取默认 0 = 无超时），已在本次修复并加单测锁定。
> 若不配此键，用例 A3（超时）无法验证。

## 1. 故意失败的测试插件样例

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

## 2. 用例

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

## 3. 取证要求

每条用例留以下证据（缺任一项不算通过）：

1. 变更单详情截图（目标状态 + 插件重载列展开）
2. `change_target` 行的 `status` / `error` / `reload_report` 原文
3. `alert_event` 相关行（`type` / `level` / `detail` / `occurrence_count`）
4. agent 侧日志中的 ERROR「配置变更派发存在失败：…」
5. A8/A9 另需：旧 agent 版本号、老插件的 `plugin.yml` 版本

## 4. 判定

- **A1–A9 与 B1–B4 全通过** → FR-274 / FR-275 可从 `开发中` 翻 `已交付@版本`，并同步 PRD 状态与 CHANGELOG。
- 任一不过 → 保持 `开发中`，在本文追加「失败现象 / 定位 / 修复」小节，不得只改状态不修问题。

## 5. 已知限制（不是缺陷，验收时不要当 bug）

- `hot_reload` **不保证 jar 被真正重载**：含 jar 替换的变更单应选 `restart`（既有边界，未改变）。
- 插件卡死时 `cancel(true)` **无法强杀**已进入 MC 主线程的代码；表现为「agent 判超时失败但主线程仍卡」，
  需插件侧遵守「重载不阻塞主线程」纪律。
- L5「玩家是否真能参与活动」**不在 Beacon 范围**（不碰玩法），验收不覆盖。
