# ADR-0082：RC/GA SDK Maven 双坐标发布

**状态**：已接受（2026-08-24，取代 ADR-0074 中 GA 完全禁止重新发布的约束）

## 背景

ADR-0074 固定了 RC 的不可变产品资产与 GA 原样晋级，避免经验证的候选和正式 GitHub Release 资产发生字节漂移。但下游业务插件还需要在 RC 阶段解析候选 SDK，并在 GA 阶段解析不带候选后缀的正式 SDK。`agent-api` 与 `agent-kit` 尚未由 RC/GA workflow 自动发布到远程 Maven releases 仓库。

Maven releases 仓库通常不允许覆盖同一坐标。`X.Y.Z-rc.N` 与 `X.Y.Z` 是两个不同的不可变版本坐标，且 Maven 工件不属于 GitHub Release 的产品资产闭集；因此不能把 SDK 正式坐标的发布误写成对 RC GitHub 资产的覆盖或替换。

## 决策

1. RC workflow 创建 `vX.Y.Z-rc.N` 后，只发布以下候选 Maven 坐标：
   - `top.wcpe.beacon:beacon-agent-api:X.Y.Z-rc.N`
   - `top.wcpe.beacon:beacon-agent-kit:X.Y.Z-rc.N`
2. GA workflow 必须先完成 ADR-0074 规定的 GitHub Release 产品资产原样晋级与校验；随后仅为同一 RC commit 发布以下正式 Maven 坐标：
   - `top.wcpe.beacon:beacon-agent-api:X.Y.Z`
   - `top.wcpe.beacon:beacon-agent-kit:X.Y.Z`
3. GA 为上述两个不同的正式 Maven 坐标重新生成 Maven 制品是唯一例外。GitHub Release 产品资产仍必须从最终 RC 原样复制，并继续核对文件名、大小与 SHA-256；GA 不得新增、替换或重新生成任何 GitHub Release 产品资产。
4. 发布地址、用户名、口令与可选签名凭据只能由 GitHub Actions Secrets 注入，复用 `BEACON_PUBLISH_*` 约定；仓库不得保存这些值。
5. 发布失败必须使相应 workflow 失败。已存在的 RC 或 GA Maven release 坐标不得覆盖、删除或重传；故障恢复前必须先核验远端坐标是否已实际发布。

## 理由

1. RC 坐标让下游在正式发布前用明确候选版本验证兼容性。
2. GA 坐标使下游无需依赖候选后缀即可解析稳定 SDK。
3. 将例外限制在两个不同 Maven 坐标，能保留 GitHub 产品资产的字节不可变性与在线更新来源边界。
4. 远程 Maven 发布不是跨 GitHub Release 的原子事务；使失败显式可见并禁止覆盖，能避免把不确定状态伪装为成功。

## 后果

### 正面

- 下游可分别依赖 `X.Y.Z-rc.N` 与 `X.Y.Z` 验证候选和正式 SDK。
- RC/GA GitHub Release 资产仍按 ADR-0074 原样晋级，`SHA256SUMS.txt` 校验规则不变。
- 发布凭据继续只存在于 Actions Secrets。

### 约束

- GA GitHub Release 已公开而 Maven 发布失败时，workflow 仍为失败；运维必须先核验 Maven 仓库状态，再决定是否安全重试。
- SDK Maven 重新生成仅限 `agent-api` 和 `agent-kit` 的不同版本坐标，不得扩展为对 Agent 插件 JAR 或 GitHub 产品资产的重新发布。
- 本 ADR 不引入 Maven Central Portal、Nexus Staging 或 Artifactory 专用的暂存/提升 API。

## 备选方案

### 方案 A：RC 预先生成正式 Maven 坐标，GA 只提升仓库暂存态

不同仓库的暂存与提升 API 不兼容，且当前发布目标不要求引入 Nexus Staging、Central Portal 或 Artifactory 专用集成。否决。

### 方案 B：GA 完全不发布 SDK Maven 坐标

下游只能继续依赖候选版本或人工上传，无法获得与正式 Beacon 版本对应的 SDK。否决。

### 方案 C：GA 重新生成并替换全部产品资产

这会使最终 GitHub Release 资产偏离已验证的 RC，违反 ADR-0074 保留的不可变产品资产边界。否决。
