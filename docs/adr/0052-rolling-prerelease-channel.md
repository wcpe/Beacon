# ADR-0052：滚动预发布渠道 + 版本号判新（取代 ADR-0046 的 rc 模型）

**状态**：已接受（决策 2「滚动预发布版本号取根 VERSION」被 [ADR-0054](0054-rolling-prerelease-version-ci-computed.md) 取代为 CI 自算 minor+1；决策 1/3/4/5/6/7 仍有效）

## 背景

[ADR-0046](0046-rc-prerelease-channel.md) 把预发布定为**语义化 rc 号**（`vX.Y.Z-rc.N`）、**显式打 tag 触发**、且明确**只做 rc、不做 nightly / 滚动**。实践中需求变了（FR-117）：

- 希望**推 master 就自动发出最新预发布**供试用，并**喂 in-app 在线更新（FR-99/100）使更新功能可测**——rc 的「必须显式打 tag 才有预发布」太重，平时根本没有可被 in-app 检查到的较新 Release，更新 / 切渠道功能无从验证。
- 渠道概念要**简化为「正式版 / 预发布」两条**，去掉 rc 这一术语层。

这与 ADR-0046「不滚动 + rc 语义号」的两条核心决策直接冲突，故立本 ADR 取代之（ADR 不可变、只取代）。

## 决策

1. **渠道简化为两条：正式版（stable）/ 预发布（prerelease）。** 去掉 rc 概念与 `vX.Y.Z-rc.N` 语义 tag（取代 ADR-0046「预发布改用语义化 rc 号」）。

2. **重新启用滚动预发布**（取代 ADR-0046「只做 rc、不做 nightly / 滚动」）：推 master → CI 构建 + **覆盖发布同一个 prerelease Release**（移动 tag force-update，只留最新一份、不堆 Release 列表），`prerelease=true`，版本 = 当前根 `VERSION`（下一目标 `X.Y.Z`）。复用 `_build-release.yml`（多平台二进制 + launcher + 双端 jar + SHA256，DRY 不变）。

3. **正式版不变**：推无后缀正式 tag `vX.Y.Z` → stable Release（`prerelease=false`），走 `release.yml`，行为同前。

4. **「哪个新」按语义化版本号判**（取代 ADR-0046 的「rc 后缀有序 + 最新 prerelease」判定）：in-app 更新检查取该渠道的 Release，解析其语义版本号（`X.Y.Z`）与**当前运行版本 semver 比较**，渠道版 > 运行版即提示有更新。**渠道区分仍用 GitHub `prerelease` 布尔**（正式 = 最新非 prerelease；预发布 = 那个滚动 prerelease），不另起自研端点（这条沿用 ADR-0046）。

5. **同版本号语义**：同一 `X.Y.Z` 的滚动预发布被反复覆盖时版本号不变 → **不判「有更新」**（你已在该预发布上即最新，重拉 / 重启即可）；只有**跨版本号**（如运行 `0.16.0` → 预发布 `0.17.0`）才提示。**不引入 build 元数据细分同号新构建**（YAGNI，用户已定按版本号简单判）。

6. **tag↔VERSION 校验**：正式版无后缀路径不变（tag 去 `v` == 根 `VERSION`）。滚动预发布走移动 tag、不参与「剥 `-rc.N` 后缀比对」那套——按本 ADR 约定的滚动 tag 名与 `VERSION` 校验（实现细节落 FR-117）。

7. **触发路径**：`release.yml`（无后缀三段 glob）→ 正式版；**`prerelease.yml` 由 rc-tag 触发改造为 master-push 滚动触发**（或等价新 workflow）。两条路径：**打正式 tag → 正式版；推 master → 滚动预发布**。

## 理由

- 推 master 自动发预发布，是 FR-117 的根本目的——让试用方拿到最新、并让 in-app 更新 / 切渠道功能**有真实 Release 可检可更、可测**（`测试绿 ≠ 真能用` 的前置）。
- 两渠道（正式 / 预发布）比 rc 直观，去掉一层术语。
- 按语义版本号判新简单可靠、不依赖 rc 后缀排序；移动 tag 只留最新一份，Release 列表无噪声。
- 渠道区分继续用 GitHub `prerelease` 布尔，零自研端点（成熟、低成本，沿用 ADR-0046 该条）。

## 影响

- **CI**：改 `.github/workflows/*`——`prerelease.yml` 改造为 master-push 滚动发布（移动 tag + 覆盖 Release）；`release.yml` 正式版不变；`_build-release.yml` 复用（调用方传 `prerelease=true` + 滚动版本 / tag 处理）。`.github/workflows/*` 属受保护文件，本次经用户明确要求修改（FR-117）。
- **in-app 更新**：渠道判新改为 semver 版本号比较（FR-100/FR-118）；渠道名收敛为 正式 / 预发布。
- **文档**：`docs/OPERATIONS.md` §2.2、FR-99/100 渠道描述随实现更新；`README` ADR 索引加本条、ADR-0046 标「部分被取代」。
- **ADR-0046**：状态改「部分被 [ADR-0052] 取代」——被取代：rc 语义号、只做 rc 不滚动、rc 渠道有序判定；**仍有效**：GitHub `prerelease` 布尔区分渠道、`release.yml`/`prerelease.yml` 薄壳复用 `_build-release.yml`（DRY）。
- `sdd-publish-snapshot`（全局技能）的滚动快照模型与本决策方向趋同，但仍属全局插件、不在此擅改其正文；本仓库预发布以本 ADR 为准。

## 备选方案

- **保留 ADR-0046 的 rc 语义 tag**：必须显式打 tag 才有预发布，平时无较新 Release → in-app 更新 / 切渠道功能测不了，违背 FR-117 目的。取代之。
- **per-commit `vX.Y.Z-dev.<sha>` 多份预发布**：每推 master 发一个新 Release。否——Release 列表噪声大，且与「看版本号判新」矛盾（同 `X.Y.Z` 多份无法靠版本号区分）；用户选了「滚动单份、按版本号判」。
- **给同号新构建加 build 元数据细分**：让同 `X.Y.Z` 的新滚动构建也能判「更新」。否——YAGNI，用户明确按版本号简单判，同号重拉即可。
