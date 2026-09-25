# 功能规格：Agent 极简接入、控制面身份分配与兼容迁移

> 状态：草拟　·　关联 PRD：FR-203　·　分支：待执行时创建　·　依赖：无

## 1. 背景与目标

当前 Agent 必须在本地 `config.yml` 同时填写控制面地址、namespace、namespace token、serverId、address 等字段。namespace 已能由 token 唯一确定，serverId 又必须经过控制面身份绑定与人工确认，本地继续维护这些业务字段形成了双真源，也容易因复制目录或误改配置造成绑定漂移。

本功能把本地人工配置收口为两类必填信息：

1. Beacon 控制面地址；
2. namespace 接入 token。

namespace 由 token 权威推导，角色由 Bukkit / Bungee 壳层确定，identityId 继续由 Agent 首启生成。新身份进入待确认后，由管理员在既有待确认流程中分配 serverId；确认完成后，Agent 才启动依赖权威 serverId 的 active runtime。已有 Agent 升级时沿用控制面既有绑定，无需批量重新确认。

### 1.1 成功标准

- 全新 Agent 不填写 namespace、serverId 或 address 也能进入正确 namespace 的待确认列表。
- 管理员确认时分配 serverId，确认事务完成前 Agent 不启动指标、目录、调度、消息、文件与交付等 active runtime。
- 已有 active / disabled 身份升级后，identityId、token namespace、kind 与既有绑定一致即可沿用原 serverId，不重新进入 pending。
- 迁移完成后，namespace、serverId、归属与调度业务字段只以控制面为权威；旧本地字段不再持续参与决策。
- 全新且没有任何已确认绑定快照的 Agent 在控制面不可用时不臆造身份；已有有效快照的 Agent 可按 fail-static 规则降级启动。

## 2. 范围与非目标

### 2.1 本期范围

- 极简本地引导配置及旧键兼容读取。
- token→namespace 权威推导。
- 无 serverId 的 pending 身份、确认时分配 serverId。
- bootstrap runtime 与 active runtime 分层。
- 已有 active / disabled 绑定的自动兼容迁移。
- 本地只读绑定快照及冷启动 fail-static。
- 待确认抽屉、身份详情抽屉中的分配与迁移信息。
- 注册、确认、迁移、冲突与审计契约。

### 2.2 明确不做

- 不引入一次性配对码。
- 不允许 Agent 自行选择或生成 serverId。
- 不允许本地配置声明大区、小区、BC 集群或 LobbyCluster 归属。
- 不改变 identityId 文件的生成、损坏 fail-closed 与并发双实例检测语义。
- 不新增独立“Agent 身份配置”页面。
- 不在本规格实现地址探测与 BC 多 listener；地址契约由 FR-204 单独定义。
- 不以旧本地 serverId 为依据把未知身份自动批准为 active。

## 3. 设计（怎么做）

### 3.1 本地引导配置

#### 3.1.1 新配置形态

```yaml
beacon:
  endpoints:
    - "http://127.0.0.1:8848"
  bootstrap-token: "<namespace 接入 token>"
```

人工必填项只有 `beacon.endpoints` 与 `beacon.bootstrap-token`。沿用现有键名，不为同一 token 新造第二个配置键；该 token 的控制面语义收敛为 namespace 接入 token。以下机器相关参数仍可保留安全默认值和本地可选覆盖：

- 请求超时、长轮询超时与退避参数；
- 快照文件名与开关；
- 文件落盘路径、命令白名单等本机安全边界；
- 资产扫描周期等本机运行参数。

身份、归属、调度权重、容量、业务版本等业务配置不得继续以本地文件为持续真源；需要保留的字段由控制面配置或运行事实提供。

#### 3.1.2 旧键兼容

- `identity.namespace`、`identity.server-id`、`identity.address` 仅供旧 Agent 兼容或升级迁移提示，不再是新 Agent 的必填项。
- 兼容窗结束前不得删除服务端对旧注册报文的接收能力；移除旧键须另立发布兼容检查，不在本 FR 静默执行。

#### 3.1.3 自动生成文件

- `identity.yml` 继续保存 identityId，不属于人工配置。
- 新增 Agent 自管的绑定快照，例如 `identity-binding.snapshot.json`；它是控制面已确认事实的本地缓存，不得要求用户手工编辑。
- 所有临时写入先落同目录临时文件，再原子 rename；损坏快照不自动修补、不作为身份依据。

