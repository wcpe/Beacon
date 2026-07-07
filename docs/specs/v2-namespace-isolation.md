# 规格：namespace 强隔离与互通信任（第二版）

> 状态：已实现（P1 基础闭环） · 关联 FR：FR-142 · 阶段：P1（0.21.x）

## 1. 背景与目标

namespace 是第二版的强隔离边界（PRD §3）：默认禁止跨 namespace 调度、消息和 Agent 操作，跨域互通只能通过后台显式配置的信任关系开放，且跨域行为必须额外审计。本规格定义隔离在各业务面的具体落点（每面一条可执行的硬规则）、namespace_trust 的能力模型与生命周期、以及 `/namespaces` 管理页的 API 契约。

## 2. 范围

**做**：

- 隔离在注册 / 调度 / 跨服消息 / Agent 操作 / 配置 / 变更单六个面的硬规则。
- namespace_trust 的 capability 枚举（本文权威）、语义与生效机制。
- 信任关系建立 / 收回流程与状态机。
- 跨域行为的额外审计要求。
- `/namespaces` 页（UX.md §2：namespace 管理与互通信任关系）的管理面 API。

**不做**：

- namespace / namespace_trust 表结构 → [v2-zone-authority.md](v2-zone-authority.md) §3.2 / §3.3（本文引用不复制）。
- 管理台登录鉴权与 API 密钥（沿用既有机制，基座 §2）。
- namespace 内部的细粒度 RBAC（管理台操作者对全部 namespace 可见可管，第二版不做按 namespace 的后台权限切分）。
- 配置与变更单的跨域开放——这两个面**绝对禁止**跨域，任何 capability 均不适用（FR-160 / FR-162）。

## 3. 数据模型

表结构引用 [v2-zone-authority.md](v2-zone-authority.md)：`namespace`（§3.2，含 `access_token_hash`）、`namespace_trust`（§3.3）。本文权威定义其中两个 VARCHAR 枚举的取值：

### 3.1 `namespace_trust.capability` 枚举（本文权威）

| 取值 | 语义 | 覆盖的默认禁令（PRD §3） |
|---|---|---|
| `schedule` | from_ns 的调度请求可将 to_ns 的服务器纳入候选；隐含对 to_ns 可调度名册的只读发现 | 跨 namespace 调度 |
| `message` | from_ns 的服务器可向 to_ns 的服务器发送跨服消息（含消息路由目标解析） | 跨 namespace 通信 |
| `agent_ops` | from_ns 侧发起、以 to_ns 服务器为目标、经控制面编排的 agent 操作类指令 | 跨 namespace Agent 操作 |

- 三个取值一一对应 PRD 的三条默认禁令，不设通配值（`all` 之类），需要多能力就建多条记录——授权范围显式可见。
- `agent_ops` 的具体指令白名单由各域规格在定义指令时逐条声明「是否支持跨域」，**默认不支持**（见 §8）。
- 配置与变更单不在枚举内：无任何 capability 能开放（§2 不做）。

### 3.2 `namespace_trust.status` 枚举（本文权威）

`active`（生效）/ `revoked`（已收回）。同一 `(from, to, capability)` 三元组唯一复用一行（表约束见 v2-zone-authority.md §3.3），历史流水由审计承载。

## 4. 机制与状态机

### 4.1 六个面的隔离硬规则

每面一条硬规则，实现处必须能落到单点检查（service 层守卫函数），禁止散落在 handler：

| 面 | 硬规则 |
|---|---|
| **注册** | agent 请求携带的 namespace 必须与 `X-Beacon-Token` 对应的 namespace 一致，不一致返回 401；注册产生的 server / agent_identity 行永远落在 token 所属 namespace，请求体中任何字段不得改写归属。serverId / identityId 的唯一性只在 namespace 内判定（v2-zone-authority.md §3.6 / §3.7 唯一索引）。**信任关系不适用于注册面**——token 决定归属，无跨域注册概念 |
| **调度** | 调度候选集合恒定过滤 `candidate.namespace == requester.namespace`，除非存在 `active` 的 trust(from=请求方 ns, to=候选 ns, capability=`schedule`)；跨域候选进入决策时，决策记录必须打 `cross_namespace=true` 标记（记录结构归 v2-metrics-health-scheduling.md） |
| **跨服消息** | 消息路由的目标解析限定发送方同 namespace；仅当存在 `active` 的 trust(capability=`message`) 才放行至 to_ns 目标；跨域消息的元数据必须携带 from_ns / to_ns 标记（存储结构归 v2-connection-message-storage.md） |
| **Agent 操作** | 控制面编排的 agent 指令，其目标 server 必须与操作上下文的 namespace 一致；由 from_ns 侧（agent-api 转发）发起的跨域目标指令，仅当存在 `active` 的 trust(capability=`agent_ops`) 且该指令声明支持跨域时放行，否则 403。管理台操作者不受此限（后台对全 namespace 可管，见 §2 不做） |
| **配置** | 配置作用域树以 namespace 为根，键解析与继承**绝不跨 namespace**；任何接口不得以他域 scope 读写配置（FR-160「跨 namespace 配置不可串用」）。无 capability 可开放 |
| **变更单** | 变更单的黄金模板源与全部目标 server 必须同 namespace，创建时校验、跨域直接拒绝 400（FR-162「跨 namespace 变更单被拒绝」）。无 capability 可开放 |

