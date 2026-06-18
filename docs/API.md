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

查询参数：`?namespace=&serverId=&configMd5=<本地配置md5>&fileMd5=<本地fileTreeMd5>&overrideMd5=<本地overrideMd5>`（无该通道内容时传空串）。未注册 → `404 NOT_REGISTERED`。

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
事件类型：`config-changed` / `file-changed` / `override-changed`（各携带对应通道新 md5）、`ready`（首轮对账完成）。预留 `command-pending` / `topology-changed` 供后续 server→agent 特性（远程命令、[FR-29](PRD.md) watch）复用本流。

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
请求：`{ "namespace", "serverId", "appliedMd5", "playerCount", "tps" }`（`playerCount/tps` **仅展示，不参与任何决策**）。响应：`{ "ok": true }`。

### 5. 服务发现 `GET /beacon/v1/agent/discovery`
查询：`?namespace=&group=&zone=&role=`。返回按标签过滤的**在线**实例列表（归 agent 前缀 + agent token）。无匹配返回 `{ "instances": [] }`。BeaconAgentProxy 用它周期同步同 namespace 下 `role=bukkit` 的在线子服，按 `serverId` 注入 Bungee `ServerInfo` 目录（仅管理 Beacon 创建的条目，同名手工配置不覆盖；FR-4 延伸出口）。

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
| `POST /admin/v1/auth/login` | 登录：`{ username, password }` → `{ token, operator }`。**本端点自身不需令牌。** |

- 除登录外，`/admin/v1/*` 一律需请求头 `Authorization: Bearer <token>`。缺失或非 `Bearer ` 前缀 → `401 ADMIN_UNAUTHORIZED`；令牌签名不符 / 结构非法 / 过期 → 同样 `401 ADMIN_UNAUTHORIZED`。
- 登录凭据错误 → `401 BAD_CREDENTIALS`。
- **操作者身份以认证态为准**：所有写操作（新建/发布/回滚/软删/改派/取消指派/手动下线）的 `operator` 由登录令牌派生写入 `audit_log`，**忽略请求体/查询里手填的 operator**（手填值不再生效）。
- 令牌有效期由配置 `auth.token-ttl-sec` 决定（默认 86400 秒）；过期需重新登录。

### 配置管理
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/configs?namespace=&group=&dataId=&scopeLevel=` | 列出配置项 |
| `GET /admin/v1/configs/{id}` | 取当前内容 + 元数据 |
| `POST /admin/v1/configs` | 新建（首次发布）：三元组 + scopeLevel/scopeTarget + format + content + comment（operator 由认证态派生） |
| `PUT /admin/v1/configs/{id}` | 发布新版本：content + comment → version+1，返回新 `version`/`md5`（operator 由认证态派生） |
| `DELETE /admin/v1/configs/{id}` | 软删（该层从合并链脱落，触发唤醒；operator 由认证态派生） |
| `GET /admin/v1/configs/{id}/revisions` | 历史版本列表 |
| `GET /admin/v1/configs/{id}/revisions/{version}` | 取某历史版本内容 |
| `POST /admin/v1/configs/{id}/rollback` | 回滚：`{ toVersion, comment }`（= 读旧版内容作新版发布；operator 由认证态派生） |
| `GET /admin/v1/configs/{id}/diff?from=&to=` | 返回两版本文本供前端 diff |
| `GET /admin/v1/configs/effective?namespace=&serverId=&group=&zone=` | 只读预览某目标合并后的有效配置 + 逐键来源（FR-22，见 [ADR-0013](adr/0013-admin-effective-config-preview-and-provenance.md)） |

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

错误：配置不存在 `404 CONFIG_NOT_FOUND`；回滚目标不存在 `404 REVISION_NOT_FOUND`；同标识重复建 `409 CONFIG_CONFLICT`；内容超长（> 256KB）`422 CONTENT_TOO_LARGE`；发布内容解析失败 `422 CONTENT_INVALID`；发布内容结构/类型/必填项校验不通过（顶层非键值映射、含空键等）`422 CONTENT_SCHEMA_INVALID`；覆盖层/目标键不合法 `400 INVALID_SCOPE`；同一 dataId 跨层格式不一致 `422 FORMAT_INCONSISTENT`。

### 文件树托管（通道B）
整文件 blob，scope **整文件覆盖**（不深合并），版本/回滚同配置思路（见 [ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)）。
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/files?namespace=&group=&path=&scopeLevel=` | 列出文件对象 |
| `GET /admin/v1/files/{id}` | 取当前整文件内容 + 元数据 |
| `POST /admin/v1/files` | 新建（首次发布）：`{ namespace, group, path, scopeLevel, scopeTarget, content, comment }`（operator 由认证态派生） |
| `PUT /admin/v1/files/{id}` | 发布新版本：`{ content, comment }` → version+1，返回新 `version`/`md5`（operator 由认证态派生） |
| `DELETE /admin/v1/files/{id}` | 软删（该层从覆盖链脱落，触发文件唤醒；下游 agent 据 manifest 删该 path 镜像；operator 由认证态派生） |
| `GET /admin/v1/files/{id}/revisions` | 历史版本列表 |
| `GET /admin/v1/files/{id}/revisions/{version}` | 取某历史版本内容 |
| `POST /admin/v1/files/{id}/rollback` | 回滚：`{ toVersion, comment }`（operator 由认证态派生） |

错误：文件不存在 `404 FILE_NOT_FOUND`；回滚目标不存在 `404 REVISION_NOT_FOUND`；同标识重复建 `409 FILE_CONFLICT`；路径不合法（空 / 绝对路径 / 含 `..` 穿越 / 含反斜杠）`400 INVALID_PATH`；内容超长（> 1MB）`422 CONTENT_TOO_LARGE`；覆盖层/目标键不合法 `400 INVALID_SCOPE`。

### 实例与健康
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/instances?namespace=&group=&zone=&role=&status=` | 按标签过滤（读内存注册表）；`status` 可取 `online`/`degraded`/`lost`/`offline` |
| `GET /admin/v1/instances/{serverId}?namespace=` | 单实例详情 |
| `POST /admin/v1/instances/{serverId}/offline?namespace=` | 手动下线（移除内存条目；operator 由认证态派生） |
| `GET /admin/v1/alerts` | 健康告警站内信：最近告警列表（最新在前），`{ items: [{ namespace, serverId, address, prevStatus, status, at }] }`（FR-28，进程内、控制面重启清零） |

错误：实例不存在 `404 INSTANCE_NOT_FOUND`。

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

### 审计与环境
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/audits?namespace=&action=&targetType=&targetRef=&from=&to=&page=&size=` | 分页审计（时间倒序），返回 `total` + `items` |
| `GET /admin/v1/namespaces` / `POST /admin/v1/namespaces` | 环境列表 / 新建 |

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
