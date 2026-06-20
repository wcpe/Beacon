# Beacon REST 契约（第一期）

> 各端对齐的硬契约。Base：`/beacon/v1`（agent 侧）、`/admin/v1`（admin/UI 侧）。内容类型 `application/json; charset=utf-8`。

## 通用约定

- 统一错误体：`{ "code": "<业务码>", "message": "<中文说明>", "traceId": "<可选>" }`。
- HTTP 状态：400 参数错 / 401 缺 token 或登录令牌 / 404 不存在 / 409 冲突 / 422 校验失败 / 500 内部错；**304 仅用于长轮询无变更超时**。
- 鉴权：admin 端需登录令牌（见下「管理面鉴权」，自 P2 前移本批，见 [ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)）；agent 端用共享 `X-Beacon-Token` 仅防误连（非安全边界，语义不变）。
- 时间统一 UTC；内容指纹 `md5` 为小写 hex。

---

## 一、agent 侧 `/beacon/v1/agent/*`

### 1. 注册 `POST /beacon/v1/agent/register`

请求（**只报 serverId；capacity/weight 为顶层一等字段；metadata 仅自定义 `map<string,string>`；无 canary**）：
```json
{
  "namespace": "prod",
  "serverId": "lobby-1",
  "role": "bukkit",
  "groupHint": "area1",
  "address": "10.0.0.7:25565",
  "version": "1.4.2",
  "capacity": 200,
  "weight": 100,
  "metadata": { "region": "cn-east" }
}
```
- `backends`（可选，`string[]`）：**仅 bc（`role=bungee`）上报**本代理当前代理的后端子服 serverId 集合，控制面存为只读事实供拓扑 bc→bukkit 连线消费（FR-36，[ADR-0024](adr/0024-bc-backend-membership-as-fact.md)）。bukkit / 旧 agent 不发即缺键，向后兼容；只存内存、随注册/上报刷新、不落 DB。
响应：
```json
{
  "instanceKey": "prod/lobby-1",
  "resolvedGroup": "area1",
  "resolvedZone": "zoneA",
  "heartbeatIntervalSec": 10,
  "ttlSec": 30,
  "assigned": true
}
```
- `resolvedZone` 未分配时为 `null`，`assigned=false`（实例仍可注册运行，有效配置只含 global/group 层，管理台高亮待指派）。
- **重复 serverId 守卫**：同 `(namespace, serverId)` 已有**仍新鲜**（`lastHeartbeat` 在心跳周期内）的另一 address 在线实例 → `409 DUPLICATE_SERVER_ID` + 写 fail 审计。旧条目已超期视为僵尸 → 允许新 address 顶替并告警（故障换机不被误杀）。同 address 重连幂等覆盖。
- 身份缺失（serverId/namespace 空）→ `400 IDENTITY_REQUIRED`。

### 2. 心跳 `POST /beacon/v1/agent/heartbeat`
请求：`{ "namespace": "prod", "serverId": "lobby-1" }`
响应：`{ "ok": true, "ttlSec": 30, "configDirty": false }`
- 刷新内存 `lastHeartbeat=now`、状态 `online`；`configDirty` 为可选优化提示位，**P1 恒 `false`**（变更感知由长轮询负责，agent 不依赖它；提示位归档 P2）。未注册 → `404 NOT_REGISTERED`。

### 2.5 单条推送流 `GET /beacon/v1/agent/stream`（SSE，FR-24）

server→agent 单向推送流，**合并配置/文件树/覆盖集三条长轮询为一条**（见 [ADR-0015](adr/0015-sse-server-push-transport.md)，取代 [ADR-0006](adr/0006-rest-long-poll-push.md)）。`Content-Type: text/event-stream`，连接 held-open。

查询参数：`?namespace=&serverId=&configMd5=<本地配置md5>&fileMd5=<本地fileTreeMd5>&overrideMd5=<本地overrideMd5>&topologyMd5=<本地拓扑摘要>`（无该通道内容时传空串；agent 不本地维护拓扑摘要，`topologyMd5` 恒传空让控制面补一次，FR-29）。未注册 → `404 NOT_REGISTERED`。

行为：
- **连接即对账**：建连时按上报的各通道 md5 与服务端当前 md5 比对，对**落后通道**立即补发 `*-changed` 事件（补齐断线期间落下的增量），再发一条 `ready` 标记，随后转入直播。
- **直播**：配置/文件/覆盖集发布后，按 scope 算最小受影响 serverId 集合（复用长轮询唤醒集合），仅向受影响连接推 `*-changed`；未受影响连接不推。
- **只发变更通知、不搬数据**：事件载荷仅含新 md5，agent 收到后**用现有 HTTP 端点（§3/§6/§8）取内容并应用**。
- **保活**：无变更时按间隔发 SSE 注释行（`: ping`）维持连接、穿透反代空闲超时；agent 解析时跳过。

