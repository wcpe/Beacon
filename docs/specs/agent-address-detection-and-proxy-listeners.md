# 功能规格：Agent 地址自动探测、BC 多 Listener 与逐项覆盖

> 状态：草拟　·　关联 PRD：FR-204　·　分支：待执行时创建　·　依赖：FR-203

## 1. 背景与目标

当前 Agent 从本地 `identity.address` 上报单个地址，控制面把它直接用于身份记录与发现展示。该做法既要求每台机器重复填写可推导信息，也无法表达 BungeeCord 的多个 listener；在 NAT、容器或反向代理场景下，本地绑定地址也不等于外部可达地址。

本功能建立角色化地址契约：

- Bukkit/Paper 只上报一个实际监听端口，控制面生成一个生效地址。
- BC 上报全部 listener，控制面为每个 listener 分别生成探测地址并允许管理台逐项覆盖。
- 探测 host 来自控制面看到的 TCP 对端地址，Agent 只提供端口与本地绑定事实。
- 旧单地址 `address` / `lastAddr` 契约继续保留，值取首个有效 listener，保证旧 Agent、旧 API 与下游发现兼容。

## 2. 范围与非目标

### 2.1 本期范围

- Bukkit 单端口、BC 全 listener 采集与注册上报。
- 控制面地址探测、独立 endpoint 模型、listener 集合对账。
- 每个 listener 独立覆盖地址。
- 身份详情 API 与 `/servers` 抽屉式管理交互。
- 单地址兼容、旧 Agent 迁移、安全校验、审计与真机验收。

### 2.2 明确不做

- 不把 Bukkit 强行改成业务上的多 listener。
- 不自动探测公网 IP、调用外部“查 IP”服务或扫描网卡。
- 不自动修改 BungeeCord / Paper 原生配置。
- 不在本期引入受信反向代理 CIDR 配置；反代/NAT 场景使用显式覆盖。
- 不新增独立“地址管理”页面。
- 不假设当前项目已有通用服务器详情页；本期复用身份详情 API与 `/servers` 的抽屉交互扩展。

## 3. 设计（怎么做）

### 3.1 术语与权威规则

- **上报绑定**：Agent 从平台 API 读取的本地 bind host 与 port，仅用于标识 listener 和排障，不直接视为外部可达地址。
- **探测地址**：控制面用 TCP 对端 host + Agent 上报 port 生成的地址。
- **覆盖地址**：管理员针对某个 endpoint 显式填写的完整 host:port。
- **生效地址**：有覆盖时取覆盖地址，否则取探测地址。
- **兼容地址**：当前身份的首个 active endpoint 的生效地址；继续映射到旧 `address` / `lastAddr` 字段。

权威优先级固定为：`overrideAddress > detectedAddress`。上报 bind host 永不越过这两层成为外部可达权威。

## 4. Agent 侧采集

### 4.1 Bukkit/Paper

- 通过平台 API 读取服务器实际监听端口，形成唯一 endpoint。
- endpointKey 固定为 `primary`。
- 不读取或上报人工 `identity.address` 作为新协议事实。
- 端口必须在 1..65535；无法取得合法端口时注册 fail-closed 并输出中文 ERROR，不得默认为 25565。

### 4.2 BungeeCord

- 通过 `ProxyServer.getInstance().getConfig().getListeners()` 读取全部启用 listener。
- 每项上报本地 bindHost、port 与配置顺序 ordinal。
- endpointKey 由控制面根据规范化 `bindHost:port` 计算，列表重排不改变 key。
- 同一报文出现重复规范化 listener 返回 400，Agent 不自行去重掩盖配置错误。
- listener 数量上限为 32；超限返回 400 `INVALID_PARAM`，避免无界请求和管理台渲染压力。
- BC 无任何合法 listener 时不得以空列表注册为 active。

规范化规则：host 去首尾空白并转小写；IPv6 去除输入方括号后再按标准形式保存；port 使用十进制；endpointKey 输出采用 `net.JoinHostPort` 等价格式。bindHost 仅用于稳定标识和展示，允许 `0.0.0.0`、`::` 与 loopback。

### 4.3 采集线程

采集在异步启动流程执行，不阻塞 MC 主线程；listener 配置变化在重新注册/重连时对账。本期不新增高频轮询。

## 5. 数据模型

