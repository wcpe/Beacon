# 功能规格：agent 版本/构建可见性

> 状态：开发中　·　关联 PRD：FR-86（增强 FR-34/FR-52）　·　ADR：[ADR-0039](../adr/0039-agent-self-reported-version.md)　·　分支：feature/fr-86-agent-version

## 1. 背景与目标
运维盲区：一台服可能装了**过期的 agent jar**，但管理台对此完全不可见。既有 `version` 是运维手填的**业务版本**（发现过滤标签 FR-29），不是「这台跑哪个 Beacon agent 构建」。结果是升级 agent 漏装一台、或回滚后忘换 jar，运维只能上机看 jar 名。

本需求让 agent **注册时自报自身构建版本**，控制面只读暴露、管理台逐台展示，并在**同一环境内 agent 版本不一致时打黄标**，治「agent 跑哪个构建运维不可见」的盲区。

## 2. 需求（要什么）
- agent 注册 payload 新增 `agentVersion`：agent **自身构建版本**（自动取，非手填），与业务 `version` 并列、语义不同。
- 控制面注册端点解析 `agentVersion`，存入内存注册表 `Instance`；`InstanceView` 暴露 `agentVersion`。
- 旧 agent 不报 → 字段空（向后兼容）；管理台显「未知」。
- 管理台服务器页表格 + 单服详情展示 agent 版本；同一环境内 agent 版本去重后多于一个时，对版本与「该环境多数版本」不同的行 / 详情打黄标。
- 范围内：agent-core（`AgentIdentity` + `BeaconApiClient.register` + `AgentBootstrap`）、双端壳层注入构建版本、控制面（注册解析 + `Instance` + `InstanceView`）、前端（`types.ts` + ServersPage + ServerDetailSheet + i18n）、`docs/API.md`、`CHANGELOG.md`。
- 不做（范围外）：
  - 不落 DB（纯内存事实，随注册刷新）。
  - 不进调度 / 健康 / 覆盖链任何决策。
  - 心跳 / report 不带 agentVersion（沿用注册时值，agent 重启即重注册刷新）。
  - 后端不返回「集群是否不一致」标志位（前端展示派生）。
  - 不改 agent 构建脚本（版本经 TabooLib 运行期 `pluginVersion` 取，VERSION→Gradle→manifest 链已有）。

## 3. 设计（怎么做）

### 3.1 agent（Kotlin）
- `AgentIdentity` 新增 `agentVersion: String = ""`（默认空 → 既有构造点 / 测试不破）；置于 `version` 之后，注释区分「业务版本 vs agent 构建版本」。
- `BeaconApiClient.register`：`agentVersion` **仅非空时**拼入报文键（与 `backends` 同范式，向后兼容旧控制面 / 旧 agent）。
- `AgentBootstrap.readIdentity(reader, role, agentVersion)`：新增 `agentVersion` 形参（默认 `""`），写入 `AgentIdentity.agentVersion`。**不从 config.yml 读**——构建版本由壳层注入，非运维可配。
- 壳层 `BeaconAgentBukkit` / `BeaconAgentBungee`：ENABLE 时经 `taboolib.common.platform.function.pluginVersion` 取插件版本，传入 `readIdentity`。core 不碰 TabooLib（守 ADR-0005 / architecture-invariants：取版本是内存读，不阻塞主线程）。

### 3.2 控制面（Go）
- `internal/runtime/registry.go`：`Instance` 增 `AgentVersion string`；`clone` 为值类型字段，浅拷贝随结构体复制即可（无需额外处理）。
- `internal/handler/agent_handler.go`：`registerRequest` 增 `AgentVersion string \`json:"agentVersion"\``（缺键 → 空串，向后兼容）；透传进 `RegisterParams`。
- `internal/service/instance_service.go`：`RegisterParams` 增 `AgentVersion`；`Register` 写入 `Instance.AgentVersion`。
- `internal/handler/instance_handler.go`：`instanceView` 增 `AgentVersion string \`json:"agentVersion"\``；`toInstanceView` 填 `i.AgentVersion`。

### 3.3 前端（React/TS）
- `web/src/api/types.ts`：`InstanceView` 增 `agentVersion: string`。
- `web/src/pages/ServersPage.tsx`：
  - 表格新增「agent 版本」列：显 `agentVersion`（空显「未知」）；按环境派生多数版本，行版本非空且与该环境多数版本不同 → 黄标徽标。
  - 多数版本计算抽为纯函数 `lib/agentVersionConsistency.ts`（按 namespace 聚合 → 每环境非空版本计数 → 取众数），穷举单测。
- `web/src/pages/servers/ServerDetailSheet.tsx`：公共标识区新增「agent 版本」字段，不一致时同样黄标。
- `web/src/i18n/locales/zh-CN.ts`：`servers.colAgentVersion`（agent 版本）、`servers.agentVersionUnknown`（未知）、`servers.agentVersionMismatch`（版本不一致悬浮提示）。

## 4. 验收
- agent-core 单测：register 携带非空 `agentVersion` 拼键；空时不拼键（向后兼容）。
- 控制面 `go build ./... && go test ./...` 绿。
- agent `:agent-core:build` 绿。
- 前端 `pnpm test`（含 `agentVersionConsistency` 纯函数单测）+ `pnpm build` 绿。
- 真机（双端 jar 重建部署后）：服务器页显真实 agent 版本；故意装一台旧 jar → 该行黄标提示版本不一致。标「待真机验」。

## 5. 不变量对齐
- ADR-0005：agent core 不依赖具体 HTTP/JSON 库、不依赖 TabooLib——版本由壳层注入。
- architecture-invariants §3：注册/健康真源是 Go 进程内存——agent 版本只存内存、不落 DB。
- ADR-0039：本规格的决策依据。