## 4. 数据模型

### 4.1 `agent_identity` 调整

| 字段 | 变更 | 约束 |
|---|---|---|
| `server_id` | `VARCHAR(64)` 改为可空 | 仅 `pending` / `rejected` / `expired` / `unbound` 可为空；`active` / `disabled` / `conflict` 必须非空 |
| `binding_source` | 新增 `VARCHAR(24)`，非空 | `legacy_local` / `admin_assigned` / `machine_registered`（后者由 FR-235 新增：控制面经机器注册通道预置的身份），应用层校验 |
| `legacy_migrated_at` | 新增 DATETIME，可空 | 旧绑定首次被新极简 Agent 成功接管的 UTC 时刻 |

既有行迁移规则：

- 当前所有已存在绑定由旧本地 serverId 引导，迁移时回填 `binding_source=legacy_local`。
- 不修改既有状态、serverId、namespace、boundAt 或审计历史。
- 新建无 serverId pending 行使用 `binding_source=admin_assigned`；管理员确认写入 serverId 后来源保持不变。

### 4.2 状态不变量

- pending 可分为“未分配 serverId”和“旧 Agent 已带候选 serverId”两种数据形态，但状态枚举不增加，避免平行状态机。
- serverId 占用校验只对非空值执行。
- `active`、`disabled`、`conflict` 任一行若 serverId 为空，视为数据完整性错误，数据面鉴权 fail-closed。
- approve 写入 serverId、创建或复用 `server` 行、状态转 active、写审计必须在同一数据库事务内完成。
- serverId 仍在 namespace 内业务唯一；终结态历史行的兼容规则沿用既有身份规格。

## 5. 注册协议与状态机

### 5.1 注册请求

`POST /beacon/v2/agent/register`

新 Agent 请求体：

```json
{
  "identityId": "uuid-v4",
  "kind": "backend",
  "bootId": "uuid-v4",
  "agentVersion": "0.31.0"
}
```

- namespace 不在请求体声明，由 `X-Beacon-Token` 唯一解析。
- kind 由平台壳层固定为 `backend` 或 `proxy`，不可从本地业务配置读取。
- 旧 Agent 可继续发送 `serverId`；服务端把它视为 `legacyServerId` 兼容输入。
- 服务端不得接受请求体 namespace 覆盖 token 所属 namespace。

### 5.2 注册分支

| 条件 | 结果 |
|---|---|
| identityId 无记录，新 Agent 未带 serverId | 建 `pending`，serverId=NULL，返回 202 |
| identityId 无记录，旧 Agent带 serverId | 建兼容 `pending`，暂存该 serverId，仍须人工确认 |
| identityId 已 active / disabled，token namespace、kind 与既有绑定一致 | 沿用 DB serverId，更新 bootId / 版本 /运行事实，返回既有状态；不重审批 |
| identityId 已 active / disabled，但旧请求所带 serverId 与 DB 不同 | 409 `identity_binding_mismatch`，不修改绑定 |
| identityId 已 pending | 刷新过期时间与运行事实；不得用重复注册暗改已暂存的 serverId |
| identityId 已 expired / unbound | 回 pending；新 Agent serverId 置空，重新由管理员分配；旧兼容请求可暂存候选值 |
| identityId 已 rejected / conflict | 沿用既有拒绝与冲突处置 |

未知身份即使携带旧 serverId，也只能进入 pending，禁止凭本地值自动 active。所谓“旧 Agent 自动迁移”只适用于控制面已经存在且 identityId 匹配的既有绑定。

### 5.3 注册响应

```json
{
  "status": "pending",
  "namespace": "prod",
  "serverId": null,
  "expiresAt": "2026-07-31T08:00:00Z",
  "bindingSource": "admin_assigned",
  "migrationState": "not_required"
}
```

- pending 时 `serverId` 可为 null；active / disabled 时必须为非空字符串。
- `migrationState`：`pending` / `completed` / `not_required`。
- 旧响应字段只增不改；旧 Agent 收到其兼容 pending 时仍返回暂存的 serverId。
- active / disabled 响应必须额外返回控制面权威 `boundAt` 与 `bindingFingerprint`；后者由控制面对 `identityId`、namespace、serverId、kind、boundAt 生成稳定 SHA-256 十六进制摘要，不含 token 或密钥。pending 等非活跃状态不得伪造这两个字段。

