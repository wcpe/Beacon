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

1. **引用刷新补全**：刷新该单引用 blob 的 `last_referenced_at`（文件项 sha + 配置冻结工件 sha）。
   **实现方式（唯一口径）**：推进器每轮对 `deliveryBlobReferenceRefreshStatuses` = **approved + rolling + paused + rolling_back** 的单批量刷新（`TouchReferencesForOrders`），另在启动（`resolvePayloadPlan`）与模板源上传回执成功（既有）时按单刷新。
   - **不碰 `delivery_order_service.go`**：approved 是审批域在事务内落的状态，在那里挂刷新会把数据面耦合进审批事务；改为推进器按状态批量覆盖，approved 单在准备期停留多久都能被持续刷新到（宽限从「最后一次刷新」起算）。
   - **approved 必须在集合内**：准备期等模板源上传常超过 1 小时补偿宽限，只靠启动那一次刷新挡不住（P0-1 实测）。
   - draft 不纳：未提审，谈不上消费。
2. **孤儿文件扫描与回收**：清理器每轮扫 `blobs/` 目录，回收两类孤儿——① 磁盘有文件但元数据无行（或元数据非 ready）；② 元数据标 ready 但磁盘文件缺失（不删磁盘——没有可删的，改把元数据降级/删除，使 `Head` 与真实盘面一致）。删除入审计。
3. **上传残留删文件**：`uploading` 残留清理除删元数据行外，一并删除该 sha 对应的磁盘文件（此前只删元数据，文件永久滞留）。
4. **MarkReady 检查行数**：`MarkReady` 必须校验其 `UPDATE` 实际影响行数 > 0；为 0（占位行被并发清理器回收）视为失败并明确报错，不得静默「假就绪」——否则元数据没有行、`Head` 却能查到文件（或反之）的不一致态被悄悄造出来。
5. **审批回滚补偿删除**：变更单审批被撤销（approved → draft）/ 草稿被删时，其**专属** blob 不再被任何单引用，保留期未到也应可被下轮清理回收——即清理判定须以「当前所有单的引用」为准，撤销后的单不再计入保护集。实现为「无人引用过短宽限即回收」的独立路径（见 §3.1），不与保留期路径共用候选集。

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

- **引用刷新**：`DeliveryBlobService.TouchReferences(orderID)` 已存在且覆盖「文件项 sha + 配置工件 sha」，本条补齐**调用点**——新增导出方法 `TouchReferencesForOrders(orderIDs []uint)` 供批量场景，并在下列路径调用：
  - `DeliveryOrchestrator.refreshBlobReferences()`：**每轮**对 `deliveryBlobReferenceRefreshStatuses`（**approved + rolling + paused + rolling_back**）批量刷新——这是持续保护的主路径，与 §2.1 第 1 条同口径；
  - `DeliveryOrchestrator.resolvePayloadPlan`（启动，payload 准备前）：准备期起点补一次。
  **不碰 `delivery_order_service.go`**（与 §2.1 一致）：approved 由审批域在事务内落，在那儿挂刷新会把数据面耦合进审批事务；按状态批量覆盖即可，approved 单在准备期停留多久都刷得到。
  刷新是**廉价的 UPDATE ... WHERE sha IN (...)**，空集合 no-op，重复调用无副作用。
- **删除顺序与 TOCTOU 收口**：`purgeBlob` **先删元数据行、行删成功才删盘**（行是保护判定真源）；两条回收路径都在**删除前二次确认引用**（判定与删除之间的窗口可能有单提交引用行），不留「拿旧快照删活 blob」的缝。
- **孤儿回收**：`DeliveryBlobCleaner` 每轮追加 `purgeOrphanFiles()`——遍历 `<root>/blobs/<xx>/<sha>`：
  - 文件名不是 64 位小写 hex → 非本域产物，跳过（防误删他目录内容）；
  - 元数据无行或非 ready → 删文件（元数据 ready 但文件在的情况不会走到这里，见下条）；
  - 元数据 ready 但文件缺失 → **降级元数据**（删行），使 `Head` 回到「未就绪」而非「以为就绪、打开才 404」。
  两项均计入本轮 `deleted / freed` 并进同一条系统审计。
