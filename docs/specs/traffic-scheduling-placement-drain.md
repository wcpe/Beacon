# 功能规格：流量调度（落位均衡 / drain）

> 状态：开发中　·　关联 PRD：FR-10　·　分支：feature/fr-10-traffic

## 1. 背景与目标

第二期治理增强（P2）。当一个 zone 下有多台同质子服时，需要一个统一的依据来回答"新玩家该落到哪台"，以及"哪台正在维护、暂时别再往里送人"。当前控制面已有注册/健康/容量（capacity）/权重（weight）的事实，但缺少把这些事实转成**落位建议**的查询，也缺少**drain（排空 / 维护标记）**这一运维状态位。

本期目标：控制面**只给调度决策（query-only）**——基于注册/健康/容量/权重，给出"某 zone 内推荐落位子服"的排序建议，以及"drain"标记的设置 / 查询。**真正把玩家送到目标服由数据面执行**，控制面不碰玩家连接、不写游戏逻辑（架构红线，见 ADR-0017）。

## 2. 需求（要什么）

- 范围内：
  - **drain 标记**：管理台可对某 `(namespace, serverId)` 标记 / 取消 drain（排空 / 维护）。drain 是运维**决策**，须跨控制面重启存活 → 落 DB（与 `zone_assignment` 同源类别），读时叠加到内存实例视图上。
  - **落位均衡决策（query-only）**：给定 `(namespace, group?, zone)`，返回该 zone 内**候选子服按推荐优先级排序**的建议。仅纳入 `status=online` 且**未被 drain** 的实例；按权威事实排序：weight 降序 → capacity 降序 → serverId 升序（稳定且确定性）。返回每个候选的 serverId / address / weight / capacity / drain 等事实，供数据面据此落位。
  - **drain 影响选择**：被 drain 的实例从落位候选中剔除（即使在线），用于"维护前先排空"。
  - 边界：zone 内无在线候选（空集 / 全部 drain / 全部离线）→ 返回空候选列表（200，不报错），由数据面决定兜底。
  - 单测覆盖：落位选择排序算法、drain 标记影响选择、空集 / 全忙（全 drain / 全离线）边界。
- 不做（范围外）：
  - **canary 引流**（金丝雀按比例引流 / 灰度放量）——更细的 P2/P3，本期不做；若要做须先停下来在 ADR 里界定（见 ADR-0017）。不引入 `canary` 字段（范围纪律红线）。
  - 控制面**主动**把玩家连接路由 / drain 时强制踢人——属玩家连接执行，归数据面。
  - 落位不读 agent 上报的 `playerCount` / `tps`（二者仅展示、不参与决策，维持注册表既有不变量）。

## 3. 设计（怎么做）

控制面分层照旧 `router → handler → service → repository`，新增能力域 `scheduling`。

- **数据模型**：新增 `server_drain` 表（GORM AutoMigrate，零方言绑定，可移植）。字段 `namespace_code / server_id / reason / created_at / updated_at`，按 `(namespace_code, server_id)` 唯一。drain 的存在性即"已 drain"，取消即软删（沿用 `SoftDeleteSentinel` 模式，与 `zone_assignment` 一致）。
- **repository**：`ServerDrainRepository`，提供 `Upsert / SoftDelete / FindByServer / ListActive`（按 ns 过滤、返回未软删集合）。
- **service**：`SchedulingService` 编排
  - drain 标记 / 取消：事务内写 `server_drain` + 审计原子完成（与 zone 改派同模式）。
  - 落位建议：读内存注册表（在线实例）+ DB drain 集合，纯函数 `rankPlacement` 排序、剔除 drained，返回候选。排序为无副作用纯函数，便于穷举单测。
- **handler + 路由**（admin 侧，挂登录令牌中间件）：
  - `GET  /admin/v1/scheduling/placement?namespace=&group=&zone=` → 落位候选列表。
  - `PUT  /admin/v1/scheduling/drains`（body: namespace/serverId/reason）→ 标记 drain。
  - `DELETE /admin/v1/scheduling/drains?namespace=&serverId=` → 取消 drain。
  - `GET  /admin/v1/scheduling/drains?namespace=` → 列出当前 drain。
- **审计**：drain 标记 / 取消各记一条审计（action `scheduling.drain` / `scheduling.undrain`，target type `instance`）。
- **架构边界**：决策只读、不下发、不触发长轮询唤醒（drain 不改变有效配置 / 文件树归属，不影响 agent 拉取）。详见 ADR-0017。

## 4. 任务拆分

- [ ] ADR-0017：落位均衡 / drain 决策由控制面给、执行在数据面；canary 划范围外
- [ ] model `server_drain` + AutoMigrate 注册
- [ ] repository `ServerDrainRepository`（先写失败单测再实现）
- [ ] service `SchedulingService` + 纯函数 `rankPlacement`（穷举单测：排序 / drain 剔除 / 空集 / 全忙）
- [ ] handler + 路由装配 + main 注入
- [ ] 文档同步：ARCHITECTURE（§7 调度小节）、API、CHANGELOG 未发布段末尾追加

## 5. 验收标准

- 同 zone 多在线实例时，`placement` 候选按 weight 降序、capacity 降序、serverId 升序确定性返回。
- 标记某 serverId drain 后，它从 `placement` 候选中消失；取消后回到候选。
- zone 内空集 / 全部 drain / 全部离线 → `placement` 返回空候选（200），不报错。
- drain 跨控制面重启仍生效（落 DB）。
- `go test ./...` 全绿；新增逻辑有单测覆盖。
- 全程不出现 `canary` 字段 / 不操作玩家连接 / 落位不读 playerCount。

## 6. 风险 / 待定

- "落位均衡"在控制面层只能给基于静态事实（weight/capacity/online/drain）的**建议**，活跃负载（实际在线人数）由数据面掌握并做最终落位——这是架构红线（控制面不碰玩家连接）使然，已在 ADR-0017 记录。
- 若后续要按实时负载精排或 canary 引流，须先写新 ADR 界定边界，不在本期擅自扩展。