事件帧（SSE 标准：`event:` 行 + `data:` 行 + 空行）：
```
event: config-changed
data: {"md5":"ab12...ef"}

event: ready
data: {}
```
事件类型：`config-changed` / `file-changed` / `override-changed`（各携带对应通道新 md5）、`topology-changed`（[FR-29](PRD.md)：namespace 内实例上线/下线/改派 zone 时推送，`data` 携带新拓扑摘要——**通知式、不含实例数据**，agent 收到后重查 §5 发现端点取最新拓扑）、`ready`（首轮对账完成）。预留 `command-pending` 供后续远程运维命令复用本流。

> **健康判活与流活性解耦**：online/lost/offline（[FR-5](PRD.md)）仍由独立心跳 + TTL 判定，**不**用「SSE 断开」判失联。流断时 agent 按本地快照继续（fail-static），带退避重连、重连即对账。
> **反代/Docker**：经 nginx 等反代须关闭响应缓冲（响应头已带 `X-Accel-Buffering: no`）、调长读超时，见 [OPERATIONS](OPERATIONS.md)。

### 3. 拉有效配置 `GET /beacon/v1/agent/config/effective`
> 自 FR-24 起变更感知由 §2.5 SSE 流负责；本端点退化为「按 md5 取内容」：md5 不同立即 200、相同挂起到超时 304。SSE 收到 `config-changed` 后 agent 调本端点取内容。长轮询挂起语义保留（迁移期兼容，未注入流传输时仍可单独续杯）。

查询参数：`?namespace=&serverId=&md5=<当前md5>&timeoutMs=30000`（首拉 `md5` 传空/0）。
返回时机三选一：① 当前 md5 ≠ 请求 md5 → 立即 200；② 挂起期间被唤醒且重算后变化 → 200；③ 到超时无变化 → `304`（空体），agent 即续杯。
200 响应：
```json
{
  "namespace": "prod", "serverId": "lobby-1",
  "group": "area1", "zone": "zoneA",
  "md5": "ab12...ef",
  "items": [
    { "dataId": "mysql.yml",        "format": "yaml", "md5": "9f...c1", "content": "url: jdbc:...\npool: 20\n" },
    { "dataId": "merge-zones.json", "format": "json", "md5": "77...0a", "content": "{\"area1\":[\"zoneA\",\"zoneB\"]}" }
  ]
}
```
- `items` 为**已按覆盖链合并后的有效配置文本**，agent 直接使用。zone 未分配时只含 global/group 合并结果。未注册 → `404 NOT_REGISTERED`。

### 4. 上报状态 `POST /beacon/v1/agent/report`
请求：`{ "namespace", "serverId", "appliedMd5", "playerCount", "tps", "memUsed", "memMax", "cpuLoad" }`（负载数字 **仅展示，不参与任何决策**；`memUsed/memMax/cpuLoad` 为附加字段，旧 agent 缺键 → 内存缺省 0、`cpuLoad` 缺省 -1.0 不可用，FR-32）。响应：`{ "ok": true }`。
- `backends`（可选，`string[]`）：**仅 bc 上报**本代理当前代理的后端子服 serverId 集合，随上报刷新控制面内存事实（FR-36，[ADR-0024](adr/0024-bc-backend-membership-as-fact.md)）。**缺键与显式空集语义不同**：bukkit / 旧 agent 缺键 → 控制面保留原集合不动；bc 显式上报（含空集即清空）才刷新。向后兼容。
- `proxy`（可选，对象）：**仅 bc（`role=bungee`）上报**的代理专属负载指标（FR-34，[ADR-0025](adr/0025-bc-proxy-metrics-and-netty-traffic.md)），仅展示不参与决策。子对象字段：`onlineConnections`（代理在线连接数）、`threadCount`（JVM 线程数）、`uptimeMs`（JVM 运行毫秒数）、`backendUp`/`backendTotal`（后端子服可达/总数）、`backendAvgLatencyMs`（到可达后端的平均 ping 延迟毫秒，`-1` 表示无可达后端不可用）。**缺键不刷新**：bukkit / 旧 agent 不发即缺键，控制面保留实例原 BC 字段不动；bc 上报才刷新。向后兼容。网络吞吐入/出字节本期不采（BungeeCord 无干净 Netty 注入点，见 ADR-0025）。

### 5. 服务发现 `GET /beacon/v1/agent/discovery`
查询：`?namespace=&group=&zone=&role=`，外加可选的自定义元数据过滤 `&tag.<key>=<value>`（可重复，多 tag 取交集；按实例 `metadata` 键值精确匹配，FR-29）。返回按条件过滤的**可用**实例列表（`online`+`degraded`，归 agent 前缀 + agent token）。无匹配返回 `{ "instances": [] }`。BeaconAgentProxy 用它周期同步同 namespace 下 `role=bukkit` 的可用子服，按 `serverId` 注入 Bungee `ServerInfo` 目录（仅管理 Beacon 创建的条目，同名手工配置不覆盖；FR-4 延伸出口）。

> **拓扑 watch（FR-29）**：要实时感知拓扑变化，不必轮询本端点——订阅 §2.5 SSE 流的 `topology-changed` 事件即可（实例上线/下线/改派时即时通知），收到后重查本端点取最新结果。SDK 经 `discovery().watch(listener)` 暴露（未启用推送流时句柄不可用、回退周期 `query`）。