- **上传残留**：`purgeStaleUploading` 删元数据行时，同时删该 sha 的磁盘文件（文件可能不存在，`os.Remove` 幂等）。
- **MarkReady 行数校验**：仓库 `MarkReady` 取 `RowsAffected`，为 0 返回新的 apperr（409 `blob_upload_slot_lost`），服务层原样上抛——agent 收到可重试的明确错误，而不是「上传成功但 HEAD 不到」的幽灵态。
- **审批回滚 / 删草稿后的补偿回收**：清理判定拆成**两条独立路径**，结果合并：
  - ① **保留期路径**（原有）：ready 且超保留期、且不被非终态单引用 → 删；
  - ② **无人引用路径**（本条新增）：只要该 blob 不被**任何**变更单（文件项 ∪ 配置冻结工件，不施加状态过滤）引用，过**短宽限**（`deliveryUnreferencedGraceHours`，默认 1 小时）即回收，不等满 7 天保留期。
    - 依据：撤销审批回 draft 后删掉草稿、或单达终态后，其专属 blob 已无人消费；等到保留期满纯属浪费容量。
    - **宽限不能为 0**：写入 blob 与「引用它的变更项 / 配置工件落库」不在同一事务，瞬时存在「已落盘、引用行未提交」的窗口，零宽限会误删刚上传、马上要被消费的 blob。1 小时足以覆盖该窗口与控制面重启间隔。
  - 两条路径**必须独立执行**：若把 ② 挂在 ① 的候选集后面，一旦「超保留期候选为空」就整轮跳过补偿回收。实现期实测先写完的合并版本正是这样被早退吞掉的，故显式拆开并各配一条测试锁定。
  - 单达终态时仍**不**主动级联删 blob：可能有他单引用同一内容（内容寻址天然共享），交给下轮清理按「无任何非终态单引用」自然收敛。
- **时间源修正**：清理器 `now` 由 `time.Now` 改为 `time.Now().UTC`。`last_referenced_at` 全链路以 UTC 落库，用本地时间相减会整体偏移一个时区差（UTC+8 机器上等于把保留期 / 宽限判定提前 8 小时），实测会误删仍在宽限内的新鲜 blob。

### 3.2 能力版本守卫（FR-264）

- 新增运维设置 `delivery.min-agent-version`（默认 `0.29.0`，即 FR-165 数据面落地版本；空串 = 不校验）。
- 新增纯函数 `deliveryAgentSupportsStreaming(version string, minVersion string) bool`：按 `.` 切分逐段数值比较，长度不等补 0；**空版本 → false**（旧 agent 未上报）；含非数字段（如 `1.4.0-rc.1`）取前缀数字段比较，非数字后缀不影响主版本判定。
- 新增窄查询 `AgentIdentityRepository.FindVersionsByServerIDs(namespaceID uint, serverIDs []string) (map[string]string, error)`：一次批量取回，避免逐目标查库。同键多行取 `status_changed_at` 最新一行；无身份行的服**不回键**（由调用方按「无版本 = 旧 agent」fail-closed 处理）。
- 守卫挂点（**只读 `delivery_order_service.go` 与 orchestrator，最小改动**）：
  - 启动：`prepareStart` 内校验 **全目标集 + 模板源**（源也要能上传），不合格即**整单拒绝启动**；
  - 下发：`dispatchPending` / `activateTarget` 下发命令前校验该目标，不合格就地置 `failed` + 写原因；
  - 回滚：`dispatchRollback` 下发前校验，不合格落 `rollback_status=failed` + `rollback_error`（不动主状态）。
  不通过时**命令一条都不建**。
