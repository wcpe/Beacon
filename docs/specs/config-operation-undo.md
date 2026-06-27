# 功能规格：配置操作级撤回子系统

> 状态：开发中　·　关联 PRD：FR-116　·　分支：feature/fr-116-undo-subsystem　·　决策见 [ADR-0051](../adr/0051-config-operation-undo.md)

## 1. 背景与目标

工作台原型（FR-114）落地了「撤回 / 回滚 / 操作日志」一整套交互，但「撤回」是纯前端 mock——内存里假装成功、后端真源未变、刷新即穿帮。FR-116 把它落成**真实后端能力**：清单内三类大操作（下发 push / 反向抓取 fetch / 发布 publish）支持**事务级撤回**，撤回后控制面配置 / 纳管真源真正回到操作前并按需重推。属第二期（P2）。

本 FR 严格照 ADR-0051 施工，**不写新 ADR**。

## 2. 需求（要什么）

- 新增一层「可逆操作记录」（`reversible_operation` 表）作撤回真源：push/publish/fetch 落地时**同事务**记一条带反向快照的可逆账目。
- 撤回端点 `POST /admin/v1/reversible-operations/{id}/undo`：`full` 角色可撤、`readonly` 一律 403、撤回入审计（`config.undo-push` / `config.undo-publish` / `config.undo-fetch`）。
- 撤回语义复用既有体系，撤回子系统只做编排：
  - **undo publish** = 把该配置项回滚到发布前版本（复用 `ConfigService.Rollback`）。
  - **undo push** = 把目标层文件回滚到下发前版本（复用 `FileService.Rollback`）。本实现中 push 等价于「把某层文件覆盖发布 / apply 到目标」，反向快照记发布前版本号。
  - **undo fetch** = 撤销该次 ingest 纳管：被该次 ingest **新建**的受管项软删、被**覆盖更新**的受管项回滚到 ingest 前版本（不删磁盘文件）。
- 幂等：仅 `reversible` 可撤、撤后 `reversed`，重复撤回返回「已撤回」成功响应、不再二次回滚。
- 并发安全：撤回与新操作 / 其它撤回并发不脏写、不双撤回（status CAS 串行化同一行）。
- 事务边界：撤回多表写原子、提交成功后才唤醒长轮询、事务内不做下发 IO。
- 过期 + 被覆盖双闸：超 N 小时（热改设置）置 `expired`；被后续操作覆盖置 `superseded`；二者不可撤、撤回端点返回明确错误。
- 清理器周期扫过期。
- web：工作台「操作日志」读真实可逆记录、逐条 / 批量撤回 + 顶部「撤回上一步」接真实撤回端点（替换原本地 mock）。
- 范围内：上述三类大操作的有限窗口撤回。
- 不做（范围外）：删除 / 重命名 / 移动等纯结构编辑的独立撤回（其撤回天然等同回滚上一版本，被 publish 撤回覆盖，ADR-0051 决策 2）；事件溯源 / 通用 undo 引擎（备选③被否）；agent 改动（撤回全在控制面，agent 零改）。

## 3. 设计（怎么做）

详见 [ADR-0051](../adr/0051-config-operation-undo.md)。要点：

- **数据模型**：`model.ReversibleOperation`（GORM 可移植：枚举落 VARCHAR + 应用层校验、`inverse_payload` 落 TEXT JSON、哨兵软删，无 MySQL 专有 ENUM/SET/JSON）。`status` 闸：`reversible` / `reversed` / `expired` / `superseded`。`inverse_payload` 按 op 类型存反向快照：
  - publish/push：`{itemId, preVersion}`（撤回 = 回滚到 `preVersion`）。
  - fetch：`{taskId, created:[fileId...], updated:[{fileId, preVersion}]}`（撤回 = 软删 created、回滚 updated 到 preVersion）。
