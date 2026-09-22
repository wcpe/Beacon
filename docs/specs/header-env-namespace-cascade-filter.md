# 功能规格：页眉 env→namespace 级联观测筛选

> 状态：草拟　·　关联 PRD：FR-214　·　依赖：[authoritative-observation-scope-contract.md](authoritative-observation-scope-contract.md)

## 1. 背景与目标

当前页眉只有 env 选择器，前端把 env 展开后在各页做不一致的单 namespace 查询或客户端过滤；集群/交付的 namespace picker 还会读取 env scope 并自动替换选择，存在“观测范围影响写目标”的隐患。

本功能在页眉提供 env → namespace 两级级联选择，所有接入页只把它作为 FR-213 的**观测查询参数**。切换范围不能改变表单、草稿、路由实体或 mutation payload 中的 namespace/目标；审批中心保持全局。

## 2. 需求（要什么）

### 2.1 级联语义

- 页眉显示两个紧凑选择器：env 在前、namespace 在后。
- env 选择“全部环境”时，namespace 可选“全部 namespace”或从全量 namespace 中精确选择；选择具体 env 后，namespace 只列该 env 当前映射项，并提供“该环境全部”。
- 用户显式切换 env 时，namespace 原子重置为该 env 的“全部”，不沿用上一 env 的 namespace；随后可再精确选择。
- 合法 env 没有映射时保持该 env + 空 namespace 范围，页面展示空态，不回退全部。
- 选中值持久化到版本化 localStorage；刷新/跨页保持。持久值不存在、已 tombstoned 或映射不再匹配时进入 `invalid`，停止所有 scoped 查询并要求用户重选。
- archived namespace 仍可显式选择并带“已归档，只读”标记：历史观测可查、实时态为空，所有 mutation 仍由领域生命周期守卫禁用。

### 2.2 只影响观测查询

- 观测页把 `envId? + namespaceId?` 放入 query key 和 API query，完全消费 FR-213；当前前端临时实现已移除 `filterItemsByEnvScope/filterItemsByEnvCodes`，但最终权威仍应由服务端查询契约执行，不得在分页结果后丢行。
- scope 变化时取消/失效旧观测 query、清游标并从第一页查询；旧详情若不在新范围内关闭或显示“超出当前观测范围”。
- mutation 的 namespace/target 必须来自显式领域表单、当前实体或 URL 主键，不得读取页眉 store、不得自动选首个 namespace、不得因 scope 改变而重写。
- 配置/文件/覆盖集/交付创建器继续使用独立、明确的 namespace 控件；可展示当前观测范围提示，但不能用它作为默认写目标。
- `/approvals` 及其导航 pending 数完全排除：不发送 scope、不随 scope 刷新；该页页眉显示“全局审批”。

### 2.3 接入范围

- 首批接入 FR-213 端点矩阵对应页面：总览、服务器、区服结构、拓扑、服务分析、连接、消息、命令、审计、告警事件及其详情。
- 控制面自身状态页不带 namespace scope；系统设置、版本、API key、env/namespace 管理等管理页不因页眉筛选隐藏管理对象。
- 页面同时有观测和写动作时，仅 read query 接 scope；写对话框必须回显不可编辑 ID/名称或要求重新显式选择，保证目标可见。

## 3. 设计（怎么做）

### 3.1 前端状态

使用一个最小客户端 store 保存：

```text
ObservationScopeSelection =
  | { kind: "all"; namespaceId?: number }
  | { kind: "env"; envId: number; namespaceId?: number }
  | { kind: "invalid"; savedEnvId?: number; savedNamespaceId?: number }
```

- store 只存选择，不缓存 env→namespace 权威映射；映射仍从现有管理 API 获取并由服务端每次请求复核。
- 初始化先进入 validating，待 env/namespace 选项验证完成才发观测请求；加载失败保留选择并停查，不按“全部”继续。
- query key 使用规范化 scope key，例如 `all:*`、`all:ns:12`、`env:3:*`、`env:3:ns:12`。

### 3.2 组件与数据流

- 把现有 `EnvFilter` 收敛为 `ObservationScopeFilter`，复用 `@beacon/ui` 的 Combobox/Dropdown，不引入新依赖。
- env 与 namespace 选项支持搜索；全量 namespace 使用服务端分页/按需搜索，不能固定 `pageSize=100` 后假装完整。
- 各页只通过 `useObservationScopeQuery()` 取得已验证 query 参数；mutation hook 不暴露、也不得导入该 hook。
- ESLint/架构测试锁定高风险目录：`features/delivery`、配置写表单、身份/区服分配动作不得从 observation scope store 读取写目标。

## 4. UX / 交互

- 用户任务：运维人员在任何观测页把视野切到“线上 env 的全部 namespace”或其中一个 namespace，跨页持续聚焦同一问题；需要写操作时清楚确认独立目标。
- 进入路径：全局页眉第二段左侧，env 与 namespace 相邻；键盘可依次聚焦，选项展示显示名称并辅以不可变 ID/code 区分同名。
- 操作闭环：选 env → namespace 选项级联 → 页面取消旧请求并从第一页加载 → 标题/空态显示当前范围 → 跨观测页保持；失效时停查并在页眉完成重选。
- IA 挂载：这是全局观测上下文，不是业务导航或授权；`/approvals` 显式替换为“全局审批”标签。