> **玩家位置名册只读查询（FR-31，仅 SDK 门面，无新增 HTTP 端点）**：业务插件经 `discovery().roster()` 取全量名册 `Map<玩家名, serverId>`（单一全局名册 `beacon:player-loc`，不按 namespace 分区，单 BC 前提下即全量），`discovery().rosterInZone(group, zone)` 取某 zone 过滤后名册（zone 集 ∩ 名册，zone 权威来自控制面发现结果）。数据源为 FR-26 的 agent 侧 Redis 名册（`beacon:player-loc`），**控制面不参与、无名册端点**；Redis 不可用 / 模块未开 / 名册空时返回空 Map。仅暴露名册事实，「看人」业务归③层业务插件（见 [ADR-0022](adr/0022-agent-roster-read-api.md)）。

### 6. 长轮询拉文件清单 `GET /beacon/v1/agent/files/manifest`（通道B）
查询参数：`?namespace=&serverId=&md5=<当前fileTreeMd5>&timeoutMs=30000`（首拉 `md5` 传空）。
返回时机同 §3：① 当前 `fileTreeMd5` ≠ 请求 md5 → 立即 200；② 挂起期间被唤醒且重算后变化 → 200；③ 到超时无变化 → `304`（空体）。**与配置长轮询唤醒集合独立**（见 ADR-0010）。
200 响应（仅 `manifest`，**不含内容**）：
```json
{
  "namespace": "prod", "serverId": "lobby-1",
  "group": "area1", "zone": "zoneA",
  "fileTreeMd5": "c4...9b",
  "files": [
    { "path": "ui-components/main.allin", "md5": "9f...c1" },
    { "path": "scripts/hello.js",          "md5": "77...0a" }
  ]
}
```
- `files` 为**已按覆盖链整文件覆盖后的有效文件清单**（path→md5），agent 比对本地已落盘 manifest，仅取/删变更文件。未注册 → `404 NOT_REGISTERED`。

### 7. 取单个文件内容 `GET /beacon/v1/agent/files/content`（通道B）
查询：`?namespace=&serverId=&path=<相对路径>`。返回该 `path` 按覆盖链解析后的**整文件内容**：
```json
{ "path": "ui-components/main.allin", "md5": "9f...c1", "content": "...整文件文本..." }
```
- 该 `path` 不在有效文件树 → `404 FILE_NOT_FOUND`。未注册 → `404 NOT_REGISTERED`。

### 8. 长轮询拉适用覆盖集 `GET /beacon/v1/agent/override-sets`（FR-15 投递）
查询参数：`?namespace=&serverId=&md5=<当前overrideMd5>&timeoutMs=30000`（首拉 `md5` 传空）。
返回时机同 §3/§6：① 当前 `overrideMd5` ≠ 请求 md5 → 立即 200；② 挂起期间被唤醒且重算后变化 → 200；③ 到超时无变化 → `304`（空体）。与文件长轮询**复用同一唤醒集合**（同属通道B，覆盖集发布按 scope 唤醒受影响 serverId），但 `overrideMd5` 与配置 md5、`fileTreeMd5` **相互独立**（见 [ADR-0011](adr/0011-third-party-file-override-and-restricted-reload-command.md)）。`overrideMd5` 指纹覆盖目标根 / 重载命令 / 成员 path 清单 **+ 各成员内容指纹**（复用 `file_object.content_md5`、按字节算，ADR-0011 决策 9）——故成员文件「内容只改、path 不变」也会改变 `overrideMd5`、触发 agent 重取落盘（成员内容经 §9 取）；该成员内容指纹**仅参与 md5 计算、不在本端点响应体下发**。
200 响应（仅"目标根 + 受限重载命令 + 成员 path"，**不含成员内容**）：
```json
{
  "namespace": "prod", "serverId": "lobby-1",
  "group": "area1", "zone": "zoneA",
  "overrideMd5": "ab...ef",
  "sets": [
    { "name": "AllinCore", "targetRoot": "plugins/AllinCore", "reloadCommand": "allin reload",
      "members": ["config.yml", "scripts/hello.js"] }
  ]
}
```
- `sets` 为按覆盖链解析后**适用本 server** 的覆盖集（同名取覆盖链最高层那份）；`targetRoot` 限定 `plugins/<plugin>/` 内（agent 再做最终校验，控制面被攻破兜底）；`reloadCommand` 可为 `""`（不下发命令），是否真正派发由 **agent 本地白名单**把关（控制面不下发白名单，默认空即不派发）。成员内容走 §9 取。未注册 → `404 NOT_REGISTERED`。