- **仓库** `repository.ReversibleOperationRepository`：`WithTx` / `Create` / `FindByID` / `List` / `MarkReversed`（status CAS：`WHERE id=? AND status='reversible'`，`RowsAffected` 判幂等）/ `MarkSuperseded`（同 scope 新操作落地时把旧 `reversible` 记录置 `superseded`）/ `ExpireStale`（清理器）。所有写经 CAS RowsAffected 判命中，串行化并发。
- **服务** `service.ReversibleOperationService`：
  - `RecordPublish/RecordPush/RecordFetch`（在大操作事务内被调用，记账 + 把同 scope 旧 `reversible` 置 `superseded`）。
  - `Undo(id, operator, clientIP)`：单事务内 —— CAS 抢占 `reversible→reversed`（抢不到即幂等返回 / 报 expired/superseded）→ 按 op 类型执行反向回滚（复用 Config/File Rollback 的事务内原语）→ 写 `config.undo-*` 审计。提交后唤醒长轮询。
  - 过期窗口 N 小时从设置 store 读、热生效（新增白名单 key `undo.window-hours`，默认常量 `DefaultUndoWindowHours`）。
- **撤回与既有服务的事务复用**：撤回的回滚必须与 status 翻转、审计同事务。既有 `ConfigService.Rollback` / `FileService.Rollback` 自带事务且事务后唤醒——撤回不能在其事务里再嵌事务。故抽出**事务内的回滚核**（`rollbackInTx`），撤回服务在自己的事务内调用回滚核 + status CAS + 审计，提交后统一唤醒。
- **清理器** `service.ReversibleOperationSweeper`：周期把创建超 N 小时仍 `reversible` 的记录置 `expired` 并清空 `inverse_payload` 瞬态（结构参照 `ReverseFetchTaskSweeper`）。
- **分层**：`router → handler.ReversibleOperationHandler → service → repository`，handler 不碰 GORM。
- **web**：`useOperationLog` 改读真实可逆记录（新增 `listReversibleOperations` 真客户端 fn）+ 审计 / 命令；`OperationLogPanel` 逐条 / 批量撤回与顶部「撤回上一步」接真实 `undoReversibleOperation`。**仅改 `ConfigWorkbenchPage.tsx` 的撤回接线部分**，不重排其它。

## 4. 任务拆分
- [ ] PRD §4 FR-116 计划→开发中
- [ ] `model.ReversibleOperation` + AutoMigrate + 枚举常量 + 审计 action
- [ ] `repository.ReversibleOperationRepository`（CAS / WithTx / List / ExpireStale）
- [ ] `service.ReversibleOperationService`（记账 + Undo 编排 + 幂等 + 过期 / 被覆盖判定）
- [ ] Config/File Rollback 抽出事务内回滚核供撤回复用
- [ ] push/publish/fetch 落地处同事务记可逆账目
- [ ] 撤回端点 handler + 路由（POST undo，requireFullRole，入审计）+ 列表端点
- [ ] 新增 `undo.window-hours` 设置项 + 清理器
- [ ] cmd/main 装配
- [ ] 高风险区单测 + 真 MySQL 集成（`-count` 验非脆）
- [ ] web：useOperationLog 接真 + OperationLogPanel/状态栏撤回接真端点
- [ ] 文档同步：API、ARCHITECTURE、CHANGELOG

## 5. 验收标准
- 清单内 push/publish/fetch 操作可撤回，撤回后真源回到操作前、按需重推（有效 md5 真变才下发）。
- 撤回幂等：重复撤回只生效一次、第二次返回「已撤回」成功。
- 撤回入审计（`config.undo-*`）。
- 过期 / 被覆盖记录拒撤回、返回明确错误。
- 多表写事务原子（status + 回滚 + 审计全成或全不成）、提交成功后才唤醒长轮询。
- 并发安全：撤回 vs 新操作 / 多撤回交错以真 MySQL `-count` 反复跑验非脆。
- `go test ./...` 绿 + web `pnpm test` / `pnpm build` 绿。

## 6. 风险 / 待定
- undo fetch 需要在 ingest 时精确捕获「哪些受管项被新建 / 哪些被覆盖 + 覆盖前版本」。`FileService.Import` 原仅返回计数——本 FR 扩展 `ImportResult` 增加逐项结果（created fileId、updated fileId + preVersion），加法式、不改既有调用方语义。
- 真 MySQL 集成需 `BEACON_TEST_DSN`；无库环境则如实标「集成待真库」，不冒充。
- 真机维度（浏览器撤回逐项活、热推真到在线服）标「待真机验」。
