# 规格：人类与机器主体及语义能力授权（FR-206）

> 状态：草拟 · 关联 PRD：FR-206 · 决策：[ADR-0079](../adr/0079-principal-capability-and-dangerous-operation-approval.md)

## 1. 背景与目标

管理面当前把登录操作者视为恒定 `full`，API key 只有 `full/readonly` 两级，并主要按 HTTP 方法判断写权限。该模型无法表达有副作用的 GET、同一路由内由 body 决定风险的分支、后台任务，也无法保证未来 MCP 或现有 API key 不能审批。

本规格建立统一且不可伪造的 `Principal`、语义 `Capability` 与 `OperationDescriptor`。它只负责“谁以什么身份请求哪项语义操作、是否有权进入 direct 或申请流程”；审批状态机与执行许可唯一归 [dangerous-operation-approval-core.md](dangerous-operation-approval-core.md)（FR-207）。

## 2. 范围

### 2.1 范围内

- 登录令牌、API key、未来 MCP OAuth client 与内部任务统一映射为 `Principal`。
- 旧 `full/readonly` 继续兼容，但只作为 capability bundle，不再作为最终授权模型。
- 所有管理面 REST 路由、未来 MCP tool、后台任务和危险 service 入口登记语义 operation。
- 服务层统一授权；handler、按钮隐藏和 HTTP method guard 只能做前置优化，不能成为安全真源。
- 审计以 additive 字段记录主体类型与稳定主体 ID，兼容既有 `operator` 字符串。

### 2.2 不做

- 不做可由用户编辑的 RBAC/ACL、角色继承、组织架构或按 namespace 的后台授权。
- 不新增 capability 数据库表；本期 capability 与 bundle 均为代码常量，避免动态权限系统。
- 不实现 MCP 传输、OAuth client 或 MCP tool；本规格只预留 `mcp` 主体契约。
- 不复制审批状态机，不允许本规格签发 `ExecutionPermit`。

## 3. 统一主体模型

### 3.1 `Principal`

认证中间件成功后必须向 context 注入不可由请求体、查询参数或 header 覆盖的值对象：

| 字段 | 约束 | 说明 |
|---|---|---|
| `kind` | `human` / `api_key` / `mcp` / `system` | 主体类别 |
| `id` | 非空稳定字符串 | human=配置用户名；api_key=数据库行 ID；mcp=OAuth client ID；system=登记策略名 |
| `displayName` | 非授权字段 | 仅 UI 与审计快照展示，改名不改变主体身份 |
| `credentialRef` | 可空、只存稳定引用 | 不得包含 token、key 前缀之外的明文或 secret |
| `capabilities` | 去重集合 | 由可信认证适配器与 bundle 生成，客户端不得提交 |
| `authMethod` | `login_token` / `api_key` / `oauth_client` / `internal_policy` | 审计用，不参与授权 |

规范化审计引用固定为 `<kind>:<id>`。既有 service 暂时仍需 `operator string` 时，只能由统一适配器投影，禁止 handler 从请求中读取 operator。

### 3.2 主体构造边界

- `human`：仅由现有登录令牌校验成功后构造；登录令牌载荷中的用户名作为稳定 ID。
- `api_key`：仅由数据库中未吊销、未过期且哈希匹配的行构造；名称仅作 displayName，稳定 ID 使用行 ID。
- `mcp`：留给后续 OAuth 规格实现；任何当前 REST 请求不得通过自报字段构造 `mcp`。
- `system`：只能由进程内 `SystemPolicyRegistry` 根据已登记策略构造，不能通过 HTTP/MCP 认证产生。
- 同一请求只允许一个 Principal；多个认证头、认证类型冲突时返回 400，不按“权限更高者”静默选择。

## 4. Capability 与兼容 bundle

Capability 为小写点分语义字符串。基础能力由本规格定义，领域能力由 FR-208/209 及后续领域规格登记：

| Capability | 语义 |
|---|---|
| `management.read` | 读取已脱敏的普通管理事实 |
| `management.direct` | 调用已明确分类为 direct 的管理动作 |
| `approval.request` | 提交危险操作申请 |
| `approval.read` | 查询审批记录；领域过滤仍由 service 强制 |
| `approval.withdraw.own` | 撤回本人/本机器主体尚处 pending 的申请 |
| `approval.decide` | 批准或拒绝；仅 human 可拥有 |