### 9. 取覆盖集成员内容 `GET /beacon/v1/agent/override-sets/content`（FR-15 投递）
查询：`?namespace=&serverId=&set=<覆盖集名>&path=<相对目标根的成员路径>`。返回该 `(set, path)` 按覆盖链解析后的**整文件内容**：
```json
{ "set": "AllinCore", "path": "config.yml", "md5": "9f...c1", "content": "...整文件文本..." }
```
- agent 据 §8 清单逐个取成员内容，经 `OverrideApplier`（备份 → 路径安全 → 原子覆盖 → 受管标记）落到该集 `targetRoot`，全量成功且命中本地白名单才派发 `reloadCommand`。该 `set` 不适用本 server / 该成员不存在 → `404 FILE_NOT_FOUND`。未注册 → `404 NOT_REGISTERED`。

---

## 二、admin / UI 侧 `/admin/v1/*`

### 管理面鉴权（操作者认证 + 写操作授权）

> 单操作者模型（非 RBAC）。凭据来源走配置/环境变量（`BEACON_ADMIN_USERNAME` / `BEACON_ADMIN_PASSWORD` / `BEACON_AUTH_SECRET`），禁硬编码。令牌为无状态 HMAC-SHA256 签名串，无需落库/Redis。

| 端点 | 说明 |
|---|---|
| `POST /admin/v1/auth/login` | 登录：`{ username, password }` → `{ token, operator }`，并记一条 `auth.login` 审计。**本端点自身不需令牌。** |
| `POST /admin/v1/auth/logout` | 登出：仅记一条 `auth.logout` 审计 → `204`。令牌为无状态 HMAC、服务端无会话可吊销，前端清本地令牌即登出；本端点需令牌（取认证身份入审计）。 |

- 除登录外，`/admin/v1/*` 一律需请求头 `Authorization: Bearer <token>`。缺失或非 `Bearer ` 前缀 → `401 ADMIN_UNAUTHORIZED`；令牌签名不符 / 结构非法 / 过期 → 同样 `401 ADMIN_UNAUTHORIZED`。
- 登录凭据错误 → `401 BAD_CREDENTIALS`。
- **操作者身份以认证态为准**：所有写操作（新建/发布/回滚/软删/改派/取消指派/手动下线/建环境）的 `operator` 由登录令牌派生写入 `audit_log`，**忽略请求体/查询里手填的 operator**（手填值不再生效）。
- 登录 / 登出审计的 `detail` 仅记操作者，**严禁含口令 / 令牌等敏感数据**。
- 令牌有效期由配置 `auth.token-ttl-sec` 决定（默认 86400 秒）；过期需重新登录。

