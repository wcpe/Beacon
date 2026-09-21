# 拓扑建树 MCP 工具（FR-221）

## 1. 背景

Beacon 已具备两套管理面：
- **HTTP `/admin/v2`**：完整的区服权威读写（`POST /bc-clusters`、`POST /regions`、`POST /zones` 等九个端点）
- **MCP `/admin/v2/mcp`**（FR-219/220）：面向自动化客户端的显式领域工具

现状缺口：MCP 侧只有**分配类**工具（`topology.servers.assign` 等），**没有建树类**工具。运维若要在 MCP 流程里新建一个大区或小区，只能回退到 HTTP + 人工登录取令牌，无法在自动化链路内闭环。

本 FR 补齐这九个建树工具。

## 2. 目标与非目标

**目标**
- MCP 可完成 BC 集群 / 大区 / 小区的新建、改名、删除
- 语义与既有 HTTP 端点**逐一对齐**，不引入新概念
- 建树是低风险结构操作，按 FR-220「低风险按能力直执」直接执行并写审计

**非目标**
- 不改动既有 HTTP 端点行为
- 不涉及**分配/换区**（已由 FR-220 的 `topology.servers.*` 覆盖，走审批）
- 不引入层级移动（大区跨集群迁移等）——保持与 HTTP 端点同等能力边界

## 3. 与审批模型的关系（关键设计）

Beacon 的动作分两档（FR-220）：

| 档位 | 判定 | MCP 行为 |
|---|---|---|
| 低风险 | 结构增删改、可逆、不触碰玩家流量 | **直接执行** + 审计 |
| 高风险 | 影响玩家入口、身份绑定、鉴权 | 只创建审批票据 |

建树属于**低风险档**：新建小区不会让任何玩家改道；删除非空节点被既有约束直接拒绝（不会误删带服的节点）。因此本组工具**不走审批票据**，与 `servers.assign`（走审批）形成清晰对照。

> 这一区分需在 spec 与工具描述里写明，避免后续维护者误以为"所有 topology.* 都要审批"。

## 4. 工具清单

九个 MCP 工具，与 HTTP 端点一一映射：

| MCP 工具 | 对应 HTTP | 说明 |
|---|---|---|
| `beacon.topology.bc-clusters.create` | `POST /admin/v2/bc-clusters` | 新建 BC 集群 |
| `beacon.topology.bc-clusters.update` | `PATCH /admin/v2/bc-clusters/{id}` | 改名 / 改描述 |
| `beacon.topology.bc-clusters.delete` | `DELETE /admin/v2/bc-clusters/{id}` | 删除（非空拒绝） |
| `beacon.topology.regions.create` | `POST /admin/v2/regions` | 新建大区（须指定所属集群） |
| `beacon.topology.regions.update` | `PATCH /admin/v2/regions/{id}` | 改名 / 改描述 |
| `beacon.topology.regions.delete` | `DELETE /admin/v2/regions/{id}` | 删除（非空拒绝） |
| `beacon.topology.zones.create` | `POST /admin/v2/zones` | 新建小区（须指定所属大区） |
| `beacon.topology.zones.update` | `PATCH /admin/v2/zones/{id}` | 改名 / 改描述 |
| `beacon.topology.zones.delete` | `DELETE /admin/v2/zones/{id}` | 删除（非空拒绝） |

### 4.1 入参约定

**create**（以 zone 为例）：
```
{
  namespaceId: number,      // 必填（或 namespace code，按既有端点约定）
  parentId: number,         // 必填：所属大区 id（bc-cluster 无此项；region 填集群 id）
  code: string,             // 必填：作用域内唯一
  displayName?: string,     // 可选
  description?: string      // 可选
}
```

**update**：
```
{ id: number, displayName?: string, description?: string }
```
> code 不可改（既有语义：code 是稳定寻址标识）。

**delete**：
```
{ id: number, confirmCode: string }   // 精确确认，与既有 destructive 一致
```

### 4.2 可见性

- **observer profile**：不可发现本组全部九个工具（写操作）
- **automation profile**：可发现并调用

