# 功能规格：交付数据面一致性与幂等收口（blob 引用保护 · agent 能力守卫 · 跨端幂等）

> 状态：开发中　·　关联 PRD：FR-261 / FR-264 / FR-263　·　分支：feature/fr-261-blob-integrity

## 1. 背景与目标

交付数据面（`delivery_blob` + 流式收发 + 清理器）自 FR-165 落地后，控制面侧残留四类一致性缺口，且跨端（控制面 ↔ agent）**没有成文的幂等契约**：

- **FR-261**：blob 生命周期与变更单生命周期不同步——活动单引用的 blob 只在「模板源上传回执成功」那一刻刷新过引用时间，其余路径（审批通过准备期上传、启动、配置渲染、回滚）都不刷；于是保留期一过、清理器就可能把**还在被活动单消费的 blob 删掉**，目标下载 404 且无告警。同时「元数据表里有行、磁盘上没有文件 / 磁盘上有文件、元数据表没行」两种孤儿形态无人回收，上传中断残留的临时文件只删元数据不删文件。
- **FR-264**：[ADR-0069](../adr/0069-delivery-data-plane-blob-relay-and-agent-stream-transport.md) 后果段已明确要求「控制面须在启动 / 组单时校验目标 agent 能力版本，不对旧 agent 下发交付命令」，但 spec 与代码均无此守卫——不支持流式交付的旧 agent 会收到它根本执行不了的 `delivery_upload` / `delivery_push`，表现为「命令挂到超时」，运维只看到卡住，看不到原因。
- **FR-263**：控制面重启后命令重发、agent 重复 push/activate、result 重复回执、blob PUT 重传四种重复各自为政——有的靠 CAS 侥幸幂等，有的会重复推进状态、重复产生副作用。波次 A（FR-266~269）已在 **agent 侧**做了部分相关工作（单回执收敛、失败回执带事实、拒收回执），本条做的是**跨端契约收口**：把「谁负责幂等、幂等键是什么、重复被拒时回什么」写清并在控制面落地。

目标：**数据面不再出现「被删的 blob 还在被引用」与「磁盘/元数据互不相认的孤儿」，旧 agent 不被下发它执行不了的命令且拒绝原因可读，四类重复全部零二次副作用。**

## 2. 需求（要什么）

### FR-261 blob 引用保护与清理一致性（fix）

1. **引用刷新补全**：以下时刻都刷新该单引用 blob 的 `last_referenced_at`（文件项 sha + 配置冻结工件 sha）：
   - 变更单进入 approved（准备期上传窗口开启）；
   - 启动（payload 准备前后）；
   - 模板源上传回执成功（既有）；
   - 回滚下发（回滚仍要消费同一批 blob）。
2. **孤儿文件扫描与回收**：清理器每轮扫 `blobs/` 目录，回收两类孤儿——① 磁盘有文件但元数据无行（或元数据非 ready）；② 元数据标 ready 但磁盘文件缺失（不删磁盘——没有可删的，改把元数据降级/删除，使 `Head` 与真实盘面一致）。删除入审计。
3. **上传残留删文件**：`uploading` 残留清理除删元数据行外，一并删除该 sha 对应的磁盘文件（此前只删元数据，文件永久滞留）。
4. **MarkReady 检查行数**：`MarkReady` 必须校验其 `UPDATE` 实际影响行数 > 0；为 0（占位行被并发清理器回收）视为失败并明确报错，不得静默「假就绪」——否则元数据没有行、`Head` 却能查到文件（或反之）的不一致态被悄悄造出来。
5. **审批回滚补偿删除**：变更单审批被撤销（approved → draft）/ 草稿被删时，其**专属** blob 不再被任何单引用，保留期未到也应可被下轮清理回收——即清理判定须以「当前所有单的引用」为准，撤销后的单不再计入保护集。

### FR-264 agent 能力版本守卫（feat，落实 ADR-0069 L58）

1. 控制面在**启动**与**下发交付命令**（upload / push / activate / rollback）两处校验目标 agent 是否具备流式交付能力；不具备则**不下发命令**，被拒目标给出可读原因。
2. 守卫口径与波次 A 的 `gracefulShutdownSupported` 一致（ADR-0088）：agent 侧能力位是**显式上报的能力事实**，控制面侧是**按版本下限推导的守卫**——两者不冲突、不互相取代：能力位判「能不能关服」（agent 自持、fail-closed），版本守卫判「能不能接流式交付命令」（控制面自持、fail-closed）。
3. 判定真源：`agent_identity.agent_version`（agent 注册自报）+ 运维可配的**最低交付能力版本**设置项；未上报版本（旧 agent 空串）一律按「不支持」处理。
4. 被拒目标的原因必须可读且可处置（「agent 版本 x 低于最低要求 y，请升级 agent」），落到目标行 `error` 并随目标视图透出；不下发命令、不重复下发。

