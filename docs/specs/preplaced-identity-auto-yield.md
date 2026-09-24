# 控制面预置身份在审批时自动让位（FR-235）

> 状态：开发中（实现完成，待真机验收）　·　关联 PRD：FR-235（增强 FR-222 / FR-141 / FR-139）　·　相关：[internal-trust-channel](internal-trust-channel.md)、[v2-agent-identity](v2-agent-identity.md)

## 1. 背景

CP 推送（FR-222 机器注册通道）的设计意图是让外部管理平台（如 JianManager）批量建实例后**免人工审批**完成接入。但 2026-09-24 真机搭建时 11 台实例**全部**遇到 `server_id_occupied`，靠逐台 `forceUnbindOccupier` 才完成接入——恰好是 FR-222 立项要消灭的那个痛点。

## 2. 现象与机理

### 2.1 真机实证（2026-09-24，11 台同构）

```
server_id            status   boot_id  binding_source   created_at
region1-zone1-lobby  active   非空      admin_assigned   11:08   ← 插件（真身份）
region1-zone1-lobby  unbound  空        admin_assigned   10:08   ← CP 推送（空壳）
```

时间线：**10:08** CP 推送 → 控制面替该服预建身份（`boot_id` 空）；**11:08** 插件启动注册 → 撞上该行 → 需人工解绑。

**11 台无一例外**，`boot_id` 空/非空与身份真伪完全分野。

### 2.2 为什么 CP 推送会造出「空壳」

插件尚未启动时，控制面无从得知插件的真实 `identityId`（FR-139 规定身份由 agent 首启生成），只能**凭空生成**一个占位身份并直接置为 active + 绑定 serverId（[createMachineRegisterIdentity](../../apps/server/internal/service/agent_machine_register.go)）。该路径**从不写 `boot_id`**——因为 `bootId` 是插件进程级随机值，控制面不可能预先知道。

于是「`boot_id` 为空」成为**预置空壳的固有特征**：真 agent 身份在注册入口强制校验 `bootId` 非空（[RegisterAgentV2](../../apps/server/internal/service/v2_control_plane_service.go)），故任何「被真 agent 用过」的身份必然带 `bootId`。

### 2.3 问题出在审批一视同仁

[resolveOccupierForApprove](../../apps/server/internal/service/v2_control_plane_service.go) 原本对任何占用者都要求 `forceUnbindOccupier`。但这里的占用者是**控制面自己造的壳**，不是「他人的真身份」——真 agent 到来时理应接管，却被迫走人工流程。

## 3. 设计

### 3.1 新增来源标记

`agent_identity.binding_source` 新增枚举 `machine_registered`，机器注册新建身份时写入。

### 3.2 判别式（两个信号同时成立）

```go
func isPreplacedOccupier(ident *model.AgentIdentity) bool {
    return ident.BindingSource == model.AgentIdentityBindingSourceMachineRegistered && ident.BootID == ""
}
```

**为什么必须两个信号同时成立**：

| 单看来源 | 单看 boot_id |
|---|---|
| 依赖标记完备；存量未迁移行会漏判（保守，需人工） | 会把「旧版本建的、来源未标记」的真身份误判为空壳 → **削弱 FR-141 防线** |

双条件使函数对未迁移的历史行保持保守（宁可要求人工），且对已迁移行精确识别。

### 3.3 审批自动让位

`resolveOccupierForApprove` 中，占用者为预置壳时**跳过** `ForceUnbindOccupier` 要求，直接解绑让位；否则维持原语义（返回 `server_id_occupied`，需显式强制解绑）。

让位后仍写 `identity.rebind_with_force_unbind` 审计（与人工强制解绑同一动作），保留可追溯性。

### 3.4 存量回填

判据同 §3.2 的第二半：`binding_source = admin_assigned AND (boot_id = '' OR boot_id IS NULL)` → 改标 `machine_registered`。

该组合在 FR-235 之前**唯一**指向机器注册写入的行（真 agent 身份必带 `boot_id`）。幂等：回填后来源已变，重复启动不再命中。

## 4. 安全边界（明确不做）

- **不放宽真身份冲突**：有 `boot_id` 或来源非预置的占用者，仍返回 `server_id_occupied` 并要求人工强制解绑。
- **不削弱 disabled 拒绝复活**：机器注册刷新路径对 disabled 身份返回 `ErrIllegalState` 的边界不变。
- **不改 CP 推送的建行行为**：仍为「无插件实例预置身份并置 active」，以便无插件的被推实例也能被控制面识别。

## 5. 任务拆分

1. `model`：新增 `AgentIdentityBindingSourceMachineRegistered` 枚举并纳入合法性校验。
2. `agent_machine_register`：新建身份标记该来源。
3. `v2_control_plane_service`：新增 `isPreplacedOccupier` 并在 `resolveOccupierForApprove` 中据此自动让位。
4. `store`：新增 `backfillMachineRegisteredIdentitySources` 存量回填（幂等）。
5. 前端：`identity-detail-sheet` 补该来源中文标签（否则回落「未提供」）。
6. 测试：预置壳自动让位、真身份仍拦截、判别式穷举、回填分支与真机同构数据。

## 6. 验收标准

| 验收项 | 通过条件 |
|---|---|
| 预置壳自动让位 | 占用者为预置壳时，**不带** `forceUnbindOccupier` 也能审批成功，旧壳转 `unbound` |
| 真身份防线不破 | 真身份之间冲突仍返回 `server_id_occupied`，显式强制解绑后才成功 |
| 判别式穷举 | 仅「来源预置 + 空 boot_id」为真；单独满足任一条件均为假 |
| 存量回填 | 幂等；`boot_id` 为空/NULL 的 `admin_assigned` 行被标记，真身份与其它来源不受影响 |
| 真机同构 | 11 空壳 + 11 真身份同构数据回填后精确命中 11 个空壳、真身份零误伤 |
| 前端可辨 | 详情页该来源显示「控制面预置」而非「未提供」 |
| 机器注册不变 | 新建身份仍为 active + 绑定 serverId，审计仍写 `identity.machine_registered` |

## 7. 影响与迁移

- 升级后**存量冲突自动消退**：已存在的预置空壳被回填标记，原先需人工解绑的 pending 身份可直接审批通过。
- 已人工解绑过的历史行（真机 11 台现状）保持 `unbound`，无需处理。
- 外部平台（JianManager）**无需改动**。
