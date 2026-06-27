# ADR-0054：滚动预发布版本号由 CI 自算（取代 ADR-0052 决策 2 的「版本取 VERSION」）

**状态**：已接受（取代 [ADR-0052](0052-rolling-prerelease-channel.md) 决策 2 的版本号来源；ADR-0052 其余决策仍有效）

## 背景

[ADR-0052](0052-rolling-prerelease-channel.md) 决策 2 定：滚动预发布版本号 = 根 `VERSION`，且把 `VERSION` 语义设为「下一个未发布目标 `X.Y.Z`」。这隐含一个前提——**`VERSION` 始终领先于已发布的最新正式版**。

实践证伪了该前提。发布流程（`sdd-release-version`）发正式版 `vX.Y.Z` 时，因 `release.yml` 的 tag↔VERSION 校验要求 `VERSION == X.Y.Z`，发版那一刻 `VERSION` 等于本次发布版；而**发版后没有任何步骤把 `VERSION` bump 到下一目标**。于是 `VERSION` 停留在已发布的版本号。实测：已发布 v0.17.0 后，`VERSION` 仍是 `0.17.0`，之后推 master 的滚动预发布版本号 = `VERSION` = `0.17.0` = 已发布正式版号，导致：

1. 滚动预发布与正式版**撞号**，`prerelease.yml` 按版本号去重、no-op 跳过，新构建发不出来；
2. in-app 更新「同号不提示」（ADR-0052 决策 5），真机运行 0.17.0 **永远检测不到**滚动预发布；
3. 修法若停留在「发版后手动 bump VERSION」，则**靠人记得、必然复发**（`sdd-release-version` 是全局技能、无此步，无法在仓库内强制）。

根因是「滚动预发布版本号依赖一个需要人工维护领先性的 `VERSION`」。把领先性交给人工是脆弱的。

## 决策

1. **滚动预发布版本号改由 CI 自算，与 `VERSION` 解耦**：`_build-release.yml` 的滚动分支（`rolling=true`）查 GitHub **最新正式 release**（`releases/latest`，该端点天然排除 prerelease），解析其 `X.Y.Z`，取 **`X.(Y+1).0`（minor+1、patch 归零）** 作滚动预发布版本号。**不再读 `VERSION`**。
2. **`VERSION` 仅服务正式版**：正式版（`rolling=false`，tag 触发）仍取触发 tag 去 `v`、仍做 tag↔VERSION 严格校验（ADR-0052 决策 3/6 不变）。`VERSION` 不必、也不应被「为了滚动预发布」而 bump——它就是「本次正式发布的版本号」，发版时由 `sdd-release-version` 设。
3. **判新模型不变**（ADR-0052 决策 4/5 仍有效）：滚动预发布仍是纯 `X.Y.Z`（不带 `-dev`/sha 后缀），in-app 按纯 `X.Y.Z` 比较。开发周期内最新正式版不变 → 滚动号稳定（如一直 `0.18.0`），同号不提示；跨周期（发 0.18.0 正式版后，最新正式版变 0.18.0）→ 滚动号自动跳 `0.19.0`，真机才再提示。
4. **首次无正式 release 兜底**：`releases/latest` 404（仓库尚无正式版）时回退基线 `0.0.0` → 滚动号 `0.1.0`。
5. **minor+1 的取舍**：滚动预发布是开发预览，版本号只需「领先最新正式版 + 周期内稳定」，不需精确预测下一正式版号（精确号由正式发版的 tag/VERSION 决定）。默认 minor+1 与「特性迭代」一致；若某周期实际只发 patch（如 0.17.1），滚动预发布显示 0.18.0 略超前——可接受，不为此引入「猜 patch/minor」的复杂度（YAGNI）。

## 理由

- **解耦根除脆弱性**：滚动预发布的领先性由 CI 从「最新正式 release」实时推导，不依赖任何人「发版后 bump VERSION」。这是对 ADR-0052 决策 2 唯一缺陷（依赖人工维护 VERSION 领先）的根治。
- **`releases/latest` 是可靠真源**：GitHub 该端点只返最新非 prerelease release，正是「最新正式版」的权威来源，CI 用 `gh api` + 内置 `GITHUB_TOKEN` 零额外配置可读。
- **仍满足 ADR-0052 判新模型**：纯 `X.Y.Z` + 同号不提示 + 跨周期跳号，决策 4/5 完整保留，只换了「`X.Y.Z` 从哪来」。
- **minor+1 简单可预期**：与多数迭代含 feat 的实际一致；不引 `-dev`/sha（ADR-0052 已否的同号语义与 Release 噪声问题继续被规避）。

## 影响

- **CI**：`_build-release.yml` 抽出版本号计算到单一前置步骤/job（滚动 = 最新正式 release minor+1、正式 = tag），各构建/发布步骤共用，保证一次性算出、全程一致；`prerelease.yml` 注释更新（版本号不再取 VERSION）。`.github/workflows/*` 属受保护文件，本次经用户明确要求修改。
- **VERSION**：滚动预发布不再读取它；它仅是「下次正式发布的版本号」，由 `sdd-release-version` 在发版时设、不需为预发布而提前 bump。
- **文档**：`docs/OPERATIONS.md` §2.2 版本号来源描述更新；`README` ADR 索引加本条、ADR-0052 标「决策 2 被 ADR-0054 取代」。
- **ADR-0052**：决策 2「版本 = 根 VERSION」被本 ADR 取代；决策 1（两渠道）、3（正式版不变）、4（纯 X.Y.Z 判新）、5（同号不提示）、6（tag↔VERSION 仅正式版）、7（触发路径）**均仍有效**。

## 被否的备选

- **发版后手动 bump VERSION 到下一目标**（ADR-0052 的隐含修法）：靠人记得、`sdd-release-version` 无此步无法强制、已被实践证明会忘并复发。否——根治应去掉对人工的依赖。
- **patch+1（0.17.0→0.17.1）**：最小领先、不猜迭代规模，但与「特性迭代」号不一致（feat 显示为 patch 号）。否——选 minor+1 与默认迭代一致。
- **`-dev.<sha>` 后缀 / per-commit 多份预发布**：每 commit 版本号变、每次提示更新（噪声），且 [ADR-0052](0052-rolling-prerelease-channel.md) 决策 5 与备选已明确否决。沿用否决。
- **`max(VERSION, 最新正式+1)`**：开发者 bump 了 VERSION 则精确、未 bump 则 CI 兜底领先。仍读 VERSION、未彻底解耦，且引入「两来源取大」的认知负担。否——用户明确要「CI 自算、不依赖 VERSION」。