### 配置管理
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/configs?namespace=&group=&dataId=&scopeLevel=` | 列出配置项 |
| `GET /admin/v1/configs/{id}` | 取当前内容 + 元数据 |
| `POST /admin/v1/configs` | 新建（首次发布）：三元组 + scopeLevel/scopeTarget + format + content + comment + 可选 `sensitive`（默认 false；true 则 content 加密入库，FR-20，见 [ADR-0018](adr/0018-config-encryption-at-rest.md)）（operator 由认证态派生） |
| `PUT /admin/v1/configs/{id}` | 发布新版本：content + comment → version+1，返回新 `version`/`md5`（operator 由认证态派生） |
| `DELETE /admin/v1/configs/{id}` | 软删（该层从合并链脱落，触发唤醒；operator 由认证态派生） |
| `GET /admin/v1/configs/{id}/revisions` | 历史版本列表 |
| `GET /admin/v1/configs/{id}/revisions/{version}` | 取某历史版本内容 |
| `POST /admin/v1/configs/{id}/rollback` | 回滚：`{ toVersion, comment }`（= 读旧版内容作新版发布；operator 由认证态派生） |
| `GET /admin/v1/configs/{id}/diff?from=&to=` | 返回两版本文本供前端 diff |
| `GET /admin/v1/configs/effective?namespace=&serverId=&group=&zone=` | 只读预览某目标合并后的有效配置 + 逐键来源（FR-22，见 [ADR-0013](adr/0013-admin-effective-config-preview-and-provenance.md)） |
| `POST /admin/v1/configs/{id}/gray` | 发布灰度：`{ content, cohort: [serverId...], comment }` → cohort 内 server 解析到灰度内容、名单外仍解析稳定版（FR-9，见 [ADR-0021](adr/0021-config-gray-cohort-version-selection.md)；operator 由认证态派生） |
| `POST /admin/v1/configs/{id}/gray/promote` | 晋升灰度为全量稳定版：`{ comment }` → 灰度内容作为新版本发布（version+1）并清空灰度，返回新 `version`/`md5`（operator 由认证态派生） |
| `DELETE /admin/v1/configs/{id}/gray?comment=` | 中止灰度：丢弃灰度，cohort 成员回到稳定版本，稳定指针不动（operator 由认证态派生） |
| `GET /admin/v1/configs/gray?namespace=` | 列出某环境当前活跃灰度（`configItemId`/`md5`/`cohort`/`sensitive` 等，不回吐 content） |

**配置灰度 / Beta（FR-9，见 [ADR-0021](adr/0021-config-gray-cohort-version-selection.md)）**：灰度作用在"某 dataId 用哪个版本内容"的**版本选择**层，与 scope 覆盖链正交叠加（不新增覆盖层）。发布灰度需给 `content` + **非空** `cohort`（显式 serverId 名单，去重 / 去空白）；灰度内容同样过发布前 schema 校验（FR-27），灰度项 `sensitive` 与所属配置项镜像（敏感则灰度 content 加密落库，FR-20）。cohort 内 `serverId` 解析到灰度内容，**名单外解析结果与无灰度时逐字节相同**。`promote` 把灰度内容晋升为全量稳定版（走既有发布路径、进版本历史、可回滚）；`abort` 丢弃灰度。两操作提交后只唤醒受影响 `serverId`（发布 / abort 唤醒 cohort、promote 唤醒该配置项 scope ∪ cohort）。一个配置项**至多一个活跃灰度**（重复发布即覆盖）。错误：无活跃灰度时 promote/abort 返 `404 GRAY_NOT_FOUND`；空 cohort 返 `400 EMPTY_COHORT`；灰度内容非法同发布（`422 CONTENT_INVALID` / `422 CONTENT_SCHEMA_INVALID`）。

`GET /admin/v1/configs/effective`：只读预览某目标按覆盖链合并后的有效配置，与 agent 端 `/beacon/v1/agent/config/effective` 同源、内容与 `md5` 一致，但**不挂长轮询、不强制注册**，可预览未注册/假定指派的目标。参数：`namespace` 必填；`serverId` 与 `group` 至少给一个（给 `serverId` 时按 `zone_assignment` 解出 group/zone，未指派则用传入的 `group`/`zone`）。返回：

```json
{
  "namespace": "prod", "serverId": "srv-031", "group": "cn-east", "zone": "z3",
  "md5": "ab12cd34...",
  "items": [
    {
      "dataId": "config.yml", "format": "yaml", "md5": "...", "content": "已合并的有效配置文本",
      "sources": [ { "path": ["server","max-players"], "scope": "global" }, { "path": ["view-distance"], "scope": "server" } ],
      "deletions": [ { "path": ["whitelist"], "scope": "server" } ]
    }
  ]
}
```

- `sources`：每个叶子键的最终来源覆盖层（`global`/`group`/`zone`/`server`），`path` 为嵌套键路径（properties 扁平键即单段、可能含 `.`）。
- `deletions`：被某层写 `null` 减量删除、且最终确实不存在的键。
- 来源由服务端权威计算（平行纯函数，不改 agent 合并热路径），前端直接用，避免前后端两份合并实现漂移。

**敏感配置 at-rest 加密（FR-20，见 [ADR-0018](adr/0018-config-encryption-at-rest.md)）**：新建配置项传 `sensitive: true` 时，其 `content` 以 AES-256-GCM 加密落库（DB 列存 `enc:v1:` 前缀的 base64 密文），密钥仅从环境变量 `BEACON_CONFIG_ENCRYPTION_KEY`（base64 的 32 字节）读取。控制面在**读取详情 / 历史版本 / 有效配置解析与下发**时自动解密——agent 拿到的是**明文**（数据面内网可信不变，agent 不持密钥）。配置项视图回吐 `sensitive` 布尔标记，但**永不回吐密钥或密文**。库中已有敏感项却未配置密钥 → 控制面 **fail-fast 拒绝启动**。md5 / 有效配置解析始终基于解密后明文，与非敏感项行为一致。

错误：配置不存在 `404 CONFIG_NOT_FOUND`；回滚目标不存在 `404 REVISION_NOT_FOUND`；同标识重复建 `409 CONFIG_CONFLICT`；内容超长（> 256KB）`422 CONTENT_TOO_LARGE`；发布内容解析失败 `422 CONTENT_INVALID`；发布内容结构/类型/必填项校验不通过（顶层非键值映射、含空键等）`422 CONTENT_SCHEMA_INVALID`；覆盖层/目标键不合法 `400 INVALID_SCOPE`；同一 dataId 跨层格式不一致 `422 FORMAT_INCONSISTENT`。

### 文件树托管（通道B）
整文件 blob，scope **整文件覆盖**（不深合并），版本/回滚同配置思路（见 [ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)）。
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/files?namespace=&group=&path=&scopeLevel=` | 列出文件对象 |
| `GET /admin/v1/files/{id}` | 取当前整文件内容 + 元数据 |
| `POST /admin/v1/files` | 新建（首次发布）：`{ namespace, group, path, scopeLevel, scopeTarget, content, comment }`（operator 由认证态派生） |
| `POST /admin/v1/files/import` | 配置导入（FR-38，`multipart/form-data`）：把一份目录批量上传到某组（`scope=group`）。字段 `namespace`、`group`、可选 `comment` + 多个 `files` 文件部件 + 与之等长一一对应的 `paths` 相对路径字段。每个文件按相对 path「存在则发布新版本、不存在则首发」（整文件覆盖语义），多文件在同一事务内原子落地，提交后唤醒文件长轮询，并记一条 `file.import` 审计。返回 `{ files, created, updated }`（operator 由认证态派生） |
| `PUT /admin/v1/files/{id}` | 发布新版本：`{ content, comment }` → version+1，返回新 `version`/`md5`（operator 由认证态派生） |
| `DELETE /admin/v1/files/{id}` | 软删（该层从覆盖链脱落，触发文件唤醒；下游 agent 据 manifest 删该 path 镜像；operator 由认证态派生） |
| `GET /admin/v1/files/{id}/revisions` | 历史版本列表 |
| `GET /admin/v1/files/{id}/revisions/{version}` | 取某历史版本内容 |
| `POST /admin/v1/files/{id}/rollback` | 回滚：`{ toVersion, comment }`（operator 由认证态派生） |

