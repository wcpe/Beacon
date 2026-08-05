# 功能规格：控制面升级、回滚与运行设置审批

> 状态：开发中　·　关联 PRD：FR-210　·　决策：[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1. 背景与目标

控制面升级、手动回滚和关键运行设置修改都能直接改变整个 Beacon 控制面的可用性或行为。现有端点只做 `full/readonly` 判权、二次确认和审计，机器主体一旦持有 `full` 凭据便可直接执行，且审批证据无法在进程重启后恢复。

本功能把这些强危险动作接入 FR-207 的统一审批真源：领域原端点只冻结意图并创建申请，human 在审批中心“批准并执行”后，由持久 worker 携内部 `ExecutionPermit` 调用既有领域 service。升级与回滚继续复用当前**单二进制自替换 + 自动回退**方案，不恢复 launcher、不增加第二进程。

## 2. 需求（要什么）

### 2.1 强危险动作

| operation | 领域入口 | 申请冻结事实 | 执行完成判据 |
|---|---|---|---|
| `system.update.apply` | `POST /admin/v1/system/update` | 当前版本、目标 GA 版本、平台资产名、SHA-256、检查结果版本及申请原因 | 持久创建系统执行记录与审批 receipt；最终换版结果由 `resultRef` 指向的领域记录承载 |
| `system.update.rollback` | `POST /admin/v1/system/rollback` | 当前版本、`.old` 备份版本/哈希、回滚可用性及申请原因 | 持久创建系统执行记录与审批 receipt；最终回退结果由 `resultRef` 指向的领域记录承载 |
| `settings.update.dangerous` | `PUT /admin/v1/settings/{key}` 中登记为强危险的 key | key、类型、旧值、新值、当前 setting version、影响说明及申请原因 | CAS 写入并触发现有热生效消费者成功 |

- 强危险 setting 由统一 operation descriptor 元数据登记，不允许 handler 或页面各自判断；至少覆盖会改变健康判定/离线窗口、采样与数据保留、归档边界、长轮询、外部 webhook、更新通道或代理等全局运行行为的 key。
- `log.level` 等未被登记为强危险的低风险设置仍可 direct，但必须保留 capability 校验、原因和现有审计；新增 setting 未分类时运行时 fail-closed，覆盖测试失败。
- 更新检查、更新状态、系统状态和代理连通诊断是只读 direct 操作，不创建审批。

### 2.2 止损动作直接执行

- 取消尚在下载/校验阶段的升级仍由 `POST /admin/v1/system/update/cancel` 直接执行；它只能缩小影响，不得开始替换或改变目标版本。
- 暂停、取消、撤回等仅降低风险的同类动作遵循 ADR-0079 的 `direct + 强审计` 规则；恢复、重新启动、重新授权或扩大影响必须另行创建审批。
- direct 止损动作仍要求语义 capability、原因（动作已有原因字段时）与专项审计，不以“无需审批”解释为无需授权。

### 2.3 申请与兼容语义

- 领域原端点保持原路径，但命中 `approval_required` 时只校验输入、冻结快照并返回 `202`：`approvalRequestId`、`status=pending`、`expiresAt`；不得下载、替换二进制或修改 setting。
- 申请原因必填；缺失返回明确的 `approval_reason_required`。旧客户端仍可调用原路径，但不再获得“立即执行”语义。
- 申请沿用审批核心的 `Idempotency-Key`：同主体、同 operation、同 key 且冻结 hash 相同返回原申请；同 key 但 hash 不同返回 409。不同申请在执行前仍由资源 version/哈希守卫裁决。
- 24 小时过期、仅申请人可在 pending 撤回、只有 human 可批准，以及 machine principal 服务层拒绝批准，均直接复用 FR-207，不在本领域复制状态机。

## 3. 设计（怎么做）

### 3.1 领域 adapter

- `SystemOperationApprovalAdapter` 只负责规范化输入、生成脱敏快照/冻结哈希、执行前重校验并调用现有 update/settings service；它不保存另一份审批状态。
- update、rollback 与危险 settings service 的产生副作用入口必须接收不可由 HTTP/MCP 构造的 `ExecutionPermit`。只做检查、状态读取和取消下载的入口不需要 permit。
- adapter 不回调 HTTP 路由；审批 worker 直接调用 adapter，避免再次创建申请或绕过服务层守卫。

### 3.2 漂移与 fail-closed

- 执行升级前重新检查当前版本、目标 Release、平台资产与 SHA-256；任一事实与快照不同即将申请置 `failed`，不得自动改用“最新版本”。
- 执行回滚前重新校验当前版本与 `.old` 备份哈希；备份不存在或已变化即失败，不尝试猜测其它历史版本。
- 执行 setting 前按冻结的 setting version 做 CAS；当前值/version 已变化即失败，申请人须按新值重提。
- 审批记录、执行任务或审计证据无法持久化时不得调用领域 service；错误按既有脱敏规则展示。

### 3.3 单二进制重启对账

- 升级/回滚 adapter 在触发现有异步流程前，事务内创建最小持久系统执行记录、领域强审计与审批 receipt；提交后才唤醒现有 update service。审批申请随后按 FR-207 收敛为 `succeeded`，表示“危险动作已被持久接受执行”，`resultRef` 指向该系统执行记录。
- 不能把 update service 的内存 `202 accepted` 或一次 HTTP 成功当作最终换版成功。系统执行记录至少保存 operation nonce、冻结目标、`pending/running/succeeded/failed` 与脱敏错误。
- 新进程启动后使用既有 update sentinel、运行版本和 nonce 对账领域记录：稳定验证成功后置领域 `succeeded`；自动回退、哈希不符、启动失败达到阈值或对账超时置领域 `failed`。审批事实不倒退。
- 对账可在控制面重启后继续；只补持久执行事实，继续复用现有单二进制流程，不引入 launcher、MQ、Redis 或额外常驻进程。

## 4. UX / 交互

- 用户任务：运维管理员在现有系统版本页或设置页提交危险变更，随后到审批中心查看快照并批准执行。
- 进入路径：`/system/version` 的升级/回滚入口、`/settings` 的危险 setting 保存入口；审批处理统一跳转 `/approvals/{approvalRequestId}`。
- 操作闭环：填写原因 → 原页面收到“已提交审批”及申请编号 → 审批中心比较快照与当前事实 → 批准并执行 → 审批详情经 `resultRef` 展示系统执行进度与最终结果；取消下载仍在版本页直接执行并显示审计编号。
- IA 挂载：不新增系统子页；审批任务统一归顶层 `/approvals`，页面细节见 [approval-center.md](approval-center.md)。

页面四态：

- **空态**：无可用升级、无可用回滚备份或无危险设置变更时说明原因，不展示可提交的伪动作。
- **加载态**：版本、备份、setting 当前事实与影响预览分别显示骨架；任一权威事实未完成时提交按钮禁用。
- **错误态**：影响预览、申请、执行或重启对账失败显示脱敏原因、traceId 与重试/重提入口；不得用成功 toast 掩盖后台未完成。
- **超大量态**：setting 列表服务端筛选/分页，历史系统执行和审批记录跳统一分页列表；页面不一次加载全部设置或执行历史。
- **常规态**：原页面显示 pending/executing/succeeded/failed 与领域执行状态，申请编号可深链；取消下载等止损动作仍原地完成并展示审计编号。

升级/回滚与危险 setting 从“直接执行”改为“提交审批 → 批准并自动执行”，属于主流程变化。实现前必须在演示模式提供可点击 mock，覆盖上述四态、批准后重启对账与止损直执；经用户浏览器评审拍板后才能接真实审批 API。

## 5. 任务拆分

已实现：冻结 GA 资产与 SHA-256、`.old` 备份 manifest、危险设置元数据与版本 CAS、统一审批事务 receipt、最小系统执行记录及启动/稳定成功和失败对账。页面 mockup、完整浏览器验收和独立复核仍未完成。

- [ ] 测试先红：operation 分类覆盖、机器主体无法批准、原端点只建申请且无副作用、内部 permit 无法由 handler 构造。
- [ ] 测试先红：升级目标/哈希漂移、回滚备份漂移、setting CAS 冲突、审批/审计持久化失败均 fail-closed。
- [ ] 最小实现系统领域 adapter，复用现有 update/settings service 与单二进制 sentinel，不复制审批表或执行器。
- [ ] 增加最小持久系统执行记录，接通新进程启动对账与重启恢复，验证成功/自动回退/失败三条路径。
- [ ] 先完成系统危险动作四态可点击 mockup 与浏览器评审，再把原入口接为提交申请并跳审批详情；取消下载保持直接止损。
- [ ] 独立复核：检查所有 V1/V2 同义系统入口与内部调用均经过同一 operation descriptor 和 permit 守卫。
- [ ] 文档同步：PRD 状态、ARCHITECTURE 的审批 adapter/重启对账、UX 的入口闭环、API 的 `202` 契约、CHANGELOG。

## 6. 验收标准

1. human、API key、MCP 调升级/回滚/危险 setting 原端点都只能创建 pending 申请，申请创建前后业务状态完全相同。
2. API key、MCP、system 即使伪造批准请求也在服务层被拒；human 批准后无需第二次“开始”操作。
3. 升级只执行快照中的确定版本与 SHA-256；Release 漂移后执行失败，不跟随最新版本。
4. 回滚只消费冻结的 `.old`；setting 只在 version 未漂移时 CAS 成功。
5. 批准事务持久创建领域执行记录与 receipt，审批 request 收敛为 succeeded 并返回 resultRef；控制面重启后按该领域记录继续对账，稳定新版本、自动回退和启动失败结果均可追溯。
6. 更新取消直接生效并有强审计，且不能触发二进制替换；恢复/重试必须重提审批。
7. 四态 mockup 经用户浏览器评审拍板后才接真；相关 Go/前端测试先红后绿，独立复核确认没有 launcher、第二审批状态机、内部 HTTP 回调或未分类危险入口。

## 7. 风险 / 待定

- 当前部分 setting 的风险元数据尚未集中，实施时必须先建立最小白名单并由覆盖测试锁定；不得用“所有 PUT”替代语义分类。
- 自替换会中断原 worker 进程；审批 succeeded 只表示已持久受理，最终换版成功必须读取 resultRef 对应的领域执行记录，未完成对账不能显示为“升级成功”。
- 升级检查依赖外部 Release 服务，但审批执行仍只消费冻结资产身份；外部不可用时失败终止，不回退为任意可下载版本。
