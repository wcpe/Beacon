# 内部信任通道与机器注册（FR-222）

## 1. 背景

Beacon 的 agent 接入是**分权设计**（FR-203/220）：agent 主动注册 → 落 `pending` → **人工审批** → 转 `active` 并绑定 serverId。这个设计对公网多操作者环境是必要的。

但在**单操作者内网部署**下会产生实质摩擦：JianManager 之类的外部管理平台批量创建实例后，无法让这些实例的 agent 自动完成接入，只能人工到管理台逐个点批准。实测中 60 台实例需要 60 次人工审批。

本 FR 提供一条**可显式开启的内部信任通道**，让受信调用方能够机器化完成注册，同时不破坏默认的安全语义。

## 2. 目标与非目标

**目标**
- 提供开关控制的机器注册通道，开启后受信调用方注册的 agent 直接 `active`
- **默认关闭**，关闭时行为与现状**完全一致**（分权设计不破）
- 所有机器注册写强审计，可追溯调用来源

**非目标**
- 不做 agent 身份的机器化**解绑 / 禁用 / 启用**（仍走审批）
- 不改变 agent 自身的注册协议（agent 侧零改动）
- 不做 namespace 级差异化开关（全局单开关，避免配置爆炸）

## 3. 设计

### 3.1 复用既有共享 token（决策 1A）

Beacon 已有 `agentTokenMiddleware`（`middleware.go:63`）的第一分支：

```go
rawToken := r.Header.Get("X-Beacon-Token")
if token != "" && rawToken == token {
    next.ServeHTTP(w, r)
    return
}
```

该 token 来自配置 `agent-token`（默认 `beacon-bootstrap-token`），注释原写「仅防误连，非安全边界」。

**本 FR 将其升级为安全边界**：一旦开启 `allow-machine-register`，持有该 token 即等价于「受信内部调用方」，故：

> **开启该开关时，`agent-token` 必须显式改为强随机值。** 启动校验拒绝默认值 + `allow-machine-register=true` 的组合。

### 3.2 新增开关

```yaml
mcp:
  # 允许受信内部调用方机器化注册 agent（跳过人工审批）。
  # 默认 false。仅内网单操作者部署可开启；公网部署必须保持 false。
  # 开启时 agent-token 不得为默认值。
  allow-machine-register: false
```

启动校验（`config/load.go`）：
```
if c.MCP.AllowMachineRegister {
    if c.AgentToken == "" || c.AgentToken == defaultAgentToken {
        return fmt.Errorf("配置校验失败: 开启 allow-machine-register 时 agent-token 必须改为强随机值")
    }
}
```

### 3.3 注册路径的分支

`POST /beacon/v1/agent/register` 现有流程：
```
注册 → agent_identity 落 pending → 等审批
```

新增分支：
```
注册
  ├─ 请求经 agentTokenMiddleware 的共享 token 分支（即受信内部调用方）
  │   └─ allow-machine-register=true？
  │        ├─ 是 → 直接落 active + 绑定 serverId + 写强审计（machine-register）
  │        └─ 否 → 落 pending（现状）
  └─ 请求经 agent 自持身份（X-Beacon-Identity/Boot）→ 一律落 pending（现状不变）
```

**关键**：分支依据是**中间件已判定的调用方类型**，不是请求体字段。调用方无法通过伪造请求体绕过。

### 3.4 审计

新增审计动作 `identity.machine_registered`：

| 字段 | 内容 |
|---|---|
| operator | `system:machine-register` |
| targetType | `agent-identity` |
| targetRef | identityId |
| detail | serverId、lastAddr、调用来源 IP |

无论开关状态如何，**机器注册意图都记审计**（关闭时记为「已提交待审批」）。

## 4. 安全性论证

| 风险 | 缓解 |
|---|---|
| 默认开启导致误配 | 默认 `false`；开启时强制改 agent-token |
| token 泄漏 → 任意注册 | 开启前必须换成强随机值；token 仅在受信内网传输；注册写强审计可查来源 |
| 伪造调用方类型 | 分支依据中间件的 token 比对结果，非请求体 |
| 绕过审批做危险操作 | 本通道**仅覆盖注册**（创建未分配 server）；分配/换区/默认入口仍走各自审批 |

### 4.1 与 FR-223 的组合

`allow-machine-register` + `allow-approval-decide` 同时开启 = 内网全自动闭环。两者**互相独立**：
- 只开 machine-register：注册自动化，分配仍人工
- 只开 approval-decide：注册仍人工，但分配可自动化
- 全开：端到端闭环（仅限内网单操作者）

## 5. 实现要点

| 位置 | 改动 |
|---|---|
| `internal/config/config.go` | `MCPConfig` 加 `AllowMachineRegister` |
| `internal/config/load.go` | 启动校验（开关 + 默认 token 组合拒绝） |
| `internal/server/middleware.go` | 将「共享 token 命中」写入 context（供 handler 判定调用方类型） |
| `internal/service/agent_identity*.go` | 注册流程按调用方类型分支；active 直落 + 审计 |
| `internal/server/audit_middleware.go` | 覆盖集合登记新动作 |

## 6. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | 开关关闭（默认）时，携共享 token 注册仍落 `pending` | 真机 |
| 2 | 开关开启 + 强随机 token 时，注册**直接 `active`** 且绑定指定 serverId | 真机 |
| 3 | 开关开启但 `agent-token` 仍为默认值 → **启动失败**并给出明确错误 | 启动校验 |
| 4 | 缺 / 错 token 一律 401（与开关无关） | 真机 |
| 5 | 机器注册在审计页可查，含 serverId 与来源 IP | 管理台审计 |
| 6 | 关闭开关时，同一请求的审计记为「已提交待审批」 | 管理台审计 |
| 7 | agent 侧零改动：既有 agent 注册流程不受影响 | 真机（现有 agent 复测） |
| 8 | 开关不影响分配/换区的审批要求 | 真机 |

## 7. 影响面

**向后兼容**：默认关闭，未开启时行为与 v1.1.0 完全一致。配置 schema 纯新增一个可选字段。

| 文件 | 改动 |
|---|---|
| `internal/config/config.go` / `load.go` | 新字段 + 校验 |
| `internal/server/middleware.go` | context 传递调用方类型 |
| `internal/service/agent_identity*.go` | 注册分支 |
| `internal/server/audit_middleware.go` | 审计覆盖登记 |
| `docs/API.md` | 注册端点补充开关说明 |
| `docs/OPERATIONS.md` | 内部信任通道配置说明 + 安全警告 |
