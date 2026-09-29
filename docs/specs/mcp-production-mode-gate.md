# 功能规格：MCP 生产模式工具门禁

> 状态：开发中（真机验收通过，待发版）　·　关联 PRD：FR-237　·　依赖：FR-236（风险分级真源）　·　分支：feature/mcp-production-mode-gate

## 1. 背景与目标

线上环境不应允许外部自动化 Agent 触发「不可逆」或「影响控制面自身」的操作。现有 `observer` / `automation` 两 profile 是**跨环境固定**的，缺少部署级的环境维度：同一份 `automation` 凭据在测试环境与生产环境看到完全相同的 78 个工具。

本 FR 提供部署级开关，按 FR-236 的风险真源收敛 MCP 工具发现面，使生产部署可显式关闭最危险的一档工具。

**附带收益（上下文成本）**：automation 侧 78 个工具的完整定义（名称 + 描述 + 输入 schema）实测约 22–26 KB，折合 7,000–9,000 tokens 的会话级固定开销。隐藏 10 个 `critical` 工具可减少约 13%，且被隐藏的恰是生产环境最不需要让 Agent 看到的工具。

## 2. 需求（要什么）

- 新增部署配置 `mcp.production-mode`（bool，默认 `false`）。
- 开启时，`critical` 等级工具**完全不可发现**（不出现在 `tools/list`）；`low` / `high` 不受影响。
- 关闭时，`tools/list` 与现状**逐工具零差异**（纯增量能力）。
- observer profile 在开关两态下集合不变（observer 本就不含 `critical` 工具）。
- 被收敛工具对客户端表现为**「不存在」**，而非「存在但调用失败」——避免泄露生产模式下的能力清单。
- 与既有 `mcp.allow-approval-decide` **正交**，但生产模式对 `critical` 的过滤**优先**：即使 `allow-approval-decide` 开启，生产模式下 `beacon.approvals.approve` / `.reject` 仍不可发现。

**范围内**：部署开关、运行时开关快照、发现面过滤、`/admin/v2/mcp/config` 只读暴露。

**不做（范围外）**：工具风险分级真源（FR-236 独立交付）；热更新（沿用 mcp.* 启动项惯例）；MCP 协议 / OAuth 层；管理台新页面。

## 3. 设计（怎么做）

### 3.1 配置与装配

沿 `mcp.allow-approval-decide` 的既有链路（配置 → 组合谓词 → 运行时包级快照）：

| 跳 | 文件 | 改动 |
|---|---|---|
| 1 | `internal/config/config.go` | `MCPConfig` 结构体（顶层为 `MCP MCPConfig \`yaml:"mcp"\``）新增 `ProductionMode bool \`yaml:"production-mode"\``，默认零值 `false` |
| 2 | `config.example.yml` | `mcp` 段增加注释与示例行 |
| 3 | `cmd/beacon/main.go` | 新增 `func mcpProductionModeEnabled(cfg config.MCPConfig) bool { return cfg.Enabled && cfg.ProductionMode }`，对齐既有 `mcpApprovalDecideEnabled`（`main.go:114`）；装配处调用 `auth.SetMCPProductionMode(mcpProductionModeEnabled(cfg.MCP))`，对齐既有 `auth.SetMCPApprovalDecide(mcpApprovalDecideEnabled(cfg.MCP))`（`main.go:426`） |
| 4 | `internal/auth/principal.go` | 包级 `atomic.Bool` + `SetMCPProductionMode` / `MCPProductionModeEnabled`（沿 `mcpApprovalDecideEnabled` 既有模式） |

**无额外配置校验**：bool 开关不与公网暴露面耦合，不触发 fail-closed 校验（对比 `validateMCP` 中 host / https / CIDR 的强制校验）。

### 3.2 过滤点（单点扩展）

FR-236 已建立唯一的可发现性判定入口。本 FR **只在该函数内叠加一个条件**：

```go
func mcpToolDiscoverable(name string) bool {
	spec, ok := mcpToolSpecByName(name)
	if !ok {
		return false // 未登记即 fail-closed
	}
	if spec.RequireApprovalDecide && !auth.MCPApprovalDecideEnabled() {
		return false
	}
	if auth.MCPProductionModeEnabled() && spec.RiskLevel == MCPRiskCritical {
		return false // FR-237：生产模式隐藏不可逆与影响控制面自身的工具
	}
	return true
}
```

