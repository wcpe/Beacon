# ADR-0070：agent 优雅关服平台原语（restart 生效方式）

**状态**：已接受

## 背景

FR-171「生效编排」给变更单三种生效方式：`restart` / `hot_reload` / `push_only`（见 `docs/specs/v2-delivery-orchestration.md` §4.6）。其中 `restart` 是含 jar 替换的变更单的唯一可靠生效途径，语义为「agent 优雅关服 → 宿主自启脚本拉起进程 → agent 随进程重启后重新注册 / 心跳回归 → 控制面判定生效」。

要落地这条链路，有两个既有约束的空档：

- **agent 现无任何关服能力**。全仓找不到 `Bukkit.shutdown` / `ProxyServer.stop` / `save-all` / 广播-关服的调用；agent 只会注册、心跳、拉配置 / 命令、回执，没有「把本服优雅关掉」的动作。
- **[ADR-0011](0011-third-party-file-override-and-restricted-reload-command.md) 决策 2 明令禁进程 / shell 执行 API**：agent-core 与适配器不得引入 `Runtime.exec` / `ProcessBuilder`——物理上无法落到 OS shell，agent **不能自重启进程**。该约束是产品最高风险点（无鉴权 RCE）的锁死前提，不可放开。

于是需要一条机制，既能让 `restart` 生效方式真正生效、又不违 [ADR-0011](0011-third-party-file-override-and-restricted-reload-command.md)：agent 只负责「优雅关服」，进程重启交给宿主，生效判定交给控制面。

## 决策

1. **`PlatformAdapter` 新增 `gracefulShutdown(reason)` 平台原语**（默认空实现；core 不碰 Bukkit / Bungee / 进程 API，守 [ADR-0005](0005-agent-transport-codec-abstraction.md) 壳层模式，与既有 `dispatchConsoleCommand` 同构）：
   - **Bukkit 实现** = 主线程 `runSync { 广播关服提示 + world save-all + Bukkit.shutdown() }`（存档落盘后再停，避免丢档）。
   - **Bungee 实现** = `ProxyServer.getInstance().stop()`。

2. **restart 生效 = agent 优雅关服 + 宿主自启拉起，Beacon 不做进程管理**。目标 agent 收到 `delivery_activate`（`activation_method=restart`）后，先回执「开始生效」，再调 `gracefulShutdown`。进程重启依赖宿主机预置的自启脚本（docker `--restart` / systemd `Restart=` / 面板守护进程）拉起——agent 只负责关服，**不引入任何进程 / shell 执行 API、不自重启**，与 [ADR-0011](0011-third-party-file-override-and-restricted-reload-command.md) 决策 2「碰重启进程即越界进 P3 发布编排」一致。

3. **`activated` 判定归控制面，不由 agent 回执 activate 成功**。控制面在 `activate_timeout_sec`（默认 300s）内观测该 identity 心跳 / 指标批回归且状态 `online`，即判 `activated`（注册 / 健康真源 = Go 进程内存，架构不变量 #3）；超时未回归、或关服指令回执失败，判 `failed` 并计入熔断失败率。「关了没起来 = 失败」是本设计的**关键安全阀**（spec §4.6.1）——进程都关了，只有它重新注册才算数，控制面据真源内存判定最可靠。

4. **边界重申**：含 `.jar` 文件项的变更单必须选 `restart`；`hot_reload` 不保证 jar 被插件框架真正重载（Beacon 只负责落盘与配置热更），前端在组单时对「含 `.jar` + `hot_reload`」组合给显式警告（spec §4.6.1）。restart 强依赖宿主自启脚本，「关了没起来」即超时判失败并熔断止血——这是设计的安全阀，不是缺陷。

## 理由

- 优雅关服是数据面对自身的运维操作，与 [ADR-0011](0011-third-party-file-override-and-restricted-reload-command.md)「agent 收到新配置后自己 reload」同构；经受限平台原语落地，不落 OS shell、不碰进程管理，守住产品最高风险线（无鉴权 RCE / 进程编排越界）。
- 生效判定用「心跳回归」而非 agent 自报成功：进程已关，agent 无法可靠回执「我起来了」；控制面按注册 / 健康真源（Go 进程内存）判定，避免误判。
- 超时判 `failed` 并计入熔断，把「宿主没配自启 / 进程起不来」这类致命故障在灰度早期（首批小爆炸半径）暴露并自动止血，避免整批服务器被关光——这正是变更单灰度编排存在的意义。

## 后果

- `PlatformAdapter` 新增 `gracefulShutdown` 平台能力（core 默认空实现 + Bukkit / Bungee 双端实现）；双端 jar 需重建并真机部署。
- restart 生效**强依赖运维在宿主侧预先配好进程自启**（docker `--restart` / systemd `Restart=` / 面板守护）；未配则整批 restart 全 `failed`。此运维前置须写入 [`docs/OPERATIONS.md`](../OPERATIONS.md)，并作为真机验收的前置条件。
- 新增关服原语、心跳回归判定、超时熔断的测试与真机验收（关服后宿主拉起 → 心跳回归即 `activated`；超时未回归判 `failed` 并计入熔断失败率，对齐 spec §7 验收 16）。
- 新增生效编排契约需同步 [`docs/API.md`](../API.md) 与 [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md)。
- **不新增任何进程 / shell 执行 API**，[ADR-0011](0011-third-party-file-override-and-restricted-reload-command.md) 决策 2 的边界不变、不被放开。

## 备选方案

- **agent 自重启进程（`Runtime.exec` / `ProcessBuilder` 拉起新进程）**：最直接，但违 [ADR-0011](0011-third-party-file-override-and-restricted-reload-command.md) 决策 2（禁进程执行 API，物理锁死 RCE 面），把 agent 变成远程进程执行器。否决。
- **控制面远程进程管理（SSH / 面板 API 远程重启宿主进程）**：越界进「进程 / 发布编排」，新增凭据与远程执行面，违控制面「只存事实、不写游戏逻辑 / 不做编排执行」边界（架构不变量 #1）。否决。
- **agent 回执 activate 成功即判 `activated`（不等心跳回归）**：进程已关，agent 无法可靠回执「起来了」，且丢失「关了没起来」的安全阀。否决。
- **不做优雅关服、纯人工重启**：restart 生效无法自动化、无法判定、无法熔断，退化为纯手工运维，与灰度编排目标背离。否决。
