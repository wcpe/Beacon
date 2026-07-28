# 功能规格：统一审批中心

> 状态：草拟　·　关联 PRD：FR-212　·　决策：[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)　·　依赖：[dangerous-operation-approval-core.md](dangerous-operation-approval-core.md)

## 1. 背景与目标

危险操作申请分散在身份、拓扑、配置、文件、交付和系统页面，审批人无法看到全局待办，审计人员也无法从申请一路追到执行结果。审批中心提供统一任务入口，但**只消费 FR-207 的审批申请、事件与领域 adapter 读模型**，不在前端、mock 或新表中保存第二份状态真源。

目标是让 human 在一个全局页面完成“找待办 → 比较冻结快照与当前事实 → 批准并执行/拒绝 → 查看执行结果”，并让申请人撤回自己的 pending 申请、审计人员查看永久历史。

## 2. 需求（要什么）

### 2.1 页面职责与范围

- 新增顶层路由 `/approvals`，导航项显示全局 pending 总数；它不是系统设置子页，也不归某个 namespace。
- 页面默认显示全局待办，**不读取、不继承、不发送页眉 env/namespace scope**；页眉在此页显示“全局审批”提示而非可误导的作用域筛选。
- 页面有自己的筛选：状态、operation/领域、风险级别、申请主体类型、申请人、目标 namespace、创建/过期时间；这些筛选只影响审批列表。
- 列表、详情、计数、timeline、快照和 current diff 全部来自统一审批 API；领域页面只提供跳转链接。

### 2.2 列表与详情

- 列表至少展示：申请编号、operation 中文名、风险级别、目标摘要、namespace（无则“全局”）、申请主体类型/名称、原因、状态、创建时间、24 小时剩余/过期时间。
- 详情使用全站既定非模态固定侧面板，展示：
  - 不可变申请快照与资源版本/冻结哈希；
  - 领域 adapter 实时生成的 current facts，以及 snapshot vs current diff；
  - 风险与影响摘要、目标引用、脱敏参数；
  - 申请、批准/拒绝/撤回、worker 认领、重试和执行结果 timeline；
  - 关联领域详情与审计查询链接。
- 内容、token、secret、敏感配置值、文件明文和消息 payload 不进入列表、快照、diff 或 timeline。

### 2.3 操作闭环

- human 在 pending 详情可“批准并执行”或“拒绝”；批准动作只调用统一 `approve`，服务端原子记录批准与执行任务，页面不能再调用领域 execute/start。
- 申请人仅可在 pending 撤回自己的申请；拒绝原因必填，撤回原因可选。过期、拒绝、撤回和失败均终态，只能按当前事实重提。
- 批准前 snapshot/current 证据加载失败、资源已明确漂移或申请已过期时禁用按钮并展示原因；并发决策以服务端 CAS 为准。
- executing 页面自动刷新/订阅统一状态；succeeded/failed 均展示领域结果和审计链接。批准成功但执行失败不得显示为“未审批”。
- machine principal 没有 approve/reject 能力；即使调用 API 也由服务层拒绝，页面隐藏按钮仅是体验优化。

## 3. 设计（怎么做）

### 3.1 API 与状态真源

审批中心只使用 FR-207 的管理面：

| 方法 | 路径 | 用途 |
|---|---|---|
| POST | `/admin/v2/approval-requests` | 按已登记 operation adapter 冻结合规申请；未知 operation fail-closed。审批中心页面本身不构造任意 payload |
| GET | `/admin/v2/approval-requests` | 服务端筛选/排序/分页；`status=pending&pageSize=1` 的 total 供导航徽标 |
| GET | `/admin/v2/approval-requests/{id}` | 申请、脱敏快照、current diff、timeline 与领域链接 |
| POST | `/admin/v2/approval-requests/{id}/approve` | human 批准并自动执行 |
| POST | `/admin/v2/approval-requests/{id}/reject` | human 拒绝，原因必填 |
| POST | `/admin/v2/approval-requests/{id}/withdraw` | 申请人撤回 pending，原因可选 |

- POST 创建只允许调用已登记 adapter 完成规范化、脱敏、前置校验与冻结；领域原端点可作为兼容申请入口。审批中心 UI 不提供“手填 operation/payload”表单，也不能绕过 adapter。
- 前端不推导状态、不合并领域状态来“修正”审批状态；current diff 是 adapter 只读投影，不是审批记录。
- 对 Agent 命令、ChangeOrder、升级等长任务，审批 `succeeded` 表示领域动作已持久受理；页面继续通过 `resultRef` 展示领域当前状态，不把领域失败回写成另一份审批状态。
- mock handler 仅模拟同一契约并覆盖四态，不能在生产构建注册或成为实现依据。

### 3.2 并发、刷新与可访问性