### 5.1 `agent_endpoint`

| 字段 | 类型 | 说明 |
|---|---|---|
| `id` | BIGINT 主键 | 内部主键 |
| `agent_identity_id` | BIGINT，非空，索引 | 所属 agent_identity |
| `endpoint_key` | VARCHAR(320)，非空 | backend 固定 `primary`；proxy 为规范化 bindHost:port |
| `ordinal` | INT，非空 | Agent 上报顺序；兼容地址取最小 ordinal |
| `reported_bind_host` | VARCHAR(255)，非空 | 本地绑定 host，仅展示 |
| `reported_port` | INT，非空 | 本地监听端口 |
| `detected_host` | VARCHAR(255)，非空 | 控制面从 TCP 对端取得的 host |
| `detected_address` | VARCHAR(320)，非空 | detectedHost + reportedPort |
| `override_address` | VARCHAR(320)，可空 | 管理员逐项覆盖的完整地址 |
| `active` | BOOL，非空 | 是否出现在最近一次有效上报集合 |
| `last_seen_at` | DATETIME，非空 | 最近一次上报时间 UTC |
| `created_at` / `updated_at` | DATETIME，非空 | 标准时间戳 |

唯一索引：`(agent_identity_id, endpoint_key)`。

兼容投影字段 `agent_identity.last_addr` 必须随本迁移从 `VARCHAR(64)` 扩为 `VARCHAR(320)`；扩列先于新 endpoint 回填执行，避免合法 IPv6 或较长域名地址在兼容路径被截断。该字段仍不是多 listener 真源。

`effective_address` 不落库，读取时按 `override_address` 是否非空派生，避免第三份可漂移真源。

### 5.2 对账规则

- 注册事务按本轮 endpointKey 集合 upsert；本轮未出现的旧项置 `active=false`，不物理删除，以保留审计与故障排查线索。
- listener 再次出现时复用原行及其 override。
- listener bindHost 或 port 改变会产生新 key；旧项失活，旧 override 不自动搬迁到新 listener。
- backend 永远至多一个 active `primary`；proxy 可有多个 active 项。
- 身份状态变化不删除 endpoint；解绑、拒绝、冲突时它们仅作为历史事实，不参与发现。

## 6. 注册与兼容协议

### 6.1 新 Agent 请求

Bukkit：

```json
{
  "identityId": "uuid-v4",
  "kind": "backend",
  "bootId": "uuid-v4",
  "listenPort": 25565
}
```

BC：

```json
{
  "identityId": "uuid-v4",
  "kind": "proxy",
  "bootId": "uuid-v4",
  "listeners": [
    { "bindHost": "0.0.0.0", "port": 25577, "ordinal": 0 },
    { "bindHost": "0.0.0.0", "port": 25578, "ordinal": 1 }
  ]
}
```

- backend 发送 listeners 或 proxy 发送 listenPort 均返回 400。
- Agent 不上报 detectedHost、overrideAddress 或 effectiveAddress；这些字段由控制面权威生成。
- FR-203 的 identityId、kind、bootId 与 token 鉴权规则继续适用。

### 6.2 旧 Agent 单地址兼容

- 旧请求的 `addr` / `address` 继续接受一个 RC 兼容窗。
- 控制面只解析其中的 port；host 不作为探测 host，避免 Agent 伪造外部地址。
- backend 旧地址映射为 `primary`；proxy 旧地址也先映射为单个兼容 endpoint，直到新 Agent 上报完整 listeners。
- 旧地址无法解析出合法端口时沿用旧协议错误，不创建不完整 endpoint。
- 既有 `agent_identity.last_addr` 扩为 `VARCHAR(320)` 后保留为兼容字段，写入首个 active endpoint 的生效地址，不再承载多 listener 真源。

### 6.3 注册响应

active / disabled 响应 additive 增加：

```json
{
  "address": "203.0.113.10:25577",
  "endpoints": [
    {
      "endpointKey": "0.0.0.0:25577",
      "detectedAddress": "203.0.113.10:25577",
      "overrideAddress": null,
      "effectiveAddress": "203.0.113.10:25577",
      "source": "detected"
    }
  ]
}
```

Agent在进入 Active Runtime 前把兼容 `address` 作为旧 v1 注册、发现与本机只读门面的兼容值；BC 的完整 endpoints 仍以控制面/endpoint模型为真源。