agent 面的可见性同规则：`/beacon/v2/agent/*` 的名册 / 发现 / 拓扑类查询默认只返回本 namespace 数据；持有 `schedule` 信任时按 §3.1 语义扩展到 to_ns 的可调度名册。

### 4.2 信任关系生命周期

```
（无记录）──授予──▶ active ──收回──▶ revoked
                     ▲                  │
                     └────重新授予──────┘   （复用同一行，更新 granted_*）
```

**建立（授予）**：

1. 集群管理员在 `/namespaces` 页发起：选 from_ns、to_ns、capability，填写建立原因（必填）。
2. 高风险写操作：影响预览（明示「X 域将获得对 Y 域的 <capability> 能力」）+ 二次确认（UX.md §4 写操作范式）。
3. 服务端校验：from ≠ to；两域均存在；三元组无 `active` 行（已存在 → 409）。
4. 事务内写 namespace_trust（`active`）+ 审计条目；提交后刷新进程内信任快照（§4.3），即时生效。

**收回**：

1. 在信任列表对某行发起收回，填写收回原因（必填）+ 二次确认。
2. 事务内置 `revoked` + revoked_by / revoked_at / revoke_reason + 审计；提交后刷新快照。
3. 生效语义：**新请求立即按无信任处理**；在途行为（已投递的消息、已完成的调度决策）不追溯撤销。

**重新授予**：对 `revoked` 行再次授予时复用该行置回 `active`（更新 granted_by / granted_at / note），流程与建立相同；避免与唯一索引冲突，历史由审计追溯。

### 4.3 生效机制（信任快照）

- 信任关系真源在 MySQL（事实类数据，基座 §1）；为避免调度 / 消息热路径逐请求查库，控制面维护进程内信任快照（读多写少，RWMutex 保护），**写事务提交成功后同步重载快照**——与「先提交后唤醒」纪律一致。
- 快照只含 `active` 行；检查函数签名形如 `allowed(fromNS, toNS, capability) bool`，六面守卫统一调用。
- 控制面重启从 DB 全量重建快照；无跨实例一致性问题（控制面单实例，禁分布式组件）。

### 4.4 跨域行为的额外审计（FR-142 验收硬项）

在常规操作审计之外，**每次信任能力被实际行使**都必须产生独立审计条目：

- action 统一前缀 `cross_namespace.*`（如 `cross_namespace.schedule`、`cross_namespace.message`、`cross_namespace.agent_ops`），便于 `/audits` 按前缀过滤出全部跨域行为。
- 条目必含：from_ns、to_ns、capability、所依据的 trust 行 id、发起方标识（serverId / 操作者）、目标标识、时间。
- 高频面（调度 / 消息）允许按「同一 trust 行 + 同一发起方 + 分钟窗口」聚合计数写审计，避免刷屏（聚合粒度见 §8）；管理面授予 / 收回操作逐条记录，不聚合。
- 信任关系本身的授予 / 收回入常规审计（action：`namespace_trust.grant` / `namespace_trust.revoke`），含原因字段。

### 4.5 namespace 本体管理机制

- **创建**：name 全局唯一；创建成功即生成接入 token，明文**仅在创建响应中返回一次**，库中只存 sha256（v2-zone-authority.md §3.2）。不预置默认 namespace，首个由管理台创建（见 §8）。
- **改名**：不支持。name 是 agent 配置携带的接入标识，改名会使全域 agent 静默失联；需要更名走「建新域 + 迁移 + 删旧域」。
- **token 轮换**：`token/rotate` 生成新 token 并立即使旧 token 失效（新明文仅返回一次）；该域所有 agent 在换用新 token 前请求将 401——轮换属高风险操作，需二次确认并在响应中明示影响（见 §8）。
- **删除**：存在 server、bc_cluster 或任意方向的 `active` 信任行 → 409 拒绝并列出阻断原因；删除入审计。

## 5. API 契约（管理面 `/admin/v2/*`）

错误统一 `{code, message, traceId}`；列表统一分页搜索参数。区服结构与分配端点见 [v2-zone-authority.md](v2-zone-authority.md) §5。

