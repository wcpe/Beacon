# ADR-0077：Agent 角色化 endpoint 地址权威与 BC 多 listener

**状态**：已接受（2026-07-28）

## 背景

Agent 现有的单地址字段不能同时正确表达两种不同的平台事实：Bukkit/Paper
只有一个游戏监听入口，而 BungeeCord（BC）可以配置多个 listener。将 Agent
本地填写的 host 直接当作外部可达地址，会在 NAT、容器和反向代理部署中把
本地绑定事实误当成网络可达事实；继续只保留一个地址，也会丢失 BC 的其余
listener。

同时，既有身份、发现与业务插件仍消费单地址 `address` / `lastAddr`，不能以
破坏性改动把公共契约直接替换成地址列表。需要把多 listener 的事实、外部
地址探测和管理员修正集中到控制面，同时保持旧字段可用。

## 决策

### 1. endpoint 记录是地址事实的控制面权威

控制面为每个已登记 Agent 身份持久保存 endpoint 记录；endpoint 集合是多地址
事实的唯一权威，身份上的单地址字段只是兼容投影，不得反向覆盖 endpoint。

- Bukkit/Paper 只能登记一个 active endpoint，固定 key 为 `primary`。
- BC 必须登记全部当前 listener；空 listener 集合不得作为 active proxy 注册。
- 一次有效注册以本轮完整 endpoint 集合对账：出现的项更新并激活，未出现的
  旧项失活但保留历史与已有覆盖，重新出现时复用该项。
- 每个 endpoint 至少保存：稳定 key、上报顺序 ordinal、本地绑定事实、探测
  地址、可选覆盖地址、active 状态和最近上报时间。生效地址在读取时派生，
  不另存第三份可漂移真源。

### 2. key 稳定标识 listener，ordinal 只定义顺序

BC 的 endpoint key 由规范化后的 `bindHost:port` 得出；host 去首尾空白并按
小写保存，IPv6 先去输入方括号后规范化，输出采用等价于 `JoinHostPort` 的
带方括号形式。Bukkit 的 key 保持 `primary`。

- 同一次上报中出现重复规范化 key 是配置错误，控制面拒绝该请求，Agent 不
  得静默去重。
- ordinal 保留 BC 配置顺序，只用于展示和选择兼容单地址；listener 列表重排
  不得改变 key、覆盖地址或 endpoint 身份。
- bind host 是本机监听和稳定匹配事实，可为通配或 loopback 地址；它不代表
  外部可达 host。

### 3. 探测地址只信任 TCP 对端与上报端口

Agent 只上报由平台读取的监听端口，以及 BC listener 的本地 bind host、port 和
ordinal。控制面从本次 HTTP 连接原始 TCP `RemoteAddr` 提取 host，并与上报端口
组合为 detected address。

上报 bind host、请求体中的地址、`X-Forwarded-For`、`X-Real-IP` 和现有审计
用途的 client IP 都不得作为探测地址来源。这样 Agent 不能借注册报文伪造
外部可达地址，审计链路的代理头兼容也不会污染发现链路。

### 4. 逐 endpoint 覆盖优先，反代本期显式修正

每个 active endpoint 可以由具备完整管理权限的管理员填写一个完整
`host:port` 覆盖地址，并记录操作原因和审计。生效优先级固定为：

`override address > detected address`

清除覆盖后立即恢复探测地址。覆盖不可按身份或 BC 整体共享，以免一个 NAT
映射误写到其他 listener。非 active endpoint 只能清除旧覆盖，不能新增覆盖。

本期不引入可信代理 CIDR、代理头逐跳解析或新的网络组件。控制面位于反向
代理之后时，自动探测得到代理地址是预期的保守结果；管理员必须为受影响的
listener 显式覆盖。若未来要信任代理头，必须先以独立 ADR 定义可信代理范围、
逐跳解析和默认拒绝语义。

### 5. 单地址契约继续作为兼容投影

`address` / `lastAddr` 以及既有发现、注册表和公共 API 的单地址形状继续保留。
它们投影为首个 active endpoint 的生效地址：Bukkit 为 `primary`；BC 先按最小
ordinal，再按 endpoint key 作稳定排序。没有 active endpoint 或身份不在既有
可用状态时，不得以旧投影绕过原有身份、健康或调度门禁。

兼容字段容量必须能够容纳规范化 IPv6 和合法长域名地址。旧 Agent 的单地址
请求在兼容窗口内继续接受，但控制面只从中解析合法端口，host 不提升为探测
权威；其余 endpoint 事实由新协议后续上报补齐。

### 6. 平台壳采集，core 保持平台无关

`agent-bukkit` 从 Bukkit/Paper 平台 API 读取唯一监听端口，`agent-bungee` 从
BC 平台 API 读取全部 listener；采集在既有异步启动或重连流程中完成，不能
阻塞 MC 主线程。平台壳把规范化后的简单数据传给 core。

agent core 只保存和传输平台无关的 endpoint 值对象，不持有 Bukkit、BungeeCord
或 TabooLib 的平台类型，也不读取平台配置。这延续 [ADR-0005](0005-agent-transport-codec-abstraction.md)
的 core 与具体平台/库隔离边界；控制面同样只处理协议数据和 TCP 连接事实。

## 理由

1. BC 的多个 listener 是平台代理事实，必须完整保留，不能压缩成一个本地
   配置字段。
2. TCP 对端与端口组成的探测地址可由控制面独立验证；本地 bind host 不能安全
   地表示公网路由。
3. 逐项覆盖能精确处理 NAT 和反代，而不把本期扩大为一套可信代理网络体系。
4. endpoint 真源与旧单地址投影分离，既可新增多 listener 能力，也不破坏已
   发布的发现与 Agent API。
5. 壳层采集让平台差异停留在正确边界，core 可通过纯值对象测试而不依赖游戏
   服务运行时。

## 后果

- 控制面需要维护 endpoint 的持久化、对账、地址派生、逐项覆盖和审计；身份
  详情可以展示完整 listener 集合，而列表页只展示摘要，避免无界展开。
- 注册与响应协议以 additive 字段扩展：新 BC 上报列表，新 Bukkit 上报单端口；
  旧单地址字段继续可读、可写兼容投影。
- 覆盖修改后，发现与运行注册表必须原子地看到新的兼容地址，并触发既有拓扑
  变更通知。
- listener 改变 bind host 或端口会产生新 key，旧覆盖不自动迁移，避免把公网
  映射误配到新入口。
- 反向代理部署若未设置覆盖，发现地址可能不可直连；这是明确可见且可修正的
  限制，不是静默信任可伪造请求头。

## 备选方案

### 方案 A：继续由 Agent 上报单个 `identity.address`

丢失 BC 的多 listener，且任意 Agent 可伪造外部 host；无法满足地址列表契约。
否决。

### 方案 B：信任上报 bind host 作为外部地址

bind host 常为 `0.0.0.0`、`::`、容器内地址或 loopback，并非外部路由事实。
否决。

### 方案 C：立即支持受信代理 CIDR 与转发头

需要定义拓扑、CIDR 生命周期、逐跳解析和失配默认拒绝，超出本期且错误配置会
重新引入地址伪造风险。否决；本期使用逐项显式覆盖。

### 方案 D：把地址列表直接替换公共单地址 API

会破坏旧 Agent、业务插件与发现消费者。否决；保留单地址投影并以 additive
端点列表扩展。

### 方案 E：在 agent core 直接读取 Bukkit 或 BC listener

会把平台依赖带入 core，破坏现有双端适配与可测试边界。否决。