### 4.1 页面状态

- 空态：合法空映射或范围内无数据时显示当前 env/namespace 和“无观测数据”，提供切换范围；不显示全局数据。
- 加载态：首次验证和切换时展示页面骨架/局部刷新，旧范围数据不得以可操作状态残留；选择器保留已选标签。**解析期不得渲染成空态**：页眉选了具体 env 而 env 选项尚未就绪时，作用域解析按 fail-closed 返回空集合，此间受 scope 收窄的查询一律 `enabled: false`**不发请求**，由 react-query 原生 pending 态承接骨架（判定用 `isPending`：`enabled: false` 时 `isLoading = isPending && isFetching` 恒为 false，只有 `isPending` 表达「尚未取数」）；计数类指标显示未知而非 0（已落地：11 个观测数据页接骨架、`NamespaceSelect` 解析期禁用并显示「范围解析中…」、`/servers` 待确认计数置未知）。
- 错误态：scope 不存在/tombstoned/错配、映射加载失败或服务端 `observation_scope_stale` 时顶部明确提示并停止查询；提供重试/重选，不自动回退。archived 是合法只读态，不显示为错误。
- 超大数据量：env/namespace 选择器可搜索、虚拟化或服务端分页；页面数据继续服务端分页/聚合，切 scope 清 cursor，1000+ 子服不全量渲染。
- 常规态：页眉显示“env 显示名 / namespace 显示名或全部”与范围徽标；页面筛选只保留状态、角色、时间等局部维度，不重复 env/namespace 控件。

### 4.2 mockup 评审门

- 这是全局交互结构调整，必须先在演示模式做可点击 mock，再接真实 FR-213 API。
- mock 可切换空、常规、超大量态、异常四态；常规态覆盖 env 全部、env 内单 namespace、全部环境内单 namespace和跨页保持，异常覆盖持久 scope 失效。
- 浏览器评审必须同时演示：观测页随 scope 变化、审批中心不变化、一个含写动作的页面其 mutation 目标不变化。用户拍板后才接真。

## 5. 任务拆分

- [ ] 测试先红：级联重置、全量/具体 env、精确 namespace、持久化、tombstoned/错配停查、archived 只读与不回退。
- [ ] 测试先红：观测 query 参数/query key/cursor 重置；删除所有分页后客户端过滤正确性依赖。
- [ ] 测试先红：配置/交付/身份/区服 mutation payload 在 scope 切换前后完全相同；审批列表/徽标不发送 scope。
- [ ] 先完成四态可切换 mock，并经用户浏览器评审页眉、审批排除和 mutation 隔离。
- [ ] 评审通过后最小实现 store、`ObservationScopeFilter` 与各观测页 FR-213 接线；复用现有依赖。
- [ ] 独立复核：逐页检查 read/mutation 数据源，确认无自动首选 namespace、无 stale 回退全量、无重复页内 scope 控件。
- [ ] 文档同步：PRD 状态、UX 全局交互/页面状态/审批排除、ARCHITECTURE 前端数据流、API query、CHANGELOG。

## 6. 验收标准

1. 页眉可选全部环境、具体 env 的全部 namespace、具体 namespace；切 env 后 namespace 正确级联重置并跨页持久。
2. 合法空映射显示空态；不存在/tombstoned/错配的持久 scope 停止查询并提示重选，绝不展示全量或首个 namespace；archived scope 可查历史并明确只读。
3. 所有接入观测页发送 FR-213 参数，列表/total/KPI/导出一致；前端不再分页后过滤。
4. scope 切换不会改变任何 mutation 目标、表单 namespace、ChangeOrder 草稿或审批动作；自动首选目标测试为零。
5. `/approvals` 列表和 pending 徽标始终全局，不随页眉选择变化。
6. 四态 mockup 经用户浏览器评审拍板后才接真；前端测试覆盖范围切换、错误和 1000+ 数据场景。
7. 独立复核确认 FR-214 只消费 FR-213，没有另建前端/后端 scope 真源。

## 7. 风险 / 待定

- **（已修复）作用域真源错位**：本 FR 依赖页眉 env/namespace 级联选择驱动各页收窄。FR-213 引入 `observation-scope` 并接管页眉后，数据侧 hooks 一度仍读旧 `state/env-filter`（已停用），致收窄静默失效。现统一以 `observation-scope` 为唯一真源。

- “全部环境 + 单 namespace”需要可搜索的全量 namespace 数据源，不能沿用当前固定 100 条选项；实施时优先复用现有分页 API，不新增依赖。
- scope 切换时保留旧数据会造成误读，完全清空又可能闪烁；默认以骨架替代旧范围，正确性优先。
- 混合读写页面最容易误把 scope 当默认目标，必须以静态依赖检查和交互测试双重守护，不能只靠代码注释。
