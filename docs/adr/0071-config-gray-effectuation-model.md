# ADR-0071：变更单配置变更的灰度生效语义模型

**状态**：已接受

## 背景

FR-162 / FR-171 要把「配置变更」纳入变更单的统一灰度编排：同一配置作用域在灰度期间要让「批内已生效目标」与「其余服」呈现不同版本，末批确认后正式切版，未完成即回滚。P7 配置中心 V2（FR-160/161）为此预留两个接缝，均已核实真建可用、有集成测试（`config_center_integration_test.go`）：

- 接缝①「按作用域版本覆盖解析」：`ConfigCenterService.EffectivePlaintext(fileID, target, pins []ConfigScopePin)`（`config_center_effective.go:60`），进程内明文渲染时对指定作用域用指定版本覆盖参与合并、不改 head。
- 接缝②「回退原语」：`ConfigCenterService.RollbackVersion(versionID, ...)`（`config_center_version_service.go:96`），基于历史版本追加生成新定稿版本、返回新版本 id。

设计交付域的配置灰度机制时暴露一处语义张力：交付编排 spec（[v2-delivery-orchestration](../specs/v2-delivery-orchestration.md) §4.6.2 初稿）假设「当前正式版本」与「正式切版到 to_version」是两个独立概念；但配置中心 V2 的实现里**生效版本永远 = 链 head（version_no 最大那行）**，无独立「当前正式版本」指针，且 `SaveVersion` 使每个新版本**立即成 head**（`config_center_version_service.go:178-183` 事务内 `maxNo+1` 追加，[v2-config-center](../specs/v2-config-center.md) §4.2 「保存即定稿、无服务端草稿态」）。二者在追加模型里对不上，须定一个可自洽、且不推翻已接受决策的模型。

**关键澄清（消解张力）**：配置中心 V2 的 **head = 最新定稿版本，不是「当前生效版本」**。V2 配置**没有 agent 下发通道**（[v2-config-center](../specs/v2-config-center.md) §2「本域无 agent 面端点：配置下发 / 生效 / 灰度全部归交付编排域」；全仓无 `/beacon/v2/agent/*` 配置端点），线上「生效」100% 是交付域的事——`SaveVersion` 只改定稿，对线上零即时影响。（agent 侧的 `resync-config` / `ConfigApplier` 是被第二版取代的 **Legacy v1** 配置通道，不喂 V2 config-center，处置同 [ADR-0058](0058-controlled-large-file-sync-channel.md) 描述的旧文件同步域。）把 head 正确理解为「定稿态」、把「生效版本」理解为「交付域最近推到该作用域服上的版本」——张力消解，本就是两个域的两件事。

## 决策

采用 **模型 A（pin 落后者到 from、head 自然停在 to）+ 冻结渲染**，配置域代码零改动，仅用上述两个接缝：

1. **灰度渲染（pin 覆盖）**：单进入 rolling 后，交付域为每个 config_change 项在**本单目标集**上按目标 activation 态选版本：**已 activated 目标该作用域取 to_version、未 activated 取 from_version**。同一配置文件在本单内的全部作用域项先按 `config_file` 聚合，再经 `EffectivePlaintext(fileID, {server}, pins)` 一次性传入完整 pin 集合，禁止逐项渲染后按路径覆盖。**config-center head 从不直接渲染给线上服**——交付域始终以显式版本选择渲染。

2. **冻结渲染（content-addressed）**：组单 / 推送时把每个目标按配置文件聚合后的载荷冻结为 sha256 内容寻址 blob（见 [ADR-0069](0069-delivery-data-plane-blob-relay-and-agent-stream-transport.md)）逐批推；pin 只在渲染时作「版本选择器」用一次，载荷冻结后同单内 head 漂移不影响已冻结载荷。payload 准备重跑时原子替换该单全部工件集合，清除已移除路径与目标的旧授权。

3. **from 锚点 = 交付域「上一交付版本」**：`change_order_item.config_from_version_id`（回滚基准）取「该作用域最近一次真正交付到服上的版本」——交付域自持事实（= 最近一个 completed 单在该作用域的 `config_to_version_id`；从无交付则该层为 nil，回滚走撤销层贡献）。**不简单取当前 head**：运维在配置中心连编多版（v5→v6→v7）后发 v7 时，head 的 `based_on` 指向可能从未交付过的 v6，会致 from 锚点错位；取「上一交付版本」才正确。（首次交付且需 from 时以 `based_on_version_id` 兜底。）

4. **正式切版（末批确认）**：末批推进门人工确认（单 → completed）时，同一事务内**清除本单 pin** + 交付域记「该作用域已交付版本 = to_version」。head 自然已 = to_version，故**无需移动 head、不调 `RollbackVersion(to)`**（head 内容已 = to 会撞 `CONFIG_NO_CHANGE`）。