错误：文件不存在 `404 FILE_NOT_FOUND`；回滚目标不存在 `404 REVISION_NOT_FOUND`；同标识重复建 `409 FILE_CONFLICT`；路径不合法（空 / 绝对路径 / 含 `..` 穿越 / 含反斜杠）`400 INVALID_PATH`；内容超长（> 1MB）`422 CONTENT_TOO_LARGE`；覆盖层/目标键不合法 `400 INVALID_SCOPE`。导入（`/files/import`）另有：缺 `namespace`/`group`/文件，或 `paths` 与 `files` 数量不一致 `400 INVALID_PARAM`；目标组非法（如填全局组）`400 INVALID_SCOPE`；单次文件数超上限 `422 TOO_MANY_FILES`；单文件或累计总字节超上限 `422 CONTENT_TOO_LARGE`。

### 实例与健康
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/instances?namespace=&group=&zone=&role=&status=` | 按标签过滤（读内存注册表）；`status` 可取 `online`/`degraded`/`lost`/`offline`。实例视图含 `backends`（`string[]`，仅 bc 非空——本代理当前代理的后端子服 serverId 集合，bukkit 恒空；供拓扑连线消费，FR-36） |
| `GET /admin/v1/instances/{serverId}?namespace=` | 单实例详情（同含 `backends`） |
| `POST /admin/v1/instances/{serverId}/offline?namespace=` | 手动下线（移除内存条目；operator 由认证态派生） |
| `GET /admin/v1/alerts` | 健康告警站内信：最近告警列表（最新在前），`{ items: [{ namespace, serverId, address, prevStatus, status, at }] }`（FR-28，进程内、控制面重启清零） |

错误：实例不存在 `404 INSTANCE_NOT_FOUND`。

### 集群拓扑（FR-37）
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/topology?namespace=` | 读内存注册表快照，返回该 namespace 的集群拓扑（bc→bukkit 真实连线）。`namespace` 必填，缺失返 `400 INVALID_PARAM` |

仅纳入**可用集合**（`online`+`degraded`，与发现 / 拓扑摘要同口径）。返回体：

```json
{
  "namespace": "prod",
  "nodes": [
    { "serverId": "bc-1", "role": "bungee", "group": "area1", "zone": null, "status": "online", "address": "10.0.0.1:25577" },
    { "serverId": "lobby-1", "role": "bukkit", "group": "area1", "zone": "z1", "status": "online", "address": "10.0.0.2:25565" }
  ],
  "edges": [
    { "source": "bc-1", "target": "lobby-1" }
  ],
  "groups": [
    { "group": "area1", "zone": null, "members": ["bc-1"] },
    { "group": "area1", "zone": "z1", "members": ["lobby-1"] }
  ]
}
```

- `nodes`：各在线实例（`serverId`/`role`/`group`/`zone`/`status`/`address`；未分配 zone 时 `zone=null`）。
- `edges`：bc→bukkit 连线，由 bc 的 `backends` 事实（FR-36，[ADR-0024](adr/0024-bc-backend-membership-as-fact.md)）生成；**只连当前在册可用的后端**，已离线后端不画悬挂边。
- `groups`：按 `(group, zone)` 聚合的 serverId 分组，供前端分簇展示。
- 各列表按 serverId / (group,zone) 字典序稳定排序；空拓扑返回空数组（非 null）。
- 控制面**只展示该事实、不据它做任何调度 / 连接决策**（守「只存事实」边界）；只读、不落 DB、不挂长轮询。前端 `/topology` 页轮询刷新（要实时可订阅 agent 侧 SSE 流 `topology-changed`，FR-29）。