### 5.4 管理端确认

`POST /admin/v2/agent-identities/{identityId}/approve`

请求体新增：

```json
{
  "serverId": "lobby-1",
  "forceUnbindOccupier": false
}
```

规则：

- 任一 pending 身份的 `serverId` 均为必填；旧 Agent 已带候选值时，候选值仅可作为管理端输入提示，服务端不得在请求省略该字段时回退沿用。
- serverId 去首尾空白后校验长度、字符集与 namespace 内占用；非法返回 400，已占用返回 409。
- Q3 强制解绑、换区重确认的目标处理继续沿用既有事务语义。
- 非 pending 调用返回 409 `illegal_state`。

## 6. Bootstrap 与 Active Runtime 分层

### 6.1 Bootstrap Runtime

Agent 启动后首先只装配以下最小组件：

- 本地配置读取与环境变量覆盖；
- identity.yml 读取/生成；
- bootId 生成；
- 平台 kind 判定；
- 最小注册 HTTP 客户端与 pending 状态轮询；
- FR-204 定义的监听端口事实采集；
- 绑定快照读取与写入。

Bootstrap Runtime 不得启动依赖 serverId 的 MessageBus、调度视图、指标上报、目录同步、连接上报、文件同步、命令执行或交付运行时。

### 6.2 Active Runtime

- 注册返回 active 且携带权威 namespace、serverId 后，构造完整 AgentIdentity 并只装配一次 Active Runtime。
- pending→active 的长轮询响应必须携带权威绑定，Agent 无需重启即可进入 active。
- active→unbind / conflict / disabled 时停止对应数据面循环并回到受限状态；不得继续用旧 serverId 发请求。
- reconnect 不得并发创建两套 Active Runtime；沿用现有注册单飞与代际门。

### 6.3 绑定快照与 fail-static

快照至少包含 identityId、namespace、serverId、kind、boundAt、控制面返回的 `bindingFingerprint` 与格式版本，不含 token。Agent 只接受格式匹配且字段完整的控制面确认值，不得以本地时间或响应哈希替代。

- 全新 Agent 无有效快照且控制面不可用：保持 bootstrap/degraded，不启动 Active Runtime。
- 已有有效快照且 identityId、kind 与本机一致：可用最后确认的 namespace/serverId 启动 fail-static Active Runtime，同时后台重连对账。
- token 成功解析到的 namespace 与快照不同：快照立即失效，禁止跨 namespace 降级启动。
- 控制面返回 unbound、rejected、conflict 或绑定不匹配：停止使用快照并按权威状态处置。
- 快照只用于控制面短时不可用，不允许用户编辑，也不恢复已被控制面否决的绑定。

实施前须新增 ADR，明确取代 ADR-0014 中“serverId 本地真源”和启动期必须在线取得身份的旧决定；本规格不创建尚不存在的 ADR 链接。

## 7. 管理台 UX

### 7.1 待确认抽屉

- 无 serverId 条目显示“待分配 serverId”，不显示空字符串占位。
- 点击确认必须填写 serverId，并即时显示格式与占用错误。
- 展示 identityId 缩略、kind、namespace、Agent 版本、迁移来源及 FR-204 地址摘要。
- 拒绝、Q3 强制解绑与换区预填继续复用现有交互。

### 7.2 身份详情抽屉

- 复用 `/servers` 已有抽屉式交互与身份详情 API，扩展身份详情抽屉；不新增独立身份配置页面，也不假设项目已有通用服务器详情页。
- 展示 `bindingSource`、`migrationState`、`legacyMigratedAt`、当前权威 namespace/serverId 与快照最近确认时间。
- 只读角色仅可查看；分配、解绑、禁用等写操作沿用 full 角色限制与确认弹窗。

## 8. 审计、安全与日志

- 新增或扩展审计：`identity.registered`、`identity.approved`、`identity.legacy_migrated`。
- 审计包含 namespace、identityId、最终 serverId、来源、前后状态、操作者与 traceId；不得记录 token、绑定快照内容或完整请求头。
- token 比较继续使用哈希与恒定时间比较；日志只写“token 无效”，不输出明文或摘要。
- 所有日志使用中文；pending 重试不得刷屏。
- 数据面继续以鉴权中间件注入的 DB 权威 namespace/serverId 为准，不信任请求体或本地快照自报。

