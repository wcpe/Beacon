# 审批、生命周期与 MCP

Beacon 将高风险管理操作统一交给审批 worker 执行。申请、审批决定、领域写入、审计和执行回执形成可追溯闭环；普通详情页不会以“已提交”代替“已执行”。

## 1. 使用审批中心 {#approvals}

1. 在目标页面填写操作原因，确认影响预览后创建申请。
2. 由具备 human 权限且不是申请人的审批人在「审批中心」查看冻结快照、目标版本和影响范围。
3. 批准后等待 worker 进入 `succeeded`；若为 `failed`，根据执行结果修正条件后重新申请。
4. 撤回只适用于尚未执行的申请。不要通过旧接口、重复提交或直接改库绕过审批。

高风险配置、文件、拓扑改派、身份确认、信任关系、交付、命令和系统设置均遵循此流程。启用排空等明确的止损动作会立即生效并写强审计；取消排空仍需审批。

## 2. 生命周期操作

- 归档：资源转为只读并退出运行面；归档 namespace 会使整个权威子树不可参与调度、注册和数据面写入。
- 恢复：必须创建审批申请。恢复不会自动复活旧运行实例，Agent 需要按正常身份与注册流程重新上线。
- 永久删除：只留下不可复用的墓碑、审计和历史关联；没有撤销或恢复入口。

归档、恢复和永久删除均应先检查影响预览、填写稳定的操作原因，并在审批中心等待执行完成。

## 3. 敏感正文与一次性授权

敏感配置、受管文件、反向抓取冲突、浏览结果和命令正文不会由普通 GET 直接返回。审批 worker 会把一次性授权与冻结的主体、目标、版本或哈希绑定；只有原申请 human 可消费一次。消费后、过期后或目标发生漂移时均失败关闭。

领取内容前确认工单/审批 ID、资源版本和用途。不要把正文粘贴到审计、告警、聊天记录或未受控的工单系统。

## 4. MCP 自动化

内置 MCP 仅用于受控自动化：

- 使用 HTTPS 反向代理和独立 OAuth Client Credentials；代理来源 CIDR 必须显式配置。
- `observer` 只能读取脱敏元数据；`automation` 只能执行已授权的低风险操作，遇到高风险操作只会创建审批申请。
- MCP bearer 不能调用普通管理 REST，普通管理凭据也不能当作 MCP bearer 使用。
- 轮换或撤销 OAuth client 后，旧 token 和 client secret 应立即失效；将凭据存入秘密管理系统，不写入代码、配置模板或日志。

公网 CA、真实域名和外部反代验证属于部署方的上线门；本机自签名联调不能替代该门。

### 4.1 交付域工具（1.5.0 起）

交付域此前是 MCP 覆盖度最低的领域（只有「创建审批申请」的写操作、只读侧为零），现在按风险分三层。**工具名前缀统一为 `beacon.delivery.`**。

**只读六工具**（`low` 风险，`observer` 与 `automation` 均可用）

| 工具 | 用途 |
| --- | --- |
| `order.list` | 变更单列表（**必须**传 `namespaceId`，强制分页） |
| `order.get` | 单张变更单的摘要字段与 `targetCounts` |
| `targets.list` | 目标分页：逐台状态、失败原因、备份标记 |
| `impact.get` | 影响面预览：汇总 + 逐目标差异计数 |
| `observe.get` | 当前批观察窗序列：时间桶、健康分与等级、TPS、告警数 |
| `events.list` | 进度事件：阶段、时间、摘要 |

返回一律是**有界投影**：`order.get` 不返回逐文件清单与批次明细，`targets.list` / `events.list` 强制分页。跨 namespace 一律拒绝（`order.list` 的 `namespaceId` 必填正是为此）。

**直接执行五工具**（不创建审批票据，直接执行并写审计；`observer` **不可见**）

| 工具 | 风险 | 用途 |
| --- | --- | --- |
| `order.create` | `low` | 组单；可一次携带 `configChanges` |
| `order.update` | `low` | 编辑草稿单 |
| `order.diff-scan` | `low` | 重扫文件差异 |
| `order.pause` | `high` | 暂停进行中的单 |
| `order.cancel` | `high` | 紧急终止（`reason` 必填） |

**申请类六工具**（走审批，不改动领域状态直到批准执行）

`order.submit` / `order.resume` / `order.rollback` / `order.delete` / `rollback.finish` / `batch.confirm` 等——它们返回的是**审批票据**，不是执行结果。

**票据现在带影响面**：申请被受理后返回的票据除 `approvalRequestId` / `status` / `operation` 外，还包含 `orderId` 与 `impactSummary{ targetCount, batchCount, payloadFiles, payloadConfigs }`，执行失败时另有截断后的 `failureSummary`。审批视图分轻/全量两档，机器主体仍只能看到自己创建的申请。

**入参收口（易踩）**

- `order.resume` 的 `mode` **只能是** `retry_failed` 或 `skip_failed`，越界在调用领域层之前即被拒。
- `rollback.finish` **不再接受** `reason`——申请原因固定为「结束交付回滚」并且已进冻结 payload，传了会被 schema 拒绝（旧版声明该字段却静默丢弃，容易误以为原因已生效）。
- `order.delete` 使用**独立入参类型**，与 `order.submit` 解耦。

**被拒时**：交付系工具会回**可区分的中文原因**（状态不允许 / 缺模板源 / 无目标 / 缺原因 / 无权），据此决定是等状态、补模板源、改 selector 还是补参数——不要逐个试。

**审计来源**：机器主体经 MCP 调用交付工具时，审计里的来源地址统一记为 **`mcp`**（表达来源类别而非 IP）；人类管理台仍记真实地址。