- 列表使用服务端分页、稳定排序（pending 先按过期时间，其余按创建时间倒序），筛选变化重置游标/页码。
- 批准/拒绝/撤回提交期间按钮互斥；`409 approval_state_changed` 后刷新服务端详情，不覆盖另一操作者结果。
- executing 采用有界轮询或既有 SSE 能力；页面卸载即停止，控制面重启不影响数据库任务恢复。
- 所有状态、风险和 diff 不只靠颜色表达；关键按钮有明确文本、键盘焦点和确认对话框。

## 4. UX / 交互

- 用户任务：
  - 审批人快速找到即将过期或高风险待办，理解影响后批准并执行或拒绝；
  - 申请人查看处理进度并在 pending 撤回；
  - 审计人员按主体/operation/时间追溯申请到执行的完整证据。
- 进入路径：顶层侧栏“审批” → `/approvals`；领域提交成功 toast/状态卡、通知和审计记录可深链 `/approvals/{id}`。
- 操作闭环：筛选待办 → 选中行打开固定侧面板 → 对照“申请时快照 / 当前事实” → 填写决策原因 → 批准并执行/拒绝 → 原位跟踪 executing → 查看最终领域结果与审计。
- IA 挂载：顶层任务型页面，与“总览”及四大域并列；不隶属当前 env/namespace，页面职责与导航同步写入 `docs/UX.md`。

### 4.1 页面状态

- 空态：无待办时区分“当前筛选无结果”和“全局暂无待审批”；提供清筛选，不伪造示例数据。
- 加载态：列表骨架与详情骨架分离；快照/current diff 未齐时审批按钮不可用。
- 错误态：保留当前筛选与选中 ID，展示脱敏错误和重试；不得回退到旧缓存后允许审批。
- 超大数据量：服务端分页/筛选，列表区独立滚动、吸底分页；长 snapshot/diff 分段懒渲染，目标明细分页，不一次性渲染 1000+ 行。
- 常规态：主从布局保持列表上下文，详情侧面板不遮罩、不顶动内容；状态变更后就地更新并保留 timeline。

### 4.2 mockup 评审门

- 先在演示模式实现可点击 mock，仅接 mock API，不接真实审批后端。
- mock 场景必须可切换：空态、常规待办、超大量态、异常；常规态再覆盖 pending、executing、succeeded、failed 与 snapshot 漂移。
- 用户通过浏览器评审确认导航位置、主从布局、快照/current diff 表达和批准并执行闭环后，才允许接真实后端；未拍板不得以单测或截图代替。

## 5. 任务拆分

- [ ] 测试先红：路由/导航、全局 pending 徽标、独立筛选、不读取页眉 scope、详情 deep-link。
- [ ] 测试先红：批准只调用一次统一 approve，页面不调用领域 start；reject/withdraw 原因与权限；并发 409 刷新。
- [ ] 先做四态可切换 mock 页面并完成用户浏览器评审留档。
- [ ] 评审通过后最小接入统一审批 API、contracts、TanStack Query 与 i18n；不建立本地审批 store。
- [ ] 补 executing 刷新、timeline、分页目标明细、脱敏与键盘可访问性测试。
- [ ] 独立复核：核对页面每个状态/动作都来自统一申请，审批路由未注入 env/namespace scope，生产构建未注册 mock。
- [ ] 文档同步：PRD 状态、UX 导航/页面职责/全局 scope 排除、ARCHITECTURE 前端数据流、API、CHANGELOG。

## 6. 验收标准

1. `/approvals` 可查看全局 pending 与永久历史，导航徽标数量不随页眉 env/namespace 改变。
2. 列表可独立按状态/领域/风险/主体/namespace/时间服务端筛选，1000+ 记录下分页稳定。
3. 详情能比较不可变快照与当前事实，并展示从申请到最终执行的同源 timeline；敏感明文不泄露。
4. human 一次“批准并执行”后由服务端自动执行；前端不调用任一领域 start/execute。
5. machine principal 服务层无法批准；申请人仅能撤回自己的 pending；并发决策不会互相覆盖。
6. 四态 mockup 经用户浏览器评审拍板后才接真；前端测试覆盖空/加载/错误/超大与关键终态。
7. 独立复核确认没有第二状态真源、没有页眉 scope 干扰、没有生产 mock 泄漏。

## 7. 风险 / 待定

- 不同领域 current diff 形状不同，统一页面只负责通用框架，具体 diff 由 adapter 返回类型化区块；禁止把领域判断塞进审批中心上帝组件。
- 执行可能跨控制面重启，页面必须相信持久状态而非一次 HTTP 响应；轮询/SSE 只负责刷新展示。
- 全局待办可能包含无 namespace 的系统操作，独立 namespace 筛选必须保留“全局操作”选项，不能误归到当前环境。
