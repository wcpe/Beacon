# agent 注册双端点语义消歧（FR-233）

> 状态：开发中　·　关联 PRD：FR-233（增强 FR-222 / FR-139/140/141）　·　架构决策：[ADR-0084](../adr/0084-agent-registration-endpoint-disambiguation.md)

## 0. 与另一者的分工（先说清本项不是什么）

Beacon 有**两个 agent 注册端点，同名却不同职责**。本规格只负责**让这件事在调用点一眼可辨**，不改变任何运行时语义：

| 端点 | 凭据 | 职责 | 归属规格 |
|---|---|---|---|
| `POST /beacon/v1/agent/data-plane/attach`（原名 `/beacon/v1/agent/register`） | 共享 token（`agent-token`，`agentTokenMiddleware`） | **数据面挂载**：写内存 registry（数据面可见性真源）+ `instance.register` 审计；命中共享 token 时**同时**是 FR-222 机器注册的判定落点 | **本规格** |
| `POST /beacon/v2/agent/register` | namespace token（按库中哈希校验） | **身份状态机**：pending → 人工审批 → active（FR-139/140/141） | [v2-agent-identity](v2-agent-identity.md) |

**两者是串联而非二选一**：插件走 v2 身份注册，其 active 分支再调用 v1 数据面挂载（见 [BeaconApiRegister.kt](../../apps/agent/agent-core/src/main/kotlin/top/wcpe/beacon/agent/core/client/BeaconApiRegister.kt)）。

> **机器注册直落不在 v2 端点**——它落在本规格的 v1 数据面挂载端点，且需开启 `mcp.allow-machine-register`（FR-222，见 [internal-trust-channel](internal-trust-channel.md)）。

## 1. 背景

2026-09-24 用 JianManager 真机搭建生产环境（12 实例 + Beacon 集成）时，双端点同名结构造成两类误判：

1. **外部搭建方**观察到「v1 与 v2 都能注册」，判定「功能重叠、应退役一个」，据此排查却发现插件**只走 v2**，方向完全错。
2. **插件**用 `bootstrap-token` 打 v2 持续 401——因为 `hasV2Identity()` 在正常启动时恒真（`identityId` 与 `bootId` 均首启生成），插件恒定走 v2，而环境里配的 `bootstrap-token` 是 **v1 的共享 token**，对 v2 认证必然失败。

排查中确认的真实关系是**串联**：`registerLegacy`（v1）的唯一有效入口是 v2 的 active 分支；「无 v2 身份」回退分支在有身份文件时不可达。

**根因是「同名」本身**：插件代码里 `"$base/beacon/v{1,2}/agent/register"` 两处读起来一样，仅靠文档无法在调用点消除歧义。

## 2. 目标与非目标

**目标**

- 两端点职责在 **spec / 代码注释 / 配置注释**三处均可辨。
- 旧路径在**一个版本周期**内继续可用，且调用方能从响应头感知其已废弃。
- 插件、外部平台推送、MCP 三处接入文档均指向正确通道。

**非目标（明确不做）**

- **不退役 v2**：它是 FR-139/140/141 的身份入口，退役即砍掉人工审批与身份冲突处置。
- **不退役 v1 的注册职责**：该端点同时是 FR-222 机器注册的判定落点与全部数据面端点所在路由组。ADR-0084 备选方案 B 已否决此路。
- **不合并两端点**：v2 需 namespace token（FR-142 强隔离），v1 需共享 token（FR-222 内网信任），合并必令一方放弃凭据模型，属安全边界变更。
- **不改运行时语义**：鉴权、机器注册直落、审计、registry 写入、状态码语义一律不变。

## 3. 设计

### 3.1 端点更名与兼容别名

规范路径改为 **`POST /beacon/v1/agent/data-plane/attach`**，语义显式化为「挂载数据面」。旧路径保留为兼容别名。

**关键约束：新端点必须留在 `/beacon/v1/agent` 路由组内。** `agentTokenMiddleware` 挂在该组上，而它是 FR-222「受信内部调用方」判定的唯一来源（且该判定绝不读请求体）。把新端点移出该组或另起路由组，会使机器注册能力静默失效。

