# Beacon REST 契约（第一期）

> 各端对齐的硬契约。Base：`/beacon/v1`（agent 侧）、`/admin/v1`（admin/UI 侧）。内容类型 `application/json; charset=utf-8`。

## 通用约定

- 统一错误体：`{ "code": "<业务码>", "message": "<中文说明>", "traceId": "<可选>" }`。
- HTTP 状态：400 参数错 / 401 缺 token / 404 不存在 / 409 冲突 / 422 校验失败 / 500 内部错；**304 仅用于长轮询无变更超时**。
- 鉴权：MVP 不做认证（鉴权属 P2）。admin 端假定内网可信；agent 端用共享 `X-Beacon-Token` 仅防误连（非安全边界）。
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

### 3. 长轮询拉有效配置 `GET /beacon/v1/agent/config/effective`
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
查询：`?namespace=&group=&zone=&role=`。返回按标签过滤的**在线**实例列表（归 agent 前缀 + agent token）。无匹配返回 `{ "instances": [] }`。

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

---

## 二、admin / UI 侧 `/admin/v1/*`

### 配置管理
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/configs?namespace=&group=&dataId=&scopeLevel=` | 列出配置项 |
| `GET /admin/v1/configs/{id}` | 取当前内容 + 元数据 |
| `POST /admin/v1/configs` | 新建（首次发布）：三元组 + scopeLevel/scopeTarget + format + content + operator + comment |
| `PUT /admin/v1/configs/{id}` | 发布新版本：content + operator + comment → version+1，返回新 `version`/`md5` |
| `DELETE /admin/v1/configs/{id}` | 软删（该层从合并链脱落，触发唤醒） |
| `GET /admin/v1/configs/{id}/revisions` | 历史版本列表 |
| `GET /admin/v1/configs/{id}/revisions/{version}` | 取某历史版本内容 |
| `POST /admin/v1/configs/{id}/rollback` | 回滚：`{ toVersion, operator, comment }`（= 读旧版内容作新版发布） |
| `GET /admin/v1/configs/{id}/diff?from=&to=` | 返回两版本文本供前端 diff |

错误：配置不存在 `404 CONFIG_NOT_FOUND`；回滚目标不存在 `404 REVISION_NOT_FOUND`；同标识重复建 `409 CONFIG_CONFLICT`；内容超长（> 256KB）`422 CONTENT_TOO_LARGE`；发布内容解析失败 `422 CONTENT_INVALID`；覆盖层/目标键不合法 `400 INVALID_SCOPE`；同一 dataId 跨层格式不一致 `422 FORMAT_INCONSISTENT`。

### 文件树托管（通道B）
整文件 blob，scope **整文件覆盖**（不深合并），版本/回滚同配置思路（见 [ADR-0010](adr/0010-file-tree-hosting-blob-channel.md)）。
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/files?namespace=&group=&path=&scopeLevel=` | 列出文件对象 |
| `GET /admin/v1/files/{id}` | 取当前整文件内容 + 元数据 |
| `POST /admin/v1/files` | 新建（首次发布）：`{ namespace, group, path, scopeLevel, scopeTarget, content, operator, comment }` |
| `PUT /admin/v1/files/{id}` | 发布新版本：`{ content, operator, comment }` → version+1，返回新 `version`/`md5` |
| `DELETE /admin/v1/files/{id}` | 软删（该层从覆盖链脱落，触发文件唤醒；下游 agent 据 manifest 删该 path 镜像） |
| `GET /admin/v1/files/{id}/revisions` | 历史版本列表 |
| `GET /admin/v1/files/{id}/revisions/{version}` | 取某历史版本内容 |
| `POST /admin/v1/files/{id}/rollback` | 回滚：`{ toVersion, operator, comment }` |

错误：文件不存在 `404 FILE_NOT_FOUND`；回滚目标不存在 `404 REVISION_NOT_FOUND`；同标识重复建 `409 FILE_CONFLICT`；路径不合法（空 / 绝对路径 / 含 `..` 穿越 / 含反斜杠）`400 INVALID_PATH`；内容超长（> 1MB）`422 CONTENT_TOO_LARGE`；覆盖层/目标键不合法 `400 INVALID_SCOPE`。

### 实例与健康
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/instances?namespace=&group=&zone=&role=&status=` | 按标签过滤（读内存注册表） |
| `GET /admin/v1/instances/{serverId}?namespace=` | 单实例详情 |
| `POST /admin/v1/instances/{serverId}/offline?namespace=&operator=` | 手动下线（移除内存条目） |

错误：实例不存在 `404 INSTANCE_NOT_FOUND`。

### zone 分配
| 端点 | 说明 |
|---|---|
| `GET /admin/v1/zones/assignments?namespace=&group=&zone=` | 列出 serverId→zone 指派 |
| `PUT /admin/v1/zones/assignments` | 新增/改派 upsert：`{ namespace, serverId, group, zone, operator, note }`，触发该 serverId 唤醒 |
| `DELETE /admin/v1/zones/assignments?namespace=&serverId=&operator=` | 取消指派（软删），触发唤醒 |
| `GET /admin/v1/zones?namespace=&group=` | zone 维度汇总（每 zone 服数/在线数） |

错误：指派不存在 `404 ASSIGNMENT_NOT_FOUND`。改派的长轮询唤醒在 M3 长轮询热更落地（M2 已即时重算有效配置、刷新内存归属）。

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
| 健康 | `status` | string | online / lost / offline |