兼容映射固定如下：

| 来源 | Capability bundle | 硬限制 |
|---|---|---|
| human 登录令牌 | 当前全部管理能力 + `approval.*` | 允许提交、拒绝和自批 |
| readonly API key | `management.read`、读取自己的审批记录 | 不可提交、决定或执行 direct 写动作 |
| full API key | 当前全部非审批管理能力 + `approval.request/read/withdraw.own` | 服务层永远没有 `approval.decide` |
| MCP observer/automation | 后续 MCP 规格映射 | 无论 bundle 名称为何，永远没有 `approval.decide` |
| system policy | 仅该策略声明的低风险 `system_exempt` operation | 不可申请、批准、拒绝或构造 `ExecutionPermit`；不得调用危险目录 |

`api_key` 表的 `role` 字段与现有创建/吊销/过期语义保留。实现不得把 `full` 直接解释为“允许所有 operation”；必须先映射 bundle，再经过 operation 授权。

## 5. OperationDescriptor 与分类完整性

### 5.1 描述符

每个可调用管理操作必须登记静态描述符：

```text
OperationDescriptor
  key                  稳定点分语义键
  schemaVersion        冻结参数格式版本
  requiredCapability   所需能力
  classification       direct | approval_required | system_exempt
  riskLevel            low | high | critical
  classify(input)      可选的服务端 body/query/当前事实分支分类器
  adapterKey            approval_required 时对应的冻结/执行适配器
  systemPolicyKey       system_exempt 时允许的唯一系统策略
```

- HTTP method、路由名、前端按钮文字和 MCP tool 名都不是 operation key。
- V1/V2 同义入口必须解析到同一个 operation key 与同一个 service，不得因旧入口绕过审批。
- 同一路由若由 body/query 或数据库事实决定风险，`classify` 必须在可信服务端规范化后选择 operation；客户端不能提交 classification。
- 有副作用的 GET 必须显式登记。若 GET 对应 `approval_required`，GET 本身不得创建申请；返回 `409 operation_requires_approval` 并指向 FR-207 的申请端点。
- `system_exempt` 必须是 `riskLevel=low`、无 approval adapter 的内部维护 operation，不能与危险目录或 `approval_required` 同义 operation 映射。
- 后台任务只能执行上述低风险 `system_exempt`，或消费已由 human 批准的持久领域任务；没有 descriptor/policy evidence 时不执行。

### 5.2 完整性与 fail-closed

- 路由装配完成后校验 `/admin/v1`、`/admin/v2` 每个 RoutePattern 均有 descriptor；缺失时启动失败并输出中文错误，但不得输出请求数据。
- 测试遍历 chi 路由、MCP tool 注册表与后台 policy 注册表，确保无未分类入口、无重复 operation key、无 V1/V2 分类漂移。
- service 授权器查不到 descriptor、主体或 capability 时一律拒绝，错误码 `operation_not_classified` / `principal_missing` / `capability_denied`；不得回退到 readonlyWriteGuard。
- `readonlyWriteGuard` 与 `requireFullRole` 在迁移窗口保留为早拒优化；它们通过不能证明 service 已授权。

## 6. 服务层授权接口

建议最小接口形态如下，具体包名由实现按现有结构放置：

```text
Authorize(principal, operationKey, normalizedInput) -> AuthorizationDecision
```

`AuthorizationDecision` 只能是：

- `DirectAllowed`：调用 direct service；高风险 direct 必须使用同事务强审计。
- `ApprovalRequired`：转 FR-207 创建冻结申请，当前调用不得产生领域副作用。
- `SystemAllowed`：仅低风险内部维护 policy 调用，携带不可外部构造、且与 `ExecutionPermit` 完全分型的 policy evidence。

`SystemAllowed` 不能调用任何接收 `ExecutionPermit` 的 service；system principal、policy registry 和普通后台任务均无 permit 构造入口。危险 operation 无论由 REST、MCP 还是后台触发，都只能先创建申请并由 human 批准。