5. **冲突守卫（启动时）**：拒绝启动当「目标集与其他活动单（rolling / paused / rolling_back）相交」**或**「本单 config_change 的 `(config_file, scope)` 与其他活动单的 config_change 重叠」——后者防两张单并发灰度同一配置作用域时经 head 互相泄漏未定稿的灰度值。

6. **整单回滚的配置回退**：按 spec §4.7.2，对各 config_change 项以 `RollbackVersion(config_from_version_id)` 生成新定稿版本并使 head 回到 from 内容 + 清 pin + 重新生效。**撞 `CONFIG_NO_CHANGE`（当前 head 内容已 = from，如回滚重试）时当幂等成功吞掉**（`errors.Is(err, apperr.ErrConfigNoChange)` → 该项回退已达成）；from 为 nil 的层（本单首次引入）走撤销层贡献（`RemoveScopeContribution`），head 已是 removal 时同样幂等吞掉。

7. **配置域代码零改动**：pin 事实存储（orderId × 目标集 → to_version）、pin 生命周期、正式切版事务、回滚编排全归交付域；两接缝纯够用，**不给 config-center 加「暂存 / 未发布版本」或「head 之外的当前正式版本指针」**。

## 理由

- 与配置中心「保存即定稿成 head」硬约束同向，不逆流制造服务端草稿态（[v2-config-center](../specs/v2-config-center.md) §8-3 已明确不做）。
- **不静默推翻已接受决策**（[decision-alignment](../../.claude/rules/decision-alignment.md)）：模型 B（head 停在 from、to 存在但非 head）要么需引入服务端草稿态（推翻 §8-3），要么靠 churn hack 污染不可变链，均否。
- 生效语义归交付域，符合 spec §6 边界「配置域只产定稿、不追踪生效态」；把「当前正式版本指针」塞进 config-center 会让配置域越界承接生效语义。
- from 用交付域「上一交付版本」而非当前 head，回滚基准在多版编辑下仍正确。
- 冻结渲染 + pin 一次性选版，使载荷确定、可 sha256 去重、与 [ADR-0069](0069-delivery-data-plane-blob-relay-and-agent-stream-transport.md) 数据面一致；head 只作定稿暂存，永不直接下发。

## 后果

- delivery spec §4.6.2 step1/step2、§4.2.1 step4 随本 ADR 改口径（「当前正式版本」→「交付域自持的上一交付版本」；正式切版=清 pin 不移 head），[v2-config-center](../specs/v2-config-center.md) §4.6 补一句「head = 定稿非生效」——已随本次同步。
- 交付域需能求「每作用域上一交付版本」（从 completed 单历史查 `config_to_version_id`，**无新表**）。
- 整单回滚须把 `CONFIG_NO_CHANGE` 当幂等成功，需覆盖测试（回滚 + 回滚重试）。
- **残余窄边界（已知、暂不实现）**：同一 `config_file` 的**不同 scope 层**被两张「目标不相交」的活动单并发灰度时，冻结渲染对「其余层」按当时 head 取值，可能提前带入另一单尚未定稿的层变更——`(config_file, scope)` 级守卫挡同层不挡跨层同文件。判为窄边界（同文件跨层并发灰度到重叠服群本就罕见，可人工串行）；真机若暴露，可把守卫收紧到 `config_file` 级、或渲染「其余层」也 pin 到其上一交付版本。本版 YAGNI 不预实现，仅记录。

## 备选方案

- **模型 B（head 停在 from、to 存在但非 head，spec 字面）**：需 config-center「服务端草稿态 / 非 head 版本」能力（违 [v2-config-center](../specs/v2-config-center.md) §8-3 已接受决策），或编辑出 to 后 `RollbackVersion(from)` 把 head 顶回 from、末批再顶回 to 的 churn（每单产 2+ 冗余版本、污染不可变链、审计噪声）。否决。
- **config-center 新增「head 之外的当前正式版本指针」**：让配置域越界承接「生效」语义，违 spec §6「本域只产定稿」边界；「当前生效版本」本就该交付域持有（可由完成单历史重建）。否决。
- **交付域完全不碰 config-center，回滚只重置 live 指针、不生成新定稿版本**：更「纯」，但使接缝② `RollbackVersion` 空置、偏离 spec §4.7.2，且定稿 head 与线上 live 长期背离难向运维解释。否决——保留 `RollbackVersion` 让定稿 head 在终态（切版 / 回滚后）与 live 对齐。
- **live 渲染（每批按当时 head + pin 现算，取代冻结）**：引入 head 漂移面、与 content-addressed blob 去重语义不吻合。否决（已定冻结渲染）。
