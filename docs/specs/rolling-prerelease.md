# 功能规格：CI 推 master 自动发滚动 prerelease + in-app 两渠道按版本号判新

> 状态：开发中　·　关联 PRD：FR-117　·　分支：feature/fr-117-rolling-prerelease

## 1. 背景与目标
in-app 在线更新（FR-99/100）与「切渠道」功能需要一条**平时就存在、随 master 滚动刷新**的预发布 Release 才能被检查到、被更新、被验证。旧 rc 模型（[ADR-0046](../adr/0046-rc-prerelease-channel.md)）要求显式打 `vX.Y.Z-rc.N` tag 才有预发布，平时根本没有较新 Release，更新功能无从验证。[ADR-0052](../adr/0052-rolling-prerelease-channel.md) 取代之：渠道收敛为「正式 / 预发布」两条、推 master 自动覆盖发布滚动 prerelease、按语义版本号判新。属第二期（P2）运维体验。

## 2. 需求（要什么）
- 推 master → CI 构建并**覆盖发布同一个滚动 prerelease Release**（移动 tag、只留最新一份），`prerelease=true`，版本 = 根 `VERSION`。复用 `_build-release.yml`（5 平台二进制 + launcher + 双端 jar + SHA256）。
- 打无后缀正式 tag `vX.Y.Z` → stable Release，行为**不变**（`release.yml` 不动）。
- in-app 更新渠道收敛为 `stable` / `prerelease`（去 `rc`）；判新改为语义版本号 `X.Y.Z` 比较：渠道版 > 运行版才提示，**同号不提示、跨号才提示**。渠道区分仍用 GitHub `prerelease` 布尔。
- 范围内：`.github/workflows/{prerelease,_build-release}.yml`；`internal/update/*`（渠道枚举、版本解析、判新）；`internal/service/settings_metadata.go`（`update.channel` 取值）；前端 `update.channel` 下拉枚举（与后端同口径，否则下拉给出 `rc` 后端拒）；文档（OPERATIONS §2.2 / API.md / CHANGELOG）。
- 不做（范围外）：FR-118 的版本页交互（立即检查移位 / 更新裁决 / 切渠道回显）；rc 概念的历史 Release 兼容（按 ADR-0052 直接切两渠道）；build 元数据细分同号新构建（YAGNI，ADR-0052 决策 5）。

## 3. 设计（怎么做）

### 3.1 滚动 tag 与版本解析（关键决策）
- **滚动 tag 名 = `prerelease`**（固定移动标签，`softprops/action-gh-release` 据其 `tag_name` force-update、覆盖同一 Release，只留一份）。
- 滚动 Release 的**资产仍按 `VERSION` 命名**（`beacon-<VERSION>-<os>-<arch>[.exe]`、launcher 同），且 Release `name` 设为 `v<VERSION>`——使版本号语义可被 in-app 解析。
- in-app 更新解析版本：`tag_name` 能解析为 semver 则用之（stable 路径，tag=`vX.Y.Z`）；否则回退解析 Release `name`（滚动路径，tag=`prerelease`、name=`vX.Y.Z`）。解析得的版本同时用于 `IsNewer` 比较与本平台资产名 `assetName(version)`。
- tag↔VERSION 校验：正式无后缀路径不变（tag 去 `v` == `VERSION`）；滚动路径 tag=`prerelease`、不走「剥 `-rc.N`」那套，直接以 `VERSION` 作版本（无需 tag 反推）。

### 3.2 CI
- `prerelease.yml`：触发由 rc-tag `push.tags` 改为 `push.branches: [master]`；不再传 tag，改传滚动 tag 名给 `_build-release.yml`。
- `_build-release.yml`：新增 `rolling`（bool）/ `rolling-tag`（string）入参。
  - `rolling=false`（正式 / 现状）：tag 由 push 提供，tag↔VERSION 校验同前，版本注入用 `GITHUB_REF_NAME` 去 `v`。
  - `rolling=true`：版本注入用根 `VERSION`；资产名用 `VERSION`；`action-gh-release` 显式传 `tag_name=<rolling-tag>` 强制移动 tag 覆盖同一 Release、设 `name=v<VERSION>`、`prerelease=true`，前置滚动预发布中文说明头。
- `release.yml` 不动（仍调 `_build-release.yml`，rolling 缺省 false）。

### 3.3 后端 in-app 更新（Go）
- `Channel`：`ChannelStable="stable"` / `ChannelPrerelease="prerelease"`（删 `ChannelRC="rc"`）；`latestForChannel` 的 `prerelease` 分支取最新 `prerelease=true` 的 Release。
- `ghRelease` 加 `Name` 字段；新增 `releaseVersion(rel)` 解析「tag 优先、name 回退」得 semver 字符串。
- `parseSemver` 收敛为只解析 `vX.Y.Z`（三段数字）——去掉 `-rc.N` 预发布段支持（ADR-0052 决策 1 去 rc，决策 4/5 按 X.Y.Z 判、同号不提示）。带任何后缀（含 `-rc.N`）视为非法 → 解析失败 → 当未知不误判。
- `CheckForUpdate` / `ApplyUpdate` 用 `releaseVersion(rel)` 作目标版本（替代直接用 `rel.TagName`）喂 `IsNewer` 与 `assetName`。
- `settings_metadata.go`：`updateChannels` 收敛 `{stable, prerelease}`，desc 文案改两渠道。

### 3.4 前端
- `VersionUpdatePage.tsx`：`UPDATE_CHANNELS = ['stable','prerelease']`；i18n `channelHint` / `types.ts` 注释 rc→预发布。仅枚举对齐，不动交互（交互留 FR-118）。

## 4. 任务拆分
- [ ] PRD §4 FR-117 → 开发中
- [ ] 写本规格
- [ ] 测试先行：semver 去 rc / 同号不判更新 / 跨号判更新；渠道选取 stable vs prerelease；releaseVersion tag 优先 name 回退
- [ ] 实现 Go：Channel / ghRelease.Name / releaseVersion / parseSemver 收敛 / service 用 releaseVersion / settings 枚举
- [ ] 实现前端枚举 + i18n
- [ ] 改 CI：prerelease.yml（master push）+ _build-release.yml（rolling 入参）
- [ ] 文档同步：OPERATIONS §2.2、API.md（channel 枚举 / 渠道说明）、CHANGELOG 未发布段
- [ ] `go test ./internal/update/... ./internal/service/...` 绿；YAML 核对

## 5. 验收标准
- `go test ./internal/update/... ./internal/service/...` 全绿，新测覆盖：同 `X.Y.Z` 不提示、跨号提示、stable/prerelease 渠道选取、releaseVersion tag/name 回退。
- workflow YAML 语法有效、与现有结构一致；`release.yml` 未改。
- 推 master → CI 发出/覆盖滚动 prerelease（5 平台 + SHA256）、管理台预发布渠道「立即检查」能发现它——**需 push + GitHub Actions 真验**（本地无法验，如实标待验）。

## 6. 风险 / 待定
- 滚动 tag 名定为 `prerelease`（ADR-0052 未硬性指定，留 FR-117）；若后续要改名，仅改 `prerelease.yml` 的入参 + 文档。
- 真机/CI 维度（推 master 真发 Release）本地不可验，须 push 后在 Actions 验证。