| 方法 | 路径 | 请求要点 | 响应要点 |
|---|---|---|---|
| GET | /admin/v2/namespaces | keyword?, page | 列表：id、name、description、server 数、bc_cluster 数、生效信任数（双向）、createdAt |
| POST | /admin/v2/namespaces | name, description | 新 namespace + **一次性明文 token** |
| PATCH | /admin/v2/namespaces/{id} | description | 更新后 namespace（name 不可改，§4.5） |
| DELETE | /admin/v2/namespaces/{id} | — | 204；有 server / 集群 / 生效信任 → 409（列出阻断原因） |
| POST | /admin/v2/namespaces/{id}/token/rotate | — （二次确认由前端承载） | 新一次性明文 token；旧 token 即时失效 |
| GET | /admin/v2/namespace-trusts | fromNamespaceId?, toNamespaceId?, capability?, status?, page | 信任行列表（含授予 / 收回人、时间、原因） |
| POST | /admin/v2/namespace-trusts | fromNamespaceId, toNamespaceId, capability, note（必填） | 新增或复活信任行（`active`）；重复 → 409 |
| POST | /admin/v2/namespace-trusts/{id}/revoke | reason（必填） | 收回后的信任行 |

`/namespaces` 页数据流：列表页 = GET namespaces + 每行展开双向信任摘要；信任管理 = GET/POST namespace-trusts + revoke。跨域行为的审计追溯跳转 `/audits?actionPrefix=cross_namespace.`。

## 6. 与其他规格的边界

| 方向 | 内容 |
|---|---|
| 依赖 [v2-zone-authority.md](v2-zone-authority.md) | namespace / namespace_trust 表结构、server 归属字段与唯一索引；本文只定枚举与语义 |
| 交给 v2-agent-identity.md | 注册面硬规则的流程细节（token 校验时机、待确认状态、identity 绑定）；本文只定「token↔namespace 必须一致」这条边界 |
| 交给 v2-metrics-health-scheduling.md | 调度面守卫的执行位置、决策记录 `cross_namespace` 标记的字段落点、agent-api 名册扩展 |
| 交给 v2-connection-message-storage.md | 消息面守卫的执行位置、跨域消息元数据 from_ns / to_ns 字段落点 |
| 交给 v2-delivery-orchestration.md | 变更单创建时的同 namespace 校验实现（本文定规则：跨域直接拒绝） |
| 交给 v2-config-center.md | 配置作用域以 namespace 为根的解析实现（本文定规则：绝不跨域、无信任可开放） |

## 7. 验收标准（对齐 PRD FR-142 验收摘要）

1. 两个 namespace 各自 token 注册的 agent：用 A 域 token 携带 B 域 namespace 注册返回 401；A 域可注册与 B 域同名的 serverId 互不冲突。
2. 无信任时：跨域调度候选为空（决策记录含同域过滤证据）、跨域消息被拒且有明确错误、跨域 agent 指令 403、以他域 scope 读写配置被拒、含他域目标的变更单创建被拒 400。
3. 建立 trust(A→B, `schedule`) 后：A 域调度候选可含 B 域可调度服务器，决策记录带 `cross_namespace=true`；B→A 方向仍被拒（单向性）；`message` / `agent_ops` 行为仍被拒（能力最小化）。
4. 每次跨域能力行使产生 `cross_namespace.*` 前缀审计条目（含 from_ns / to_ns / capability / trust id），`/audits` 可按前缀过滤。
5. 收回信任后新请求立即被拒（无需重启控制面）；收回行保留 revoked_by / revoked_at / revoke_reason；重新授予复用同行置回 `active`。
6. 授予 / 收回均要求原因必填并入审计；`/namespaces` 页可完成创建域、轮换 token（明文只显示一次）、建立 / 收回信任全流程。
7. 删除有 server / 集群 / 生效信任的 namespace 返回 409 并列出阻断原因。
8. 控制面重启后信任快照从 DB 正确重建，隔离行为与重启前一致。

## 8. 风险 / 待定（默认决定待拍板）

1. **token 轮换即时失效**：轮换后旧 token 立即 401，该域 agent 在换配置前全体失联（fail-static 保玩家进服不受影响，但控制面功能中断）。备选「双 token 宽限期」被简单优先原则搁置，待拍板。
2. **namespace 不可改名**：以「建新迁移」替代改名；若运维强诉求原地改名，需设计 agent 侧同步换名机制（复杂度高，暂不做）。
3. **不预置默认 namespace**：全新安装首个 namespace 由管理台创建，agent 接入前必须先建域发 token；若希望开箱即用可预置 `default` 域，待拍板。
4. **`agent_ops` 指令白名单**：哪些 agent 指令支持跨域由各域规格逐条声明、默认不支持——P1 基础闭环落地时该能力可能是「有枚举无消费者」的空集，是否推迟 `agent_ops` 枚举到有真实消费者时再加，待拍板（当前保留以完整覆盖 PRD 三禁令）。
5. **高频跨域审计聚合**：调度 / 消息面按「trust 行 + 发起方 + 1 分钟窗口」聚合计数写审计，管理操作不聚合；聚合窗口与是否需要逐条模式开关待拍板。
6. **收回不追溯在途行为**：已投递消息、已生效调度不撤销；若要求收回时强制断开已建立的跨域会话，需消息域配合定义断连语义。
7. **后台无按 namespace 的 RBAC**：管理台操作者全域可管（PRD 未要求后台分域授权）；若后续需要，属新 FR。