## 7. 来源 IP 安全边界

- 自动探测默认且仅使用 HTTP 连接的原始 TCP `RemoteAddr` host。
- 不读取 `X-Forwarded-For`、`X-Real-IP` 或 Agent 请求体中的 host 生成 detectedAddress。
- 当前审计 `clientIP` 口径会接受代理头，只能继续用于审计展示，不得复用于地址探测。
- 本期不新增受信代理配置；控制面经反向代理部署时，探测值可能是代理地址，管理员必须用逐 listener override 修正。
- 如后续需要代理头探测，必须另立安全规格，要求显式 trusted proxy CIDR、逐跳解析与默认拒绝。

## 8. 管理端 API

### 8.1 身份详情

`GET /admin/v2/agent-identities/{identityId}` additive 返回：

```json
{
  "address": "203.0.113.10:25577",
  "endpoints": [
    {
      "endpointKey": "0.0.0.0:25577",
      "ordinal": 0,
      "reportedBindHost": "0.0.0.0",
      "reportedPort": 25577,
      "detectedAddress": "203.0.113.10:25577",
      "overrideAddress": null,
      "effectiveAddress": "203.0.113.10:25577",
      "source": "detected",
      "active": true,
      "lastSeenAt": "2026-07-28T08:00:00Z"
    }
  ]
}
```

旧 `lastAddr` 继续返回兼容地址。

### 8.2 逐项覆盖

`PUT /admin/v2/agent-identities/{identityId}/endpoints/{endpointKey}`

```json
{
  "overrideAddress": "proxy.example.com:25577",
  "reason": "公网 NAT 映射"
}
```

- `overrideAddress=null` 表示清除覆盖并恢复 detectedAddress。
- 非 active endpoint 允许清除旧覆盖，但不得新增覆盖。
- 地址必须是单个 host:port；port 1..65535；域名按 ASCII/小写规范化；IPv6 必须使用方括号输出。
- 拒绝 URL scheme、路径、userinfo、空白、控制字符与多个地址。
- identity / endpoint 不存在返回 404；非法值返回 400；非 active 新增覆盖返回 409。
- 写操作只允许 full 角色，必须写原因与审计；readonly 返回 403。

## 9. 管理台 UX

- 待确认抽屉展示探测地址摘要：Bukkit 显示单地址，BC 显示 listener 数量与首项，不在列表无界展开。
- 复用现有身份详情 API及 `/servers` 抽屉式交互，扩展身份详情抽屉展示全部 listener；不新增独立路由，也不假设已有通用服务器详情页。
- 每项同时展示“本地绑定、探测地址、覆盖地址、生效地址、来源、最后上报时间”。
- 每个 listener 独立编辑/清除覆盖；保存前二次确认影响地址。
- 失活 listener 标记“已失活”，默认折叠，不参与兼容地址选择。
- 错误信息展示服务端稳定错误码对应的中文文案，不回显请求头或 token。

## 10. 发现、注册表与下游兼容

- 旧发现模型的单个 address 继续取兼容地址。
- backend 兼容地址唯一；proxy 兼容地址取 active endpoints 中 ordinal 最小、再按 endpointKey 稳定排序的首项。
- override 变更后，控制面运行注册表、发现响应与相关拓扑摘要必须原子看到新兼容地址，并触发既有拓扑变更通知。
- 多 listener 列表只通过身份详情/专用管理契约暴露；不得把旧 `ServiceInstance.address()` 破坏性改成列表。
- endpoint 不健康或 identity 非 active 时不因有地址而绕过既有调度与身份门禁。

## 11. 审计、日志与隐私

- 新增 `identity.endpoint_override_changed`；listener 集变化可复用/新增 `identity.endpoints_changed`。
- 审计记录 identityId、serverId、endpointKey、旧/新覆盖的脱敏摘要、原因、操作者与 traceId。
- 不记录 token、完整请求头；来源 IP 按现有隐私规范展示，日志不得高频打印每次相同上报。
- Agent 采集失败、重复 listener、控制面拒绝分别使用中文 ERROR/WARN，包含 identityId 缩略与 listener key，不包含凭据。

## 12. 迁移与发布顺序