## 9. 向后兼容与迁移顺序

1. 先迁数据库：server_id 可空、新增来源字段并回填既有行。
2. 先发布兼容控制面：同时接受旧、新注册报文与 approve 请求。
3. 再发布新 Agent：启动时读取旧键作为迁移提示，active 成功后写绑定快照。
4. 再发布管理台：确认时显式分配 serverId，展示迁移状态。
5. 至少跨一个 RC 观察旧 Agent 接入与回滚能力，再另行决定删除旧本地键解析。

控制面回滚时，新 Agent 应保留旧键兼容提示或明确拒绝并提示版本不兼容；不得把无 serverId 注册发送给不支持该协议的旧控制面后无限刷请求。

## 10. 实施任务拆分

1. 新 ADR与协议定稿：身份真源、启动 fail-static、兼容窗。
2. 数据模型与迁移：agent_identity 可空 serverId、来源字段、不变量测试。
3. Go 注册服务：token 推 namespace、无 serverId pending、既有 active 兼容、approve 分配事务。
4. Agent bootstrap 分层：最小注册客户端、Active Runtime 延迟装配、代际与停机收口。
5. 绑定快照：原子存储、校验、跨 namespace 失效、降级启动。
6. 管理台与 contracts：nullable serverId、确认输入、迁移信息、身份详情抽屉。
7. 文档与示例配置：只保留两项必填，说明旧键兼容期。
8. 自动化、真机与 RC 门禁。

## 11. 测试与验收

### 11.1 自动化测试

- token 正确推导 namespace；请求体伪造 namespace 无效。
- 新身份无 serverId 进入 pending，除注册/状态查询外数据面全部拒绝。
- approve 缺 serverId、非法格式、重复占用分别返回确定错误；成功时绑定、server 行、状态与审计同事务落地。
- 旧 active / disabled 身份在 identityId、namespace、kind 匹配时不重审批；本地 serverId 与 DB 不同 fail-closed。
- pending、expired、unbound、rejected、conflict 全状态迁移回归。
- 无快照冷启动不装配 Active Runtime；有效快照降级启动；namespace 不匹配快照失效。
- Bootstrap/Active Runtime 单飞，重复注册与重连不产生双循环。
- 旧 Agent 报文、旧 approve 请求、旧响应解析兼容。
- 管理台 nullable serverId、分配校验、权限与错误展示测试。

### 11.2 真机验收

- 全新 Paper 与 BC 均只填写 `beacon.endpoints` 与 `beacon.bootstrap-token`，能进入待确认并由管理员分配 serverId 后无重启上线。
- 已有 Paper 与 BC 带旧配置升级，身份一致时保持 active，业务 serverId 不变且出现迁移完成记录。
- 控制面断开时，有有效绑定快照的已接入 Agent按 fail-static 启动；全新 Agent 不误启动。
- 解绑后旧快照不能让 Agent继续以旧绑定访问数据面。
- 日志、审计、配置文件和网络错误均不泄露 token。

## 12. 风险与收口条件

- `AgentIdentity` 当前在完整装配期被广泛当作不可变对象使用；若不先拆 Bootstrap Runtime，pending 无 serverId 会被空字符串扩散。此项是实现硬门。
- server_id 改可空影响查询、关键词过滤与约束，所有 active 数据面入口必须增加非空不变量测试。
- fail-static 快照改变 ADR-0014 的启动语义，必须先有新 ADR 再写实现。
- 只有自动化绿不足以证明迁移安全；Paper、BC 新装与旧装升级均通过真机验收后，FR-203 才可交付。

## 13. 已拍板决定

- 本地人工必填只有 Beacon 地址与 namespace token。
- namespace 由 token 推导，serverId 在待确认流程分配，角色由平台识别。
- 机器相关参数可保留默认值与可选本地覆盖；业务配置面板化。
- 已有 Agent 自动导入既有绑定，identityId 与绑定一致时保持 active，无需重新审批。
- 迁移完成后控制面成为身份业务字段真源。
- 待确认与身份详情沿用现有 `/servers` 工作流，不新增独立身份配置页面。
