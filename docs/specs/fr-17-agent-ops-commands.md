# 功能规格：agent 本地运维命令（FR-17，仅本地）

> 状态：开发中　·　关联 PRD：FR-17　·　分支：feature/fr-17-agent-ops-commands

## 1. 背景与目标

运维在排障时常需在子服侧手动触发 agent 动作：看 agent 接没接上控制面、强制立刻重拉一次有效配置而不等长轮询超时、打断退避立刻重连。本期（属 P2）先落 **agent 壳层本地命令**这一最小、最可能独立交付的子集；**远程下发**（控制面经鉴权向 agent 推命令）依赖 FR-11 鉴权，本轮不做。

## 2. 需求（要什么）

范围内：
- agent-bukkit / agent-bungee 各注册一个根命令 `beacon`（权限 `beacon.admin`），含三个子命令：
  - `status`：打印 AgentState、connected、有效配置 md5、心跳周期、控制面 endpoint。
  - `reload`：强制立刻重拉一次有效配置并 apply（md5=null 旁路 304），不等长轮询超时。
  - `reconnect`：打断退避、重置、重新接入控制面。
- core 在 `AgentLifecycle` 新增**幂等、线程安全**的控制方法：
  - `reconnectNow()`：重置退避并重新注册接入；**不清空 store/快照**（保 fail-static）。
  - `forcePollNow()`：以 md5=null 强制重拉一次有效配置并 apply（复用 ConfigApplier 幂等守卫）。
  - `snapshot()`：把当前可观测状态（state/connected/md5/心跳周期/endpoint）打包给壳层 status 用。

不做（范围外）：
- `resync`（强制重同步文件树）首版仅占位（依赖通道B FR-14，未合时回"文件树子系统未启用"）；待 FR-14 落地后补实际接线（见 §3「resync 接通」）。
- `offline`/`online`：与健康 TTL 同形会造成可观测盲区且语义冲突，本轮**先不做**，避免镀金。
- 远程下发命令（依赖 FR-11 鉴权）。

## 3. 设计（怎么做）

### core 控制入口（AgentLifecycle）

- `reconnectNow()`：重置 register/poll 退避 → 递增循环代标识使旧循环退出 → 走单飞注册入口重新接入。不动 store/snapshot。
- `forcePollNow()`：异步执行一次 `pollEffective(identity, md5=null, requestTimeoutMs)`，200 则经 `applier.apply` 落地（幂等守卫天然去重），不接管长轮询主循环、不影响其代标识。
- `LifecycleSnapshot`：值对象（state/connected/effectiveMd5/heartbeatIntervalSec/endpoint），壳层 status 渲染用。core 不持有 Bukkit/Bungee 类型（守 ADR-0005）。

### 并发单飞收口（修对抗审查发现的坑）

现状 `registerThenStartLoops()` 有多个并发触发点（heartbeat-404、poll-404、退避重试，叠加新增 reconnectNow），无单飞保护时会瞬时双注册、双循环。

- 新增 `AtomicBoolean registering` 单飞门：`registerThenStartLoops()` 先 CAS 抢占，抢到才发 register→启循环，没抢到直接 no-op；register 终局（成功启循环 / 安排退避重试前）释放门。
- 新增 `registerGen`：reconnectNow / 各 NotRegistered 触发时递增；延迟重试携带触发时的 gen，fire 时 gen 不符则自我作废——杜绝旧退避链与新接入链并存。
- 不变量：任意时刻只有一条 `register→loops` 在飞（被测断言）。

### 壳层命令（agent-bukkit / agent-bungee）

- 用 TabooLib `command("beacon") { ... }` DSL 运行期注册（权限 `beacon.admin`）。
- 命令体经 `adapter.runAsync { ... }` 落异步线程执行 core 控制方法，**不在 MC 主线程做阻塞动作**（守 ADR-0005 与不阻塞主线程不变量）；回显文案中文。

### resync 接通（FR-14 落地后补）

- `forceSyncFileTreeNow()`：以 `fileTreeMd5=null` 异步执行一次 `pollFileManifest`，200 则经 `FileTreeApplier.apply` 幂等差分落盘（与 `forcePollNow` 同形：旁路长轮询 304、不接管主循环、不改其代标识）；文件树子系统未启用（`fileTreeApplier` 为 null）时返回 false。
- `resync` 子命令调用 `forceSyncFileTreeNow()`：已触发回"已触发文件树重新同步"，未启用回"文件树子系统未启用（请开启 `file-tree.enabled`）"。

## 4. 任务拆分
- [ ] core：`LifecycleSnapshot` + `reconnectNow` + `forcePollNow` + register 单飞门 + registerGen
- [ ] core 并发单测：连续 reconnect、reconnect 与正常 poll 并发、register 单飞不变量
- [ ] 壳：agent-bukkit / agent-bungee `beacon` 命令（status/reload/reconnect/resync 占位）
- [ ] 文档同步：PRD 状态、ARCHITECTURE §8、CHANGELOG

## 5. 验收标准
- core 全测试绿（含并发单测）：连续多次 reconnect 不出现并行 register；reconnect 与 poll 并发下 register 单飞不变量成立。
- `forcePollNow` 在有变更时经幂等守卫 apply 一次、md5 相同时不重复广播。
- `reconnectNow` 不清空 store/快照（fail-static 不被破坏）。
- 双端壳编译通过、命令注册不依赖 FR-11。

## 6. 风险 / 待定
- TabooLib 命令执行线程模型因平台而异；统一经 `runAsync` 下沉异步，回显在异步线程发送（TabooLib 的 sender 发送线程安全）。
