# ADR-0067：小区默认入口真源收敛到 v2 `server.is_default_entry`（取代 ADR-0031 的存储决策）

**状态**：已接受（取代 [ADR-0031](0031-zone-default-entry-and-bc-injection.md) 决策 1 的存储与写入口；其注入机制决策 2/3/4 保留不变）

## 背景

默认入口存在 v1/v2 双真源断层（真机排障与代码排查证实）：

- **v1 真源** `zone_default_entry` 表（ADR-0031 决策 1）是 BC fallback 注入链路唯一消费的源（`GET /beacon/v1/agent/discovery` 渲染 `zoneDefaultEntry` 标志）。但其写端点 `PUT/DELETE /admin/v1/zones/default-entry` **从未有过任何 UI 调用者**（Legacy web 只读列表、第二版管理台不写它）——该表在实际部署上恒空，BC 动态默认入口注入**从未激活**，一直靠 BungeeCord `config.yml` 静态 `server-priority` 兜底。
- **v2 真源** `server.is_default_entry` 列（FR-155，`docs/specs/v2-zone-authority.md` §4.4）是第二版管理台唯一可写的默认入口（分配勾选 / `PUT /admin/v2/servers/{id}/default-entry`）。但它此前**没有任何下发链路消费**——只被管理台列表与 zone-tree 计数读取。spec §4.4 承诺的「BC 注入下发」从未实现。

结果：管理台改默认入口 → BC 永远看不到 → fallback 不生效；而能驱动 BC 的 v1 写端点又不可达。两环都断。

## 决策

### 1. 默认入口唯一真源 = v2 `server.is_default_entry` 列

发现 / 实例视图的 `zoneDefaultEntry` 标志解析器改读 v2（`V2ControlPlaneService.DefaultEntryServerIDs`：按 namespace 取 `is_default_entry=true` 的 serverId 集合），装配处（`main.go` 的 `SetDefaultEntryResolver`）换源即可，注入机制（discovery 标志 + agent `DefaultEntrySelector`，ADR-0031 决策 2/3/4）不动。写入口 = v2 分配勾选与 toggle 端点（校验已分配、换区自动清、proxy 恒 false 等不变量沿 spec §4.4）。

### 2. v1 表与写端点移除，只读列表 v2 背书

- `zone_default_entry` 表（model / repository / AutoMigrate）删除——真源唯一化，不留双写或迁移期兼容（该表实际恒空，无数据可迁）。
- `PUT/DELETE /admin/v1/zones/default-entry` 路由移除——从未有调用者的写端点，保留只会成为「写了无人消费」的静默失效陷阱（违背错误可见原则）。
- `GET /admin/v1/zones/default-entry` 保留（Legacy web 只读消费），改由 v2 解析：`(group, zone)` 取大区名 / 小区名（与 v1 code 语义对应），响应形状不变。

### 3. 管理台补默认入口 toggle 入口

/servers 行操作补「设为 / 取消默认入口」（镜像 draining 模式，接既有 v2 toggle 端点）——补上 PRD P3「default-entry 页内可操作」承诺缺口；分配弹窗勾选保留。

## 已知边界（不在本决策内，另行决策）

实例 zone 归属解析仍走 v1 `zone_assignment`（注册时回填 `ResolvedGroup/Zone`），agent 选择器按 `(group, zone)` 匹配 `proxy.home-group/home-zone`。**v1 无 zone 指派行的纯 v2 部署**下实例 zone 为空、选择器匹配不上——这是 v1/v2 拓扑真源分裂的更大独立事项（配置覆盖链 / 长轮询分组 / 影响面分析同样读 v1 指派），需单独 ADR 决策统一方向，本 ADR 不扩大范围。

## 后果

- 管理台默认入口操作即时生效于 BC fallback 注入（e2e：v2 set → v1 discovery `zoneDefaultEntry=true` 断言贯通）。
- v1 默认入口写 API 属破坏性移除，但无任何已知调用者（Legacy web 只读、e2e 走 v2）；CHANGELOG 明示。
- `DEFAULT_ENTRY_SERVER_NOT_IN_ZONE` / `DEFAULT_ENTRY_NOT_FOUND` 错误码随 v1 写端点退役；v2 侧沿用 `not_assigned`（409）。
