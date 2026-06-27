# ADR-0055：滚动预发布版本号带 `-dev.<sha>` + 同基线标识变即提示更新（取代 ADR-0052 决策 4/5 与 ADR-0054 的纯 X.Y.Z）

**状态**：已被 [ADR-0056](0056-rolling-prerelease-dev-distance-version.md) 取代（原决策：取代 [ADR-0052](0052-rolling-prerelease-channel.md) 决策 4/5 与 [ADR-0054](0054-rolling-prerelease-version-ci-computed.md)「纯 X.Y.Z」部分。ADR-0056 改为基线不 +1、`-dev.<提交距离>.g<sha>`、提交距离序号判新）

## 背景

[ADR-0054](0054-rolling-prerelease-version-ci-computed.md) 让滚动预发布版本号 = 最新正式版 minor+1 的**纯 `X.Y.Z`**（如 `0.18.0`），[ADR-0052](0052-rolling-prerelease-channel.md) 决策 4/5 据此「按纯 `X.Y.Z` 判新、同号不提示」。

这在**反复验证在线更新功能**时成了死穴：一个开发周期内最新正式版不变 → 滚动预发布号恒定 `0.18.0`。真机第一次更新到 `0.18.0` 后，后续每次改 beacon 再推 master，滚动预发布仍是 `0.18.0` → in-app「同号不提示」→ **真机检测不到更新、无法再次触发**。而 FR-119/120 的「自替换换版 / 起不来自动回退 / 手动回滚」恰恰需要在真机上**反复触发在线更新**来验证。

姊妹项目 JianVideo 用 `X.Y.Z-dev.<短sha>`：每次 push sha 变 → 版本号变 → 真机每次都能检测到更新。这正是测试所需。

> [ADR-0052](0052-rolling-prerelease-channel.md) 决策 5 当初**否决** `-dev.<sha>`，理由是「每次 push 都提示更新（噪声）」。本 ADR 是这一权衡的**有意反转**：对**预发布渠道**而言，「每次 push 都能更新」不是噪声而是**特性**——试用 / 测试方就是要拿最新、要能反复验证更新链路。正式渠道不受影响（仍纯 `X.Y.Z`）。

## 决策

1. **滚动预发布版本号 = 最新正式版 minor+1 基线 + `-dev.<7 位 commit sha>`**（如 `0.18.0-dev.715989a`）。基线算法沿用 [ADR-0054](0054-rolling-prerelease-version-ci-computed.md)（CI 取最新正式 git tag minor+1、与 VERSION 解耦），仅在其后缀拼 `-dev.<sha>`。CI 用 `${GITHUB_SHA}` 前 7 位。
2. **`-dev.<sha>` 是 semver 预发布段**：贯穿 Release 标题（`v0.18.0-dev.<sha>`）、产物名（`beacon-0.18.0-dev.<sha>-<os>-<arch>`）、agent jar、`SHA256SUMS`、in-app 版本解析与资产匹配，全程一致。
3. **判新改为「基线比较 + 同基线标识不同即更新」**（取代 ADR-0052 决策 4/5 的「纯 X.Y.Z、同号不提示」）：in-app 解析渠道版与运行版的 `X.Y.Z` 基线——远端基线高即更新、低即否；**基线相同时预发布标识（`-dev.<sha>`）不同即视为更新**。于是每次 push（sha 变）真机都能检测到、反复触发；`dev → 正式`（同基线、标识由 `dev.x` 变空）也算更新（提示从预发布升正式）。
4. **正式渠道不变**（ADR-0052 决策 3、ADR-0054 决策 2 关于正式版仍有效）：正式版仍纯 `X.Y.Z`、tag↔VERSION 校验、无 `-dev` 后缀。`-dev.<sha>` 只用于滚动预发布渠道。
5. **仍单份滚动 Release**：移动 tag `prerelease`、发布前删旧 release 重建只留最新一份（沿用既有），**不**因带 sha 就堆多份 Release（区别于 ADR-0052 备选否决的「per-commit 多份 Release」——本决策只让单份滚动 Release 的版本号带 sha）。

## 理由

- **可反复验证在线更新是硬需求**：FR-119/120 的换版 / 自动回退 / 手动回滚必须真机反复跑，纯 `X.Y.Z` 一次更新后就「永远最新」、测不动；`-dev.<sha>` 让每次 push 都可触发。
- **预发布渠道的「频繁提示」是特性非噪声**：装预发布的本就是试用 / 测试方，要最新、要能更新；正式渠道仍稳定纯 `X.Y.Z`，生产用户不受打扰。
- **用预发布段而非 build 元数据**：semver 中 build 元数据（`+sha`）不参与优先级比较（`0.18.0+a == 0.18.0+b`），无法据此判新；预发布段（`-dev.sha`）可比较「标识是否不同」，恰好实现「sha 变即更新」。
- **基线仍 semver 合规**：`0.18.0-dev.<sha>` 低于同基线正式版 `0.18.0`，`dev → 正式` 判为更新符合直觉。

## 后果

- `internal/update/semver.go`：`semver` 加 `prerelease` 字段；`parseSemver` 接受 `X.Y.Z-<prerelease>`（不再拒 `-` 后缀，仍拒 `+` build 元数据）；`compareSemver` 改 `compareBase`（只比基线）；`IsNewer` 改为「基线比较 + 同基线标识不同即更新」。单测同步。
- `_build-release.yml` meta job：滚动版本号在 minor+1 基线后拼 `-dev.${GITHUB_SHA::7}`。
- 资产名 / Release 标题 / agent jar / SHA256SUMS 自动带 `-dev.<sha>`（沿用 `version` 串，无需单独改）。
- 文档：`docs/ARCHITECTURE.md`、`docs/OPERATIONS.md` §2.2、`docs/API.md` 的判新 / 版本号描述更新；`README` 索引加本条、ADR-0052 / ADR-0054 标对应部分被取代。
- [ADR-0052](0052-rolling-prerelease-channel.md) 决策 4（纯 X.Y.Z 判新）、决策 5（同号不提示）被本 ADR 取代；决策 1/2/3/6/7 仍有效。[ADR-0054](0054-rolling-prerelease-version-ci-computed.md) 的「基线由 CI 取最新正式 tag minor+1」仍有效，仅「纯 X.Y.Z、无后缀」被本 ADR 取代为「基线后拼 `-dev.<sha>`」。

## 被否的备选

- **保持纯 `X.Y.Z`（ADR-0054）**：开发周期内版本号恒定，真机一次更新后检测不到后续 push，**无法反复验证在线更新**——本 ADR 的根本动因。否。
- **build 元数据 `0.18.0+<sha>`**：semver 规定 build 元数据不影响版本优先级，`0.18.0+a` 与 `0.18.0+b` 判等，无法据此提示更新。否，用预发布段 `-dev.<sha>`。
- **per-commit 发多份预发布 Release**（ADR-0052 备选）：Release 列表噪声大。本决策仍单份移动 tag Release，只让版本号带 sha，规避多份问题。否（沿用单份滚动）。