### zone 分配
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/zones/assignments?namespace=&group=&zone=` | 列出 serverId→zone 指派 |
| `PUT /admin/v1/zones/assignments` | 新增/改派 upsert：`{ namespace, serverId, group, zone, note }`，触发该 serverId 唤醒（operator 由认证态派生） |
| `DELETE /admin/v1/zones/assignments?namespace=&serverId=` | 取消指派（软删），触发唤醒（operator 由认证态派生） |
| `GET /admin/v1/zones?namespace=&group=` | zone 维度汇总（每 zone 服数/在线数） |

错误：指派不存在 `404 ASSIGNMENT_NOT_FOUND`。改派的长轮询唤醒在 M3 长轮询热更落地（M2 已即时重算有效配置、刷新内存归属）。

### 流量调度（FR-10）

控制面**只给调度决策（query-only），不执行玩家连接**：落位建议是基于权威事实（在线状态 / 容量 / 权重）的推荐，drain 是排空 / 维护标记；真正把玩家送到目标服由数据面执行（见 [ADR-0017](adr/0017-traffic-scheduling-decision-vs-execution.md)）。本期只做落位均衡 + drain，**不做 canary 引流**。

| 端点 | 说明 |
|---|---|
| `GET /admin/v1/scheduling/placement?namespace=&group=&zone=` | 某 zone 内落位候选（按推荐优先级排序）。仅纳入 `online` 且未 drain 的实例，按 weight 降序 → capacity 降序 → serverId 升序。`namespace`/`zone` 必填，`group` 可选 |
| `GET /admin/v1/scheduling/drains?namespace=` | 列出当前 drain 标记 |
| `PUT /admin/v1/scheduling/drains` | 标记 drain：`{ namespace, serverId, reason }`（幂等；operator 由认证态派生） |
| `DELETE /admin/v1/scheduling/drains?namespace=&serverId=` | 取消 drain（软删；operator 由认证态派生） |

`placement` 返回 `{ "candidates": [ { "serverId", "address", "weight", "capacity", "drained" }, ... ] }`；zone 内无可用候选（空集 / 全部 drain / 全部离线）返回空 `candidates`（`200`，不报错），由数据面兜底。落位**不读** agent 上报的 `playerCount`/`tps`（二者仅展示、不参与决策）。错误：参数缺失 `400 INVALID_PARAM`；取消不存在的 drain `404 DRAIN_NOT_FOUND`。

### 指标看板（FR-32）

控制面自带的可观测看板（负载指标 + 历史趋势），与外部抓取的 `/metrics`（FR-30）并存不冲突——前者是 Beacon 内自带可视化、采样持久化到 MySQL，后者供外部监控系统 pull（见 [ADR-0023](adr/0023-control-plane-observability-dashboard.md)）。**只展示负载指标（健康事实），不含玩家名单 / 身份**（看人归③层业务插件）。

| 端点 | 说明 |
|---|---|
| `GET /admin/v1/metrics/summary?namespace=` | 当前快照聚合统计（从内存注册表实时计算，不读库）。`namespace` 可选，空=聚合全部环境 |
| `GET /admin/v1/metrics/trend?namespace=&serverId=&window=&from=&to=` | 历史时序趋势（查 `metric_sample` 表）。`namespace` 可选，空=聚合全部环境；`serverId` 可选过滤；`window` 取预设窗口 `1h`/`6h`/`24h`，或用 `from`/`to`（RFC3339）自定义时间窗；聚合粒度由服务端按窗长自动降采样（约 120 点），无需传步长 |

`GET /admin/v1/metrics/summary`：返回全集群总玩家数、在线服务器数、每服在线人数列表（仅 `serverId` + `playerCount`）、全集群平均 TPS·内存·CPU，数据源为内存注册表（与发现 / 健康同源、实时计算）。内存为**字节**（`int64`）；`avgCpuLoad` 为 [0,1]，`-1` 表示无可用 CPU 样本，`cpuSampleCount` 为参与平均的可用样本数。**平均 TPS 与平均 CPU 仅统计 `role=bukkit` 子服**（bungee 作纯代理 tps 恒为 0，计入会拉低平均失真）；总玩家数 / 在线服数 / 平均内存仍计全部在线实例。`bc` 子对象为 **bc（bungee 代理）维度聚合（仅 `role=bungee` 实例统计，FR-34，[ADR-0025](adr/0025-bc-proxy-metrics-and-netty-traffic.md)）**：`proxyCount`（在线 bc 代理数）、`totalConnections`（bc 连接合计）、`avgThreadCount`（bc 平均 JVM 线程数）、`backendUp`/`backendTotal`（bc 可达/总后端合计）、`avgBackendLatencyMs`（bc 平均后端延迟毫秒，`-1` 表示无可用样本）；无 bc 实例时各计数为 0、平均延迟为 `-1`。**仅负载数字，不含玩家名单 / 身份**。返回：
```json
{
  "totalPlayers": 312,
  "onlineServers": 6,
  "servers": [
    { "serverId": "lobby-1", "playerCount": 84 },
    { "serverId": "pvp-2",   "playerCount": 56 }
  ],
  "avgTps": 19.6,
  "avgMemUsed": 1929379840,
  "avgMemMax": 4294967296,
  "avgCpuLoad": 0.42,
  "cpuSampleCount": 6,
  "bc": {
    "proxyCount": 2,
    "totalConnections": 312,
    "avgThreadCount": 56,
    "backendUp": 6,
    "backendTotal": 6,
    "avgBackendLatencyMs": 9.0
  }
}
```

`GET /admin/v1/metrics/trend`：按时间窗查询 `metric_sample`，服务端按窗长自动降采样，返回**时间序列点**（`sampledAt` + 各指标聚合值），用于 Dashboard 趋势图（近 1h / 6h / 24h）。内存为**字节**，`avgCpuLoad` 为 [0,1]（`-1` 表示该点无可用 CPU 样本）。**仅聚合数字，不含玩家名单 / 身份**。返回：
```json
{
  "points": [
    { "sampledAt": "2026-06-20T08:00:00Z", "totalPlayers": 80, "avgTps": 19.8, "avgMemUsed": 1572864000, "avgMemMax": 4294967296, "avgCpuLoad": 0.30 },
    { "sampledAt": "2026-06-20T08:05:00Z", "totalPlayers": 84, "avgTps": 19.9, "avgMemUsed": 1593835520, "avgMemMax": 4294967296, "avgCpuLoad": 0.31 }
  ]
}
```
- 不传 `namespace` 时聚合**全部环境**的趋势；不传 `serverId` 时返回该范围的聚合趋势（服务端按窗长自动降采样汇总在线实例）；传 `serverId` 时返回单服趋势。
- 各点的**平均 TPS / 平均 CPU 仅统计 `role=bukkit`**（与 summary 一致，bungee 不进这两个平均的分母）；总玩家数 / 平均内存仍按全部样本聚合。
- 保留期外的样本已被采样器滚动清理，超出保留期的时间窗只返回现存样本（见 [ADR-0023](adr/0023-control-plane-observability-dashboard.md) 与 ARCHITECTURE §7.1）。
- 错误：时间窗非法 `400 INVALID_PARAM`。

### 控制面自身状态（FR-33）

控制面**进程自身**的健康快照，供管理台顶部页眉实时展示——区别于上面「指标看板（FR-32）」的 agent 网络聚合指标（那是被管服的负载，这里是 Beacon 自己的健康）。

| 端点 | 说明 |
|---|---|
| `GET /admin/v1/system/status` | 控制面自身状态：版本 / 运行时长 / DB 连通 / 在线实例数 / 采样器状态 + Go 运行时资源 |

`GET /admin/v1/system/status`：实时采集一次。`startedAt` 为进程启动时间（UTC），`uptimeSeconds` 为运行时长（秒）；`db.connected` 经底层连接池 `Ping` 探测，断开时 `db.connected=false` 且带 `db.error`（端点仍返回 `200`，以反映状态而非报错）；`onlineInstances` 取自内存注册表的在线实例数；`samplerEnabled` 为负载指标采样器（FR-32）是否启用；`runtime` 为 Go 运行时资源，`heapAlloc`/`heapSys` 为**字节**。进程 CPU% 经 [gopsutil](https://github.com/shirou/gopsutil) 采集本进程占用：`cpuAvailable=true` 时 `cpuPercent` 为自上次采集以来的占比（[0,100]，钳顶后保留 1 位小数；该占比不按核心数归一，多核满载会先到 100 再被钳）；采集失败时优雅降级为 `cpuAvailable=false`（`cpuPercent` 此时无意义、恒 `0`）。返回：
```json
{
  "version": "v0.5.0",
  "startedAt": "2026-06-20T08:00:00Z",
  "uptimeSeconds": 12345,
  "db": { "connected": true },
  "onlineInstances": 6,
  "samplerEnabled": true,
  "runtime": { "goroutines": 42, "heapAlloc": 134217728, "heapSys": 268435456 },
  "cpuAvailable": true,
  "cpuPercent": 23.4
}
```

### 审计与环境
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/audits?namespace=&operator=&action=&targetType=&targetRef=&from=&to=&page=&size=` | 分页审计（时间倒序），返回 `total` + `items`；`operator` 按操作者过滤（FR-30） |
| `GET /admin/v1/namespaces` / `POST /admin/v1/namespaces` | 环境列表 / 新建（建环境记一条 `namespace.create` 审计，operator 由认证态派生） |

