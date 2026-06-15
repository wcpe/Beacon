# 架构不变量（防架构漂移）

> 以下是 Beacon 锁定的架构约束（依据 `docs/ARCHITECTURE.md` 与 `docs/adr/`）。**违反任一条即为架构漂移。**
> 确需改变某条 → 先写新 ADR 取代旧决策、经确认后再改；**禁止在代码里静默违背**。

## 1. 控制面 / 数据面边界
- 控制面（Go）只存"事实"（配置 / 拓扑 / 注册 / 健康）+ 提供查询下发，**禁止写任何游戏逻辑**。
- 跨服玩家行为（看人 / 传送 / 经济）是业务插件的事，**不进 Beacon MVP**。
- agent 对业务插件**只读**暴露 API，不开放改配置 / 改 zone 的旁路。

## 2. 简单优先（禁重型件）
- MVP **禁止引入** Redis、消息队列、DI 框架、分布式一致性组件、服务网格。
- 注册/健康用进程内存（map+RWMutex），配置/版本/分配/审计落 MySQL，不另起中间件。

## 3. 真源切分
- 注册/健康的真源 = Go 进程内存；配置/版本/zone 分配/审计的真源 = MySQL。两者不得互为权威或互相阻塞。

## 4. 数据库可移植（GORM）
- **禁用 MySQL 专有特性**：不用 `ENUM/SET/JSON` 列、不写 `gorm:"type:<方言专有>"`、自增用 GORM 抽象。枚举落 `VARCHAR`+应用层校验，json 落 `TEXT`。必须能切 Postgres。

## 5. agent 约束
- **不在 MC 主线程做阻塞 IO**（用 TabooLib async）。
- **fail-static**：控制面不可用时按本地快照继续，绝不阻断玩家进服。
- HTTP 客户端与 JSON 库**只能出现在适配器里**，core 依赖 `HttpTransport`/`JsonCodec` 接口（[ADR-0005](../../docs/adr/0005-agent-transport-codec-abstraction.md)）。
- agent 自管身份，**不依赖 CoreLib**。

## 6. zone 权威
- serverId 由 agent 上报；**zone 归属由控制面 DB 权威指派**（方案 b，[ADR-0004](../../docs/adr/0004-zone-authority-control-plane.md)）。agent 不声明 zone。

## 7. 技术栈锁定
- 后端 Go + chi + GORM；前端 React(Vite+TS) 经 `go:embed` 内嵌、单二进制同端口；agent Kotlin/TabooLib。
- 换栈 / 换框架 = 架构决策 → 走新 ADR，不擅自更换。

## 红线（出现即停止并先确认）
引入 Redis/MQ/DI · 把游戏逻辑写进控制面 · 用 MySQL 专有 SQL 破坏可移植 · agent 阻塞主线程或在 core 硬绑 OkHttp/kotlinx · 静默违背任一已接受 ADR。