### FR-263 交付幂等语义收口（feat，spec 先行）

为四类重复定义并落地幂等语义（详见 §3.3）：

| 重复场景 | 幂等键 | 负责方 | 重复时的行为 |
|---|---|---|---|
| 控制面重启后命令重发 | (namespace, serverId, type, payload.orderId) | 控制面 | 已存在在途同键命令则复用，不新建第二条 |
| agent 重复 push / activate | 命令生命周期 | 控制面 | 命令已终态则回执被拒（前态 CAS），不二次推进状态机 |
| result 重复回执 | 命令 id + 前态 `fetched` | 控制面 | 第二条被 CAS 拒，返回既有错码且**不改变**已落定的状态与计数 |
| blob PUT 重传 | sha256（内容寻址） | 控制面 | 已 ready 直接秒传成功；并发重传按 rename 去重；字节完全一致 |

范围内：控制面侧幂等落库与契约定义、清理一致性、能力守卫。
不做（范围外）：agent 侧代码（`apps/agent/**`）零改动；编排推进主体（advance / rollback / stream / control）不改逻辑，只挂载守卫；不做跨控制面实例分布式锁（控制面单实例运行，库内 CAS 即足够）。

## 3. 设计（怎么做）

### 3.1 数据面（FR-261）

- **引用刷新**：`DeliveryBlobService.TouchReferences(orderID)` 已存在且覆盖「文件项 sha + 配置工件 sha」，本条补齐**调用点**——新增导出方法 `TouchReferencesForOrders(orderIDs []uint)` 供批量场景，并在下列路径调用既有刷新：
  - `DeliveryOrderService` 审批流落到 approved 的时刻（只读改造受限 → 改为在**启动**与**下发**前统一刷新，覆盖准备期上传窗口）；
  - `DeliveryOrchestrator` 启动（payload 准备前）与回滚下发前。
  刷新是**廉价的 UPDATE ... WHERE sha IN (...)**，空集合 no-op，重复调用无副作用。
- **孤儿回收**：`DeliveryBlobCleaner` 每轮追加 `purgeOrphanFiles()`——遍历 `<root>/blobs/<xx>/<sha>`：
  - 文件名不是 64 位小写 hex → 非本域产物，跳过（防误删他目录内容）；
  - 元数据无行或非 ready → 删文件（元数据 ready 但文件在的情况不会走到这里，见下条）；
  - 元数据 ready 但文件缺失 → **降级元数据**（删行），使 `Head` 回到「未就绪」而非「以为就绪、打开才 404」。
  两项均计入本轮 `deleted / freed` 并进同一条系统审计。
- **上传残留**：`purgeStaleUploading` 删元数据行时，同时删该 sha 的磁盘文件（文件可能不存在，`os.Remove` 幂等）。
- **MarkReady 行数校验**：仓库 `MarkReady` 取 `RowsAffected`，为 0 返回新的 apperr（409 `blob_upload_slot_lost`），服务层原样上抛——agent 收到可重试的明确错误，而不是「上传成功但 HEAD 不到」的幽灵态。
- **审批回滚补偿**：清理的受保护集以**当前库内状态**为准（`ListSHAsReferencedByStatusNotIn` 按 status 过滤），撤销回 draft 的单 status=draft 不在终态集合内故**仍受保护**（draft 还可能再启动）；真正解除保护发生在草稿被删 / 单达终态后。为避免「删草稿后 blob 滞留到保留期满」，删除草稿与单达终态时**不**主动删 blob（可能有他单引用），而是让下轮清理按「无任何非终态单引用」自然回收——这与 §2.1.5 的要求一致，无需额外补偿删除逻辑。

### 3.2 能力版本守卫（FR-264）

- 新增运维设置 `delivery.min-agent-version`（默认 `0.29.0`，即 FR-165 数据面落地版本；空串 = 不校验）。
- 新增纯函数 `deliveryAgentSupportsStreaming(version string, minVersion string) bool`：按 `.` 切分逐段数值比较，长度不等补 0；**空版本 → false**（旧 agent 未上报）；含非数字段（如 `1.4.0-rc.1`）取前缀数字段比较，非数字后缀不影响主版本判定。
- 新增窄查询 `AgentIdentityRepository.FindVersionsByServerIDs(namespaceID uint, serverIDs []string) (map[string]string, error)`：一次批量取回，避免逐目标查库。
- 守卫挂点（**只读 `delivery_order_service.go` 与 orchestrator，最小改动**）：
  - 启动：`prepareStart` 末尾校验 **全目标集 + 模板源**（源也要能上传）；
  - 下发：`dispatchPending` / `activateTarget` / `dispatchRollback` 下发命令前校验该目标；
  - 上传命令：`resolvePayloadPlan` 生成 upload 命令前校验模板源。
  不通过 → 目标置 `failed` + `error` 写可读原因（启动期则整单拒绝启动并报错），**命令一条都不建**。
