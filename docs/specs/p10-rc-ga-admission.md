# 历史规格：P10 RC/GA 发布收口

> 状态：历史背景，非执行真源；本文不改变 PRD 中任何 FR 状态
>
> **当前执行真源**：[ADR-0074](../adr/0074-simple-rc-ga-release-flow.md)。[ADR-0072](../adr/0072-immutable-rc-ga-promotion-and-n-minus-one.md) 与 [ADR-0073](../adr/0073-standard-rc-ga-release-lifecycle.md) 仅保留为历史背景，不再作为当前 RC/GA 流程依据。
>
> 关联 PRD：FR-169、FR-182～FR-185 · 历史阶段：P10 RC / GA
>
> 本文只保留 P10 的背景、兼容性关注点和真实发布状态。当前发布按 ADR-0074 的简单通用 RC/GA 规则执行；本文不再编排发布，不要求任何 P10 专用脚本、测试入口、workflow、证据 schema 或 acceptance 门。

## 1. 历史背景

P1-P9 完成了第二版从工程化、身份与区服权威、可观测、归档、配置、文件资产到交付编排的核心能力。P10 原计划以 `v0.30.0` 为 N-1，围绕首个 `1.0.0` 候选集中复验兼容、迁移、平台和发布流程。

历史方案曾进一步引入分阶段审批、长期证据归档和多个发布位置的晋级编排。该方案复杂度超过当前需要，现已收敛为通用的不可变 RC 和 GA 原样复制规则。历史 P10 的 N-1、四格兼容、迁移和平台检查可继续作为测试参考，但不再是创建 RC 或 GA 的专用硬门。

## 2. 当前通用 RC/GA 规则

### 2.1 RC

- RC 使用 `vX.Y.Z-rc.N`，并作为 prerelease 发布。
- 每个 RC 固定指向一个 commit；tag 不得移动、删除后复用或改指向。
- 每个 RC 的产品资产只构建和发布一次。发布后不得覆盖、替换或补传产品资产。
- 发现需要改变源码、构建结果或产品资产的问题时，必须提交修复并发布递增序号的新 RC。
- RC 不进入标准在线更新候选集合，只能由用户显式选择安装。

### 2.2 GA

- GA 使用 `vX.Y.Z`，并与选定的最终 RC 指向同一 commit。
- GA 直接从最终 RC 原样复制产品资产，不执行编译、打包、重建、重新生成或替换。
- 复制前后按产品资产逐项核验文件名、大小和 SHA-256；集合或任一 SHA-256 不一致时不得发布 GA。
- GA 发布说明可以更新，但不得借此改变产品资产字节。
- 发布失败时保留可观察的失败状态并修复流程；不得通过重建另一套 GA 资产绕过不一致。
- 真实 GA 的 `release` job 不额外要求 GitHub Environment 人工审批；在 master 上对已公开 RC 完成身份与资产校验后直接原样晋级并公开。

这里的“产品资产”是用户安装或执行的发布文件及其校验和文件。过程日志、审批记录和历史证据文件不属于产品资产，也不是 GA 的必需附件。本地验证或本地预备不写入真实 tag/Release，不能视为 GA；截至 2026-07-20，真实 RC 与 GA 均尚未发生。

## 3. API 与在线更新兼容性

本次文档收口不改变 REST API、`agent-api`、Agent 协议、序列化格式或 GA 版本语义。

标准在线更新继续沿用既有 GA-only 规则：

1. 只接受 `prerelease=false` 且 tag 精确匹配 `^v\d+\.\d+\.\d+$` 的 GA Release。
2. `vX.Y.Z-rc.N` 不进入普通在线更新候选集合。
3. Actions Artifact 不进入在线更新候选集合。
4. `update.channel` 保持 `stable`；历史 `prerelease` 配置读取时归一为 `stable`。
5. API 中既有 `channel` 字段及其 GA/stable 语义保持不变。

## 4. 历史兼容性关注点

以下内容保留为发布前测试参考，不构成 P10 acceptance 硬门：

- 历史 N-1 为 `v0.30.0`，V2 可升级基线为 `v0.20.0+`；`v0.19.x` 及更早 Legacy 不承诺原地升级。
- 控制面与 Bukkit/Bungee Agent 可按 N/N-1 组合检查注册、鉴权、指标、调度、连接、消息、资产、配置与降级行为。
- 数据库迁移保持 forward-only；需要回退时优先使用已验证的协调备份恢复，不依赖 down migration。
- API 与 `agent-api` 在 RC 周期内默认冻结；修复应保持向后兼容，破坏性变更必须进入新的版本计划。
- Linux amd64、Linux arm64、Windows amd64、Darwin arm64 可继续作为控制面产品资产的发布前 smoke 范围。

这些检查由仓库通用质量流程或版本负责人按改动风险选择，不要求生成 P10 专用清册、manifest 或长期归档。

## 5. 不再使用的历史机制

下列机制已退出当前发布流程，不再作为 RC/GA 条件，也不应由本文触发实现或调用：

- 私有审计仓库和跨仓库存证。
- 长期审批记录归档、审批历史回拉及其哈希绑定。
- S11、S12、S14 阶段编号及对应的预检、授权和公开回拉证据契约。
- Maven Central staging/publish 晋级编排。
- OCI stable tag/digest 晋级编排。
- accepted manifest、authorization evidence、final evidence 等最终证据体系。
- P10 acceptance、43 FR 清册、稳定窗口、风险等级或最终 evidence 作为 GA 硬门。
- P10 专用发布脚本、测试脚本、workflow 审计命令和专用验收目录。

仓库中若仍保留相关历史实现或测试，它们不因本文而成为当前发布依赖；当前通用 workflow、Make 入口和 shell 校验脚本实现 ADR-0074 的真实 tag、同 commit、RC/GA 原样资产规则，且真实 GA 不再附加 Environment 人工审批闸门。

## 6. 真实发布状态

截至 2026-07-20：

- Agent 真机 E2E 启动链已迁移到正式版 `mc-testkit 0.5.0`；本地完成过 harness race、E2E 包编译、Agent build，以及 directory、override、metrics 真机验收。
- GitHub Actions Linux E2E run `29670927028` 已成功。该结果只代表对应 Linux 运行，不代表通用 RC 或 GA 已发布。
- `v1.0.0-rc.N` 的真实候选发布和 `v1.0.0` GA 原样复制均尚未发生。
- FR-169、FR-182～FR-185 不得仅因仓库中存在历史实现、workflow、脚本、测试或本文而标记为已交付；状态必须以实际发布结果和 PRD 记录为准。

## 7. 发布核对摘要

通用发布只要求维护以下核心不变量：

| 对象 | 核对项 |
|---|---|
| RC | prerelease；tag/commit 不可变；产品资产发布后不可覆盖 |
| GA commit | 与最终 RC commit 完全相同 |
| GA 产品资产 | 从最终 RC 原样复制；文件名、大小、SHA-256 逐项相同 |
| GA 构建行为 | 不编译、不打包、不重建、不替换产品资产 |
| 在线更新 | 只自动选择严格 `vX.Y.Z` GA，拒绝 RC 与开发 Artifact |

任何产品资产变化都必须回到新的 RC，不能直接修补 GA。
