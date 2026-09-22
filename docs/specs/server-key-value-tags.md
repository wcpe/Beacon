# 功能规格：服务器键值标签

> 状态：已实现（单测通过；真机验收待做）　·　关联 PRD：FR-227（与 FR-29 发现过滤对齐）　·　分支：feature/server-key-value-tags

## 1. 背景与目标

运维想给服务器打业务标记（如 `env=beta`、`owner=team-a`、`tier=core`），用于**批量识别与筛选**。现有发现接口已支持按 `tag.<key>=<value>` 过滤（FR-29），但**该能力依赖实例 `metadata`，且没有面向 server 的标签管理与展示**；管理台资产 / 拓扑也看不到标签。

本 FR 让 **server 具备可维护的 `key=value` 标签**，与 FR-29 的过滤口径统一。属 P3。

## 2. 需求（要什么）

- server 支持**多个** `key=value` 标签（key 唯一）。
- 可**增 / 删**标签（单条或批量）。
- **展示**：资产列表、服务器详情、拓扑（可选）可显示标签。
- **筛选**：资产列表可按 tag 过滤（多 tag 取交集，与 FR-29 一致）。
- 写操作**落审计**。

范围内：server 标签的模型、接口、展示、筛选、审计。
不做（范围外）：标签驱动的自动调度 / 自动分配规则（可作后续 FR）；标签继承（namespace → server）；自由文本无序标签（本 FR 明确取 **key=value**）。

## 3. 设计（怎么做）

- **数据模型**：`server` 关联 `server_tag`（`server_pk` + `tag_key` + `tag_value`，唯一键 `(server_pk, tag_key)`）。key 非空、长度受限；value 允许空串。
- **与 metadata 的关系**：**同源**——以 `server_tag` 为**唯一真源**，FR-29 的 `tag.<key>=<value>` 过滤**直接读它**（避免两份与数据迁移）（见 §7）。
  - ⚠️ **破坏性语义变更**：此前 `tag.*` 过滤匹配的是**实例注册 metadata**（agent 上报、仅内存、不落库）。改为读 `server_tag` 后，**只对经标签接口写入的标签生效**；依赖 agent metadata 打标签的既有用法会**静默不再命中**。
  - **无法自动回填**：registry metadata 为**内存态**（agent 注册时上报、不持久化），无历史数据可迁移——运维需在升级后经标签接口重新登记所需标签。已在 CHANGELOG 以「变更」显式声明。
- **接口**：
  - `PUT /admin/v2/servers/{serverId}/tags`（整体覆盖或按 key 增改）
  - `DELETE /admin/v2/servers/{serverId}/tags/{key}`
  - 列表：`GET /admin/v2/servers?tag=k:v&tag=k2:v2`（多 tag 交集）
- **校验**：key 格式（如 `[a-zA-Z0-9_.-]+`）、数量上限、单 key 单值（重复即覆盖）。
- **审计**：每次增 / 删 / 覆盖写 `server.tag_updated` 类审计（含 key、新旧值、操作者）。
- **权限**：**低风险直执 + 强审计**（与 FR-221 建树同类，见 §7）。

## 4. UX / 交互

- 用户任务：给一批服务器打标签，并按标签筛出它们。
- 进入路径：`/servers` 行内「标签」动作 / 详情抽屉；列表顶部 tag 筛选器。
- 操作闭环：选中服务器 → 加标签 → 列表出现标签 chip → 用 chip 或筛选器按 tag 过滤 → 结果一致。
- 状态设计：无标签 → 空态；标签过多 → 折叠 +「+N」；无匹配结果 → 空态带「清除筛选」。
- IA 挂载：集群域 `/servers`；不新增页面。

## 5. 任务拆分

- [x] 数据模型：`server_tag` 表 + 迁移 + 唯一约束
- [x] 落实「同源」：FR-29 `tag.*` 过滤改读 `server_tag`（§7 拍板）
- [x] 增 / 删 / 覆盖接口 + 校验 + 审计
- [x] 列表 tag 过滤（多 tag 交集）
- [x] 前端：标签 chip 展示、编辑、筛选器
- [x] 文档同步：PRD 状态、API、ARCHITECTURE、CHANGELOG

## 6. 验收标准

- 可给 server 加 `/删` `key=value`；重复 key 覆盖而非重复行。
- `GET /admin/v2/servers?tag=env:beta&tag=tier:core` 只返回两个 tag 都命中的 server（交集）。
- 资产列表 / 详情可见标签；拓扑（若做）一致。
- 每次写操作落审计，含 key 与新旧值。
- 非法 key / 超上限被拒绝，错误码可区分。

## 7. 已确认默认（评审拍板）

- **与 FR-29 metadata 的关系**：**同源**——以 `server_tag` 为**唯一真源**，FR-29 的 `tag.<key>=<value>` 过滤**直接读它**（避免两份与数据迁移）。
- **审批口径**：**低风险直执 + 强审计**。
- **上限**：`key ≤ 32`、`value ≤ 128`、单 server `≤ 20` 个标签。
- **筛选范围**：**不纳入** FR-213 观测范围契约（资产维度，非观测维度）。