- 与 ADR-0088 的关系：ADR-0088 的 `gracefulShutdownSupported` 是 **agent 进程内**对「关服原语是否实现」的自探测，只影响 restart 生效是否回执 success；本守卫是**控制面**对「agent 版本是否够新到认识流式交付命令」的前置判定，只影响是否下发。二者判定主体、时机、失败动作都不同，互不覆盖；同一台旧 agent 会先被本守卫拒（不下发），即便侥幸下发也会被 agent 侧能力探测拒（不回执 success）——双 fail-closed，不冲突。

### 3.3 跨端幂等语义（FR-263）

> 与波次 A 的边界：ADR-0088 / spec §4.6.4 已规定「agent 侧同一条命令只发一条回执」，本条在控制面侧给出**被重复时的确定行为**，两端合起来才是完整契约。

1. **命令重发（控制面重启）**：新增 `FindActiveByTypeAndOrder(ns, serverID, cmdType, orderID)`（按 payload `orderId` 应用层过滤的既有范式），下发前先查：已存在 pending/fetched 同键命令则**复用**不新建。落在下发路径的守卫位，不进 advance 主体逻辑。
2. **重复 push / activate**：命令终态由推进器单点推进（`fetched → done/failed` CAS），重复回执必然 CAS 未命中；控制面**不改变**目标状态与计数，返回既有 `ErrCommandNotFound`，使 agent 侧能区分「回执被接受」与「重复被拒」。
3. **result 重复回执**：`ReceiveResult` 在 CAS 未命中时返回明确错误（当前 `errOrCommand` 已返回 `ErrCommandNotFound`，本条补**专门测试锁定**：重复回执不改状态、不改 `result_detail`、不二次唤醒推进器）。
4. **blob PUT 重传**：`persistBlob` 的秒传去重已覆盖「已 ready」；并发重传由 `placeBlobFile` 的 rename 去重覆盖；哈希不符的占位行由清理器回收。本条补测试锁定「同一内容并发 PUT N 次只落一份、均成功」。

### 3.4 错误码

新增两条 `apperr`：

- `blob_upload_slot_lost`（409）：上传占位行在落账前被清理器回收，请重传。
- `agent_capability_unsupported`（409）：目标 agent 版本低于交付能力最低要求（或未提供版本），已拒绝下发。

## 4. UX / 交互

纯后端 FR，无新增页面。被拒目标的 `error` 字段随既有 `/admin/v2/change-orders/{id}/targets` 目标视图透出，运维在既有目标列表即可看到「agent 版本 x 低于最低要求 y，请升级 agent」。

## 5. 任务拆分

- [x] 新建本 spec（FR-261 / 264 / 263 合一）
- [ ] FR-261：引用刷新调用点补全 + 孤儿回收 + 残留删文件 + MarkReady 行数校验（红→绿）
- [ ] FR-264：版本比较纯函数 + 批量版本查询 + 启动/下发守卫（红→绿）
- [ ] FR-263：命令重发复用 + 四类重复幂等测试锁定（红→绿）
- [ ] 文档同步：PRD 状态、本 spec、CHANGELOG

## 6. 验收标准

- **FR-261**：活动单引用的 blob 在保留期到期后仍不被删（引用刷新生效）；两类孤儿文件各有一条用例证明被回收；uploading 残留清理后磁盘文件同步消失；`MarkReady` 影响 0 行时返回 409 而非静默成功。
- **FR-264**：旧版本 / 空版本目标被拒下发、目标行 `error` 可读；新版本目标行为完全不变；启动期含不支持目标则整单拒绝启动。
- **FR-263**：重发命令不产生第二条在途同键命令；重复回执不改状态与计数；并发 PUT 同一内容只落一份且全部成功。
- 全量 `go test ./...` + `golangci-lint run ./...` 绿。

## 7. 风险 / 待定

- **版本下限取值**：默认取 FR-165 落地版本（0.29.0）。真机上 agent 上报的是 `apps/agent/gradle.properties` 的 `version`（当前 0.1.0），与服务端 `VERSION` 不同源——**真机须先确认 agent 上报串的实际形态**再定默认值；运维可用 `delivery.min-agent-version` 空串临时关闭守卫。
- **孤儿扫描成本**：`blobs/` 目录遍历是 O(文件数)，默认保留 7 天 + 20 GiB 上限下规模可控；超大部署若成热瓶颈，可降频（复用现有清理间隔设置）。
- **不做**分布式锁：多控制面实例并发下发仍可能建出同键命令（既有行为），需多实例部署时另立 FR。