`MCPToolNames`（测试与文档侧）与 `mcpAddTool`（运行时注册侧）共用该函数，**两侧永不漂移**是本设计的核心保证。

### 3.3 对外暴露

`GET /admin/v2/mcp/config` 增加只读字段 `productionMode`（启动项，不提供写端点）：

| 层 | 文件 | 改动 |
|---|---|---|
| handler | `internal/handler/mcp_config_handler.go` | DTO 增 `productionMode`；`Get` 逐字段映射 |
| 契约真源 | `packages/contracts/src/mcp.ts` | 类型增字段 |
| mock | `packages/devmock/src/domains/mcp.ts` | handler 响应增字段 |
| 前端 | `apps/web` | **无需改动**——`config-card.tsx` 直接读契约字段，后端不下发则零变化 |

### 3.4 测试

新增测试（可与 FR-236 的 `mcp_tool_catalog_test.go` 同文件或独立）：

1. **关闭态回归**：`productionMode=false` 时，in-memory 枚举集合 == FR-236 基线逐工具相等。
2. **开启态差集**：在**同一 `allow-approval-decide` 状态**下分别取 production-mode 关闭的集合 A 与开启的集合 B，断言 `A − B` **精确等于**「catalog 中 `RiskLevel == critical` 且在该 approval-decide 状态下可见」的工具名集合。

   > **基线定义不可省**：`beacon.approvals.approve` / `.reject` 同时是 `critical` 且 `RequireApprovalDecide`。当 `allow-approval-decide=false`（默认）时它们本就不在集合 A 内，若直接套用「A − B == 全部 critical」会误报。测试须对 `allow-approval-decide` 的**两态各跑一次**差集断言。
3. **不可见语义**：被收敛工具出现在 `MCPToolNames` 之外；对其调用返回「未知工具」类错误而非权限错误。
4. **observer 不变**：开关两态下 observer 集合逐工具相等。
5. **与 approval-decide 的优先级**：`productionMode=true` + `allow-approval-decide=true` 时，`approve` / `reject` 仍不可发现。
6. **端点字段**：`GET /admin/v2/mcp/config` 在两态返回正确 `productionMode`，且不回显任何凭据。

测试需 `t.Cleanup` 复位包级开关（沿 `TestMCPToolCoverageApprovalDecideIsOptIn` 既有模式），避免测试间串扰。

## 4. 任务拆分

- [ ] 配置字段 + 示例配置 + 解析测试
- [ ] main 装配 + auth 包级开关
- [ ] `mcpToolDiscoverable` 叠加生产模式过滤
- [ ] `/admin/v2/mcp/config` 字段 + contracts + devmock
- [ ] 六项测试
- [ ] 文档同步：PRD 状态、ARCHITECTURE、CHANGELOG

## 5. 验收标准

- 关闭时：in-memory 枚举集合与 FR-236 基线**逐工具相等**。
- 开启时：在同一 `allow-approval-decide` 状态下，`A − B`（关闭态集合减开启态集合）**精确等于**该状态下可见的 `critical` 工具名集合（可证伪：多一个或少一个都失败）；须对 approval-decide **两态各验一次**。
- 被收敛工具表现为不可发现（非执行被拒）。
- observer profile 在任何开关状态下集合不变。
- `GET /admin/v2/mcp/config` 两态字段正确且不含凭据。
- 既有测试全绿。

## 6. 风险 / 待定

- **启动项限制**：改开关需重启控制面（与既有全部 `mcp.*` 一致）。若未来需要热更，需另立 FR 并评估运行时目录重建对已建会话的影响。
- **catalog 驱动**：未来新增 `critical` 工具会被生产模式**自动隐藏**，无需改代码——但也意味着提级某个工具会静默改变生产可见面，故 §3.3 的提级必须经评审。
- **待拍板**：`beacon.approvals.approve` / `.reject` 定为 `critical` 会使生产模式下即使开启 `allow-approval-decide` 也无法使用审批决定工具。若部署方需要「生产环境 + 内网闭环审批」组合，需改定 `high`（见 FR-236 spec §3.3）。
