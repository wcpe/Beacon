# 功能规格（specs）

非平凡功能在动手前先写一份**工作规格**：一个功能一个文件 `docs/specs/<feature>.md`，把"要什么 / 怎么做 / 任务 / 验收"集中一处，再实现。模板见 [`_template.md`](_template.md)。

## 何时写

- **写**：新增一个非平凡功能 / 能力（尤其 P2/P3，如配置灰度、流量调度），或任何够得上一个分支 / PR 的功能。
- **不写**：小改动、bug 修复、重构、依赖升级——走 PRD 状态列 + 对应技能即可。别为每个小改动建 spec（简单优先）。

## 与项目级文档的分工（别双源打架）

- `docs/PRD.md`：持久路线图——该功能在 PRD 是**一行 FR + 状态**。
- `docs/specs/<feature>.md`：该功能**开发期的详细工作规格**（比 PRD 那行细）。
- 交付后：持久真相归并回 PRD（FR 标 `已交付@vX.Y.Z`）+ `ARCHITECTURE.md`（更新到现状）+ ADR（若有架构决策）；spec 留作该功能的历史记录，基本不再改。

## 怎么用

1. 复制 `_template.md` 到 `docs/specs/<feature>.md`。
2. 填需求 / 设计 / 任务 / 验收。
3. 按 `sdd-develop-feature` 技能实现，对着 spec 的任务与验收推进。
4. 交付后归并回项目级文档（见上）。

## FR-205～220 执行规格索引

本批资源标识、生命周期、审批、观测范围与 MCP 工作以 `docs/PRD.md` §4/§6 为路线图，以以下 spec 为施工真源；实现时不得以 `.tmp/` 过程计划替代正式规格。

| 工作项 | 规格 | 说明 |
|---|---|---|
| BUG-FR178-SCOPE | [authoritative-observation-scope-contract](authoritative-observation-scope-contract.md)、[header-env-namespace-cascade-filter](header-env-namespace-cascade-filter.md) | 先封闭失效/越权 scope 回落全量和客户端分页后过滤，再由 FR-213/214 归真。 |
| BUG-NAMESPACE-DELETE | [namespace-crud](namespace-crud.md)、[namespace-permanent-deletion-and-tombstone](namespace-permanent-deletion-and-tombstone.md) | 旧 namespace DELETE 固定迁移为 410，不创建审批、不删除资源。 |
| FR-205 | [stable-business-identifiers-and-display-names](stable-business-identifiers-and-display-names.md) | 稳定业务标识与可变显示名称。 |
| FR-206 | [human-and-machine-principals-and-capability-authorization](human-and-machine-principals-and-capability-authorization.md) | 统一主体、能力与语义 operation 授权。 |
| FR-207 | [dangerous-operation-approval-core](dangerous-operation-approval-core.md) | 通用危险操作审批内核与持久执行。 |
| FR-208 | [identity-credential-trust-and-topology-dangerous-operations](identity-credential-trust-and-topology-dangerous-operations.md) | 身份、凭据、信任与拓扑危险操作适配。 |
| FR-209 | [agent-command-and-sensitive-content-approval](agent-command-and-sensitive-content-approval.md) | Agent 命令与敏感内容访问审批。 |
| FR-210 | [control-plane-upgrade-rollback-and-settings-approval](control-plane-upgrade-rollback-and-settings-approval.md) | 控制面升级、回滚与系统设置审批。 |
| FR-211 | [config-file-override-set-and-delivery-approval](config-file-override-set-and-delivery-approval.md) | 配置、文件、覆盖集与交付审批适配。 |
| FR-212 | [approval-center](approval-center.md) | 顶级全局审批中心。 |
| FR-213 | [authoritative-observation-scope-contract](authoritative-observation-scope-contract.md) | 服务端权威观测范围契约。 |
| FR-214 | [header-env-namespace-cascade-filter](header-env-namespace-cascade-filter.md) | 页眉 env→namespace 级联筛选。 |
| FR-215 | [server-archive-and-restore](server-archive-and-restore.md) | 服务器归档与恢复。 |
| FR-216 | [namespace-archive-and-restore](namespace-archive-and-restore.md) | namespace 归档与恢复。 |
| FR-217 | [server-permanent-deletion-and-tombstone](server-permanent-deletion-and-tombstone.md) | 服务器永久删除与墓碑。 |
| FR-218 | [namespace-permanent-deletion-and-tombstone](namespace-permanent-deletion-and-tombstone.md) | namespace 永久删除与子树墓碑。 |
| FR-219 | [built-in-admin-v2-mcp-and-oauth](built-in-admin-v2-mcp-and-oauth.md) | 内置 `/admin/v2/mcp` 与 OAuth Client Credentials。 |
| FR-220 | [mcp-domain-tools-and-approval-handoff](mcp-domain-tools-and-approval-handoff.md) | MCP 显式领域工具与审批交接。 |

> spec 是 🌡 中频文档：功能开发期动，交付后基本不动。涉及架构决策时在 spec 里**引用** ADR，不重复决策正文。