- **事务安全**：能力查询必须复用调用方事务连接（`capabilityGuard.withTx`）。审批适配器在「已开启事务」内启动变更单，守卫若另开连接，在单连接池（测试 / 受限部署）下会与外层事务互等死锁——这一条是实测踩到的，不是理论风险。
- **为什么置 failed 而不是静默跳过**：不下发命令的目标若仍留 `pending`，推进器每轮重扫重试，运维只看到「卡住」；置 failed 并带原因才把「为什么没动」表达出来（与 ADR-0088 同族的可观测纪律）。
- 与 ADR-0088 的关系：ADR-0088 的 `gracefulShutdownSupported` 是 **agent 进程内**对「关服原语是否实现」的自探测，只影响 restart 生效是否回执 success；本守卫是**控制面**对「agent 版本是否够新到认识流式交付命令」的前置判定，只影响是否下发。二者判定主体、时机、失败动作都不同，互不覆盖；同一台旧 agent 会先被本守卫拒（不下发），即便侥幸下发也会被 agent 侧能力探测拒（不回执 success）——双 fail-closed，不冲突。

### 3.3 跨端幂等语义（FR-263）

> 与波次 A 的边界：ADR-0088 / spec §4.6.4 已规定「agent 侧同一条命令只发一条回执」，本条在控制面侧给出**被重复时的确定行为**，两端合起来才是完整契约。

1. **命令重发（控制面重启）**：新增 `FindActiveByTypeAndOrder(ns, serverID, cmdType, orderID)`（按 payload `orderId` 应用层过滤的既有范式），`dispatchPending` / `activateTarget` / `dispatchRollback` 下发前先查：已存在 pending/fetched 同键命令则**复用**不新建。
   - 只看**在途态**是刻意的：已 done/failed/expired 的历史命令不得阻拦下一轮下发，否则重试 / 回滚再下发会被历史命令永久挡住。
   - 幂等键取「payload 内 orderId」而非加列：payload 是 TEXT JSON，用 SQL JSON 函数会破坏 DB 可移植（架构不变量 §4）；单服在途交付命令量级为个位数，全取后内存匹配代价可忽略。
2. **重复 push / activate**：命令终态由推进器单点推进（`fetched → done/failed` CAS），重复回执必然 CAS 未命中；控制面**不改变**目标状态与计数，返回既有 `ErrCommandNotFound`，使 agent 侧能区分「回执被接受」与「重复被拒」。
3. **result 重复回执**：`ReceiveResult` 在 CAS 未命中时返回明确错误（`ErrCommandNotFound`）。已锁定三条行为：不改命令状态、不改 `result_detail`（失败原因不得覆盖已落定的成功事实）、**不二次唤醒推进器**（唤醒是「回执落定」的副产物，不是「收到回执」的副产物）。
4. **blob PUT 重传**：`persistBlob` 的秒传去重覆盖「已 ready」（不重写磁盘、mtime 不变）；并发重传由 `placeBlobFile` 的 rename 去重覆盖（同 sha 内容必然一致）；哈希不符的占位行由清理器回收。已锁定「同一内容并发 PUT 8 次只落一份、全部成功、字节一致」。

### 3.4 错误码

新增两条 `apperr`：

- `blob_upload_slot_lost`（409）：上传占位行在落账前被清理器回收，请重传。
- `agent_capability_unsupported`（409）：目标 agent 版本低于交付能力最低要求（或未提供版本），已拒绝下发。

## 4. UX / 交互

纯后端 FR，无新增页面。被拒目标的 `error` 字段随既有 `/admin/v2/change-orders/{id}/targets` 目标视图透出，运维在既有目标列表即可看到「agent 版本 x 低于最低要求 y，请升级 agent」。

## 5. 任务拆分

- [x] 新建本 spec（FR-261 / 264 / 263 合一）
- [x] FR-261：引用刷新调用点补全 + 孤儿回收 + 残留删文件 + MarkReady 行数校验（红→绿）
- [x] FR-264：版本比较纯函数 + 批量版本查询 + 启动/下发/回滚守卫（红→绿）
- [x] FR-263：命令重发复用 + 四类重复幂等测试锁定（红→绿）
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