### 运维指标
| 端点 | 说明 |
|---|---|
| `GET /metrics` | Prometheus 文本格式运行指标，免管理台鉴权（内网信任面，见 [ADR-0020](adr/0020-prometheus-metrics-observability.md)） |

暴露指标：`beacon_instances_registered{namespace,role}`（注册实例数，gauge）、`beacon_instances_status{status}`（健康状态分布 online/degraded/lost/offline，gauge）、`beacon_config_publish_total`（配置发布累计，counter）、`beacon_push_notify_total`（推送/长轮询唤醒累计，counter）。注册/健康为 pull 模型，抓取时读内存注册表实时计数。

---

## 三、字段对齐速查

| 概念 | 字段 | 类型 | 取值 |
|---|---|---|---|
| 环境 | `namespace` | string | prod / test |
| 大区 | `group` | string | 业务大区；global 层占位 `__GLOBAL__` |
| 小区 | `zone` | string | zone 编码 |
| 子服身份 | `serverId` | string | agent 上报，环境内唯一 |
| 覆盖层 | `scopeLevel` | string | global / group / zone / server |
| 配置名 | `dataId` | string | 如 mysql.yml |
| 格式 | `format` | string | yaml / properties / json |
| 指纹 | `md5` | string(32) | 小写 hex |
| 版本 | `version` | int64 | 单调递增，回滚也 +1 |
| 角色 | `role` | string | bukkit / bungee |
| 健康 | `status` | string | online / degraded / lost / offline |
