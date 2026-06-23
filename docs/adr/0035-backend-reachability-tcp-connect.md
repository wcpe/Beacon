# ADR-0035：后端可达性探测由 MC status-ping 改 TCP 连接（修订 ADR-0025）

**状态**：已接受

## 背景

[ADR-0025](0025-bc-proxy-metrics-and-netty-traffic.md) 决策 1 / 3 把 bc 代理的「后端子服可达性 + 延迟」用 **`ServerInfo.ping`（MC server-list-ping）** 实现：bc 壳逐后端发 ping、在 async 上报线程内用 `CountDownLatch` 有界等待汇总 up/total/平均延迟。该 ADR 后果一节即标注「可达性为近似值，受网络抖动与超时影响」。

真机验证（BungeeCord 1.21 + Paper 1.20.1 后端 + Beacon agent）暴露其更深的缺陷：**一个完全在线、玩家可正常连入的 Paper 后端，TCP 端口可连，却对 MC status ping 不作应答**——外部裸 status-ping 实测 TCP 三次握手成功、随后 status 请求 recv 超时。原因是后端开启了代理转发（bungeecord / 现代 proxy forwarding）后，对未携带转发握手的 status ping 不予应答。于是 `ServerInfo.ping` 对这台**可达**后端**恒判不可达**，看板「后端可达性」持续 0/N（如 lobby-1 恒 0/3），且**任何加大超时都无效**（后端根本不应答，不是慢）。

「后端可达」的运维语义是「代理此刻能否把玩家路由到该后端」，其真实信号是**后端端口能否建立 TCP 连接**，而非「后端是否应答 server-list-ping」。status-ping 把「应答 status」误当「可达」，对代理后端是系统性误判。

## 决策

bc 后端可达性探测**由 `ServerInfo.ping`（MC status-ping）改为 TCP 连接探测**，修订 ADR-0025 决策 1 第四项与决策 3 的探测机制（ADR-0025 其余决策——连接数 / 线程 / 运行时长 / 吞吐缺位 / metric 模型扩列 / 角色分流聚合与展示——**全部不变、不受本 ADR 影响**）：

1. **探测机制 = 带超时的阻塞 TCP 连接**：对代理目录里每个后端的 socket 地址发起一次 `Socket().connect(addr, timeout)`——连上即**可达**，RTT 取连接耗时毫秒；连接被拒（`ConnectException`）/ 超时（`SocketTimeoutException`）/ 无路由即**不可达**。可达性与延迟语义、聚合口径（up/total/平均延迟、无可达后端时延迟 -1）与 metric 模型、上报契约、落库列、前端展示**全部沿用 ADR-0025 不变**——仅换「怎么探」，不换「探什么 / 怎么存 / 怎么展示」。

2. **探测落 core 纯逻辑 + 平台壳仅取地址**：TCP 探测实现为平台无关纯 JDK 工具 `TcpBackendProbe`（agent-core，与聚合纯函数 `BackendReachability` 同包、可穷举单测），bungee 壳 `BungeeProxyMetricsCollector` 仅负责从 `ProxyServer.getServers()` 取各后端 `socketAddress` 后委托。`java.net.Socket` 是 JDK 标准连通性原语、非 HTTP 客户端，不属 [ADR-0005](0005-agent-transport-codec-abstraction.md) 所约束的「HTTP/JSON 库只进适配器」——故落 core 不破不变量。

3. **不碰 MC 主线程、有界并发**：连接在独立**守护线程池**（`beacon-backend-probe`，空闲自动回收）逐后端并发执行，调用方（agent 既有 async 上报线程）用 `CountDownLatch` 有界等待汇总；总等待略大于单连接超时（少数慢后端不叠加）。绝不在 MC / 调度主线程做阻塞 IO（守 [architecture-invariants](../../.claude/rules/architecture-invariants.md) #5），与 ADR-0025 的「不碰主线程」约束一致。

## 理由

- **TCP 连接才是「可达」的真实信号**：代理路由走 TCP；端口可连即可路由。status-ping 依赖后端「应答 status」这一与可达性正交、且代理后端常关闭的能力，是错误的可达性判据。
- **稳健、跨后端配置无关**：TCP 连接不依赖 `enable-status`、转发模式、协议版本或 server-list-ping 实现，对 bukkit / 代理后端 / 任意 MC 服一致工作；消除真机暴露的系统性误报。
- **更简单**：不再依赖 BungeeCord `ServerInfo.ping` 的异步回调契约（且消除 agent 编译期 bungee-api 版本与运行期版本差异带来的脆弱性），探测逻辑下沉为纯 JDK、可单测。
- **延迟仍是近似值**：TCP 连接 RTT 同样仅供趋势 / 概览参考，不作精确 SLA——与 ADR-0025 对延迟近似值的定位一致。

## 后果

- bc 后端可达性此后以「TCP 端口可连」为准：在线后端（含不应答 status ping 者）正确判可达，离线 / 端口未监听后端正确判不可达。**契约 / 落库列 / 前端展示零改动**（仅 agent 探测实现变）。
- agent 新增一个**后端探测守护线程池**（`beacon-backend-probe`，cached、daemon、空闲回收）：相对 ADR-0025 复用 Netty 回调，多一处受控线程资源；规模与后端数同阶、空闲即收，开销可忽略。
- **延迟语义微变**：由「MC status-ping RTT」变为「TCP 连接 RTT」；同 LAN 后端连接 RTT 常为亚毫秒（整数毫秒可能为 0），属正常。
- **「可达」≠「MC 服已就绪」**：端口监听但世界仍在加载的后端会被判可达。这与代理路由视角一致（代理按 TCP 可连路由），且 ADR-0025 起可达性即非精确 SLA，可接受。
- ADR-0025 状态标注「后端可达性探测机制于本 ADR 修订」，其正文不改（已接受 ADR 不可变，仅状态注记）。

## 备选方案

- **保留 `ServerInfo.ping`，仅加大超时 / 文档标注局限**：真机已证后端**根本不应答** status ping（非慢），加大超时无效；接受局限等于让看板对在线代理后端长期误报不可达，违背 FR-34「呈现真实可达性」意图。落选。
- **混合：TCP 探可达 + status-ping 探延迟**：延迟更贴近「MC 层 RTT」，但实现更复杂，且对不应答 status 的后端延迟恒不可用（信息量反不如 TCP RTT），为边际收益增复杂度。落选（守简单优先）。
- **修后端 Paper 配置使其应答 status ping**：属改用户后端配置、且其它部署同样会遇到（代理后端不应答 status 是普遍现象），治标不治本。落选。
- **TCP 探测放 bungee 壳内联（不下沉 core）**：platform-coupled、且 agent-bungee 无测试工程，新增 IO 逻辑将无单测覆盖。落选——下沉 core 纯 JDK 工具可单测、可复用、与聚合纯函数同构。