1. 实施前新增 ADR，明确角色化地址、TCP 对端信任边界与覆盖优先级；本规格不创建悬空链接。
2. 增加 agent_endpoint 表与读取服务；不改旧 address 返回。
3. 控制面先兼容新 listenPort/listeners 与旧 addr。
4. 发布管理端详情与逐项覆盖 API、管理台抽屉。
5. 发布新 Agent平台采集器；旧 Agent 继续单地址运行。
6. 新 Agent 首次上报时把旧兼容 endpoint 对账为新集合；不自动搬迁无法唯一对应的旧覆盖。
7. 完成一个 RC 观察期与真机验收后，再另行决定移除旧 identity.address 读取。

## 13. 实施任务拆分

1. ADR与线上契约定稿。
2. Go endpoint 模型、迁移、repository 与对账事务。
3. 探测 host 提取器：严格使用 RemoteAddr，与审计 clientIP 分离。
4. Agent平台采集器：Bukkit 单端口、BC 全 listener、输入规范化。
5. 注册客户端与响应解析：新字段 additive、旧 address 兼容。
6. 管理端详情、逐项覆盖、审计与拓扑唤醒。
7. contracts、API client 与身份详情抽屉。
8. 自动化、双 listener 真机、NAT/覆盖场景与 RC 门禁。

## 14. 测试与验收

### 14.1 Agent 单元测试

- Bukkit 采到一个合法端口；缺失/非法端口 fail-closed。
- BC 上报全部 listener，保持配置顺序；顺序变化不改变 endpointKey。
- 重复 listener、空集合、超过 32 项、IPv4/IPv6/通配绑定覆盖。
- 旧 identity.address 仅在兼容窗读取，不覆盖新平台事实。

### 14.2 控制面测试

- TCP RemoteAddr 生成探测 host；伪造 X-Forwarded-For / X-Real-IP 不影响结果。
- backend/proxy 报文角色错配、端口边界、重复键、数量上限。
- endpoint upsert、缺失项失活、重现复用、override 保留。
- override 优先、清除恢复、非 active 写入冲突、权限与审计。
- `last_addr` 扩列迁移保持既有值，320 字符边界内的首项兼容 address 可完整回填；旧 Agent 单 addr 与旧 API shape 回归。
- IPv6 JoinHostPort、域名覆盖、非法 URL/路径/userinfo 拒绝。
- override 更新后注册表、发现与拓扑摘要一致刷新。

### 14.3 前端测试

- Bukkit 单地址与 BC 多 listener 摘要/详情展示。
- 每项独立编辑、清除、确认、readonly 禁用、失活项折叠。
- 服务端错误码与中文错误文案。
- 1000+ 身份列表不因 listener 详情产生 N+1；详情只在抽屉打开时请求。

### 14.4 真机验收

- 一台真实 Paper 上报实际监听端口，控制面生成正确单地址。
- 一台真实 BC 配置至少两个 listener，管理台完整显示两项，旧 address 等于稳定首项。
- 两个 listener 分别设置不同覆盖，重新注册与重启后覆盖仍保持。
- 通过反向代理/NAT 部署时自动探测不误信伪造代理头，显式覆盖后发现地址正确。
- listener 删除后对应项失活，不再进入兼容地址；重新加入后恢复原覆盖。
- 旧 Agent与新控制面、Agent回滚兼容路径各验证一次。

## 15. 风险与收口条件

- 监听 bind 地址并不等于公网可达地址，所以它只能是标识和排障事实，不能提升为探测权威。
- 直接复用当前信任代理头的审计 clientIP 会造成地址伪造风险，是禁止实现方式。
- listener key 随 bindHost/port 改变，旧 override 不自动迁移是有意的 fail-closed 取舍。
- 单元测试绿不能替代 Bungee 多 listener 与 NAT 真机验证；全部真机验收通过前 FR-204 不得标已交付。

## 16. 已拍板决定

- 只有 BC 会有多个 listener；Bukkit 保留单地址语义。
- BC 上报全部 listener，单地址契约升级为角色化契约。
- BC 保留首个有效 listener 作为兼容 address。
- 管理台对每个 listener 分别设置覆盖地址；未覆盖项使用控制面探测 host + 上报端口。
- 控制面使用请求来源 IP 与 Agent 上报监听端口生成探测地址。
- serverId 分配、地址覆盖和迁移状态复用现有待确认流程与 `/servers` 身份详情抽屉式工作流，不新增独立身份配置页面。