```
/beacon/v1/agent（组级挂 agentTokenMiddleware）
  ├─ POST /data-plane/attach   ← 新规范路径，同 handler
  ├─ POST /register            ← 兼容别名，额外回带 Deprecation / Link 头
  └─ …（heartbeat / stream / files / commands 等数据面端点不变）
```

仅**旧路径**回带两个响应头（新路径不得有，否则「新路径也废弃」的错读会立刻发生）：

| 响应头 | 值 |
|---|---|
| `Deprecation` | `true` |
| `Link` | `</beacon/v1/agent/data-plane/attach>; rel="successor-version"` |

实现上把加头逻辑放在**路由包装器**而非 handler 内部——写在 handler 里会让新路径也带上。

### 3.2 插件侧调用与回退

插件改用新路径。为避免「插件先升级、控制面后升级」导致接入中断（MC 插件与控制面分批部署很常见），保留**窄回退**：

```
请求新路径
  ├─ 404（对端是旧控制面，无此路由）→ 改用旧路径重试一次
  └─ 其他状态码 → 按既有映射处理，不回退
```

**只在 404 回退**：其余状态码（200 / 400 / 401 / 403 / 409）都由真实业务语义产生，回退会掩盖真实错误（例如 409 重复 serverId 被误当作版本不匹配而重试）。

### 3.3 配置注释澄清（消歧的一次性根治）

`bootstrap-token` 一名的字面暗示（「引导用」）正是误判来源之一。配置注释需显式说明：

- `agent-token`（v1 共享 token）：数据面挂载凭据；开启 `mcp.allow-machine-register` 后**升级为安全边界**，必须换强随机值。
- `bootstrap-token`（agent 本地键）：供 bootstrap 阶段**观察 v2 身份状态**（`bootstrapRegister`），**不是**机器注册凭据。

### 3.4 文档交叉引用

两端点规格首段互写「与另一者的分工」（本规格 §0，v2 规格对应段落），使任一入口的读者都能立刻看到另一端点的存在与区别。

## 4. 任务拆分

1. Go 侧：路由器新增新路径 + 旧路径包装器（`Deprecation` / `Link` 头）+ 相关注释更新。
2. Go 侧：集成测试覆盖「新路径可用且无 Deprecation 头」「旧路径可用且有 Deprecation 头」「两路径语义一致」。
3. 插件侧：`registerLegacy` 改用新路径 + 404 回退旧路径 + 单测（新路径 404 回退成功；非 404 不回退）。
4. 文档：`API.md` v1 端点段更名并注明别名；`OPERATIONS.md` 机器注册落点说明更新；`config.example.yml` 与 `config.go` 注释澄清两个 token；本 spec 与 v2 spec 交叉引用。

## 5. 验收标准

| 验收项 | 通过条件 |
|---|---|
| 新路径可用 | `POST /beacon/v1/agent/data-plane/attach` 注册成功；响应**不含** `Deprecation` 头 |
| 旧路径兼容 | `POST /beacon/v1/agent/register` 注册成功；响应**含** `Deprecation: true` 与 `successor-version` 的 `Link` |
| 语义一致 | 两路径对同一实例的注册结果一致（同状态码、同结构） |
| 机器注册不回归 | 开启 `allow-machine-register` + 强随机 token 时，经该端点仍直落 active 并写 `identity.machine_registered` 审计 |
| 鉴权不变 | 缺 / 错 token 仍 401；agent 自持身份仍走 v2 人工审批 |
| 插件回退 | 新路径 404 时插件自动用旧路径并成功；非 404 不回退 |
| 运行时不变量 | 除新增路由与响应头外，无任何行为变化（既有回归全绿） |
| 三处可辨 | spec / 代码注释 / 配置注释均能分辨两端点职责与凭据 |

## 6. 风险与迁移

- **更名需插件与外部平台同步**：兼容别名窗口为一个版本周期；届时删除旧路径须再走一次变更（`API.md` 已将其标为在役硬契约，删除属破坏性变更）。
- **JianManager 等外部推送方**：其推送路径在**另一仓库**（`beacon_push.go`），需该侧同步；本仓库只保证旧路径在窗口期内不失效。
- **`Deprecation` 响应头为新增**：旧客户端忽略即可，非破坏性。

## 7. 与既有决策的关系

不取代任何 ADR。ADR-0084 已就本项做出决策并否决三个备选方案（退役 v2 / 退役 v1 注册职责 / 合并端点），本规格是其实现落点。