与既有 `MCPToolNames(profile)` 的分配逻辑一致（见 `mcp_tools.go`）。

> **覆盖门禁同步**：`MCPToolNames()` 是覆盖门禁（`mcp_tools_test.go`）的契约来源，新增工具必须同时登记，否则门禁失败。本 FR 的九个工具已登记在 automation 分支。

### 4.3 与审批决定工具的清单一致性（FR-223 联动）

改造 `MCPToolNames()` 时一并修正了一处**清单与运行时不符**：

| 层面 | 现状 |
|---|---|
| 运行时注册 | `NewMCPServer` 按 `principal.HasCapability(CapabilityApprovalDecide)` 判定，该能力由 `mcp.allow-approval-decide` 开关控制 |
| 静态清单（改造前） | **无条件**列出 `beacon.approvals.approve/reject`，与开关无关 |
| 后果 | 门禁测试 `TestMCPToolCoverageObserverCannotWithdrawAndAutomationCanOnlyWithdrawOwn` 断言「机器主体不得发现审批决定工具」而失败 |

**修正**：清单改为读 `auth.MCPApprovalDecideEnabled()` 动态纳入——开关关闭时不列（保持 FR-220 分权原则），开启时才列。测试同步改为验证动态行为（新增 `TestMCPToolCoverageApprovalDecideIsOptIn`）。

> 这项修正属 FR-223 的收口，与建树工具无耦合，仅因同处 `MCPToolNames()` 而一并处理。

## 5. 实现要点

1. **复用既有 service 层**：`V2ControlPlaneHandler` 的 `CreateBCCluster` / `CreateRegion` / `CreateZone` 等已有完整实现（含校验、审计、唤醒），MCP 工具只做「入参解析 → 调 service → 结果投影」。
2. **审计**：由 service 层既有事务内自记（`bc_cluster.create` / `region.create` / `zone.create` 等已在审计动作表），MCP 侧不重复记。
3. **错误投影**：沿用 `mcpRejectedResult()`，业务码与 HTTP 侧一致（如 `409 not_empty`、`409 code_conflict`）。
4. **删除确认**：`confirmCode` 与目标当前 code 精确比对，不符拒绝——复用既有 destructive 确认范式（参考 `instance_group_delete` 的 `confirmGroupName`）。

## 6. 验收标准

| # | 验收项 | 方式 |
|---|---|---|
| 1 | MCP 可创建 BC 集群 / 大区 / 小区，层级归属正确 | 真机 MCP 调用 |
| 2 | 可改名与改描述；code 不可改 | 真机 |
| 3 | 删除非空节点被拒（409），错误码与 HTTP 一致 | 真机 |
| 4 | 删除空节点成功，`confirmCode` 不符时拒绝 | 真机 |
| 5 | 建树操作**不产生审批票据**（与 assign 的差异可观测） | 检查响应形状 |
| 6 | observer profile 工具列表中**不含**本组九个工具 | 真机（不同 profile 的 token） |
| 7 | 各操作在审计页可查（动作名与 HTTP 侧一致） | 管理台审计 |
| 8 | 建树后 `zone-tree.get` 能正确反映新结构 | 真机 |

## 7. 影响面

| 文件 | 改动 |
|---|---|
| `apps/server/internal/server/mcp_tools.go` | 新增九个 `mcp.AddTool` 注册 |
| `apps/server/internal/server/mcp_tools.go` | `MCPToolNames(automation)` 追加九个工具名 |
| `apps/server/internal/service/` | 复用既有方法，若无导出则补导出 |
| `docs/PRD.md` | FR-221 状态推进 |
| `docs/API.md` | MCP 工具清单追加 |

**无破坏性改动**：纯新增，不影响既有 HTTP 与 MCP 行为。

## 8. 未决 / 待确认

- 建树是否需要 namespace 级隔离校验（跨 namespace 引用父级应拒绝）——按既有 HTTP 端点行为对齐即可，无需新决策。
- 是否提供批量建树（如一次性建 3 大区 × 4 小区）？**本 FR 不做**（YAGNI）；调用方循环即可，且批量会引入部分成功语义。