任何 `Principal.kind != human` 调用 approve/reject service 时，必须先按主体类型返回 403 `machine_principal_cannot_decide`，再谈 capability。该不变量不得只写在路由或 UI。

## 7. API 与审计兼容

### 7.1 当前主体端点

新增只读端点：

| 方法 | 路径 | 响应 |
|---|---|---|
| GET | `/admin/v2/auth/principal` | `{kind,id,displayName,authMethod,capabilities[]}`；不返回 token、hash、secret、credentialRef |

现有 `/admin/v1/auth/login`、API key 两种认证头与 401/403 语义保持兼容。新端点只帮助 UI 判断可见动作，后端仍逐次授权。

### 7.2 审计 additive 字段

`audit_log` additive 增加可空 `principal_type`、`principal_id`、`approval_request_id`。新写入必须填前两项；历史行保持原样，查询视图在字段为空时返回 `kind=legacy,id=operator`，不做有歧义的数据回填。既有 `operator` 字段继续写规范化审计引用，保证旧查询与导出兼容。

任何日志和审计均不得记录认证 header、登录令牌、API key 明文/哈希、OAuth secret 或完整 Principal context。

## 8. 事务、并发与安全不变量

- Principal 与 descriptor 是只读值对象；单请求内不可变，不放入全局可变状态。
- API key 吊销后下一次认证立即失效；已进入执行器的审批任务仍按冻结申请和 permit 校验，不借旧请求 context 续权。
- 授权只决定“可 direct/可申请/可 system”；领域版本、namespace 隔离、排空、冲突等守卫仍由领域 service 在执行时校验。
- namespace trust 的 `capability` 是跨 namespace 业务能力，和本规格的管理面 Capability 是两个命名空间，禁止混表或互相隐式映射。
- 所有外部字符串先做长度、枚举和 UTF-8 校验；拒绝原因脱敏，对外错误保持 `{code,message,traceId}`。

## 9. 实施任务与测试纪律

1. 先写失败测试：四类 Principal 构造、full/readonly bundle、machine 决策硬拒、缺 descriptor fail-closed、V1/V2 同义入口一致、有副作用 GET 与 body 分支。
2. 最小实现 Principal/context、bundle、descriptor registry、service authorizer 与 `/auth/principal`；不引动态 RBAC。
3. 为 chi 路由、后台 policy 及未来 MCP registry 建完整性守护测试；新增入口漏登记必须先红。
4. additive 扩展审计字段与兼容视图，测试日志/审计不含凭据。
5. 完成后由未参与实现的代理独立复核：重点尝试 full API key 伪造 approve、V1 绕过 V2、GET 副作用、system_exempt 映射危险 operation 或构造 permit；发现问题先补红测再修。

## 10. 验收标准

1. human、api_key、预留 mcp、system 均能形成稳定 Principal；请求字段不能覆盖 kind/id/capabilities。
2. full API key 可提交其有权限的危险申请，却无论经 REST、service 直调还是伪造 capability 都不能 approve/reject。
3. 旧 readonly API key 读请求保持兼容，任何 direct 写或申请被 403；full/readonly 不再是 service 的最终判断。
4. `/admin/v1`、`/admin/v2` 路由覆盖测试无遗漏；删掉任一 descriptor 测试立即失败，运行时未知 operation fail-closed。
5. 至少一个 GET 副作用、一个 body 分支、一个低风险 system_exempt 与一个已批准危险后台任务测试证明分类按语义而非 method。
6. system principal 无法调用任一 approval_required operation、无法把 system_exempt 映射到危险目录，也无法构造或复用 ExecutionPermit。
7. 新审计包含 principalType/principalId，旧审计仍可查询导出；任何输出不含凭据。
8. `go test ./...`、相关 integration 测试、前端 principal 消费单测与 build 全绿；独立安全复核无未处理高风险发现。

## 11. 风险与固定边界

- 本期不开放 capability 配置 UI；若未来需要多用户/自定义 RBAC，必须另立 FR 与 ADR，不得把静态 bundle 偷换成可编辑权限表。
- human 允许自批是已冻结产品决策，不实现职责分离开关。
- MCP 本期只占用主体枚举，未安装/未实现 MCP 时不注册任何 MCP 认证入口。
