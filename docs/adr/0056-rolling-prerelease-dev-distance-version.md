# ADR-0056：滚动预发布版本号改 `<基线>-dev.<提交距离>.g<sha>` + 提交距离序号判新（取代 ADR-0055）

**状态**：已接受（取代 [ADR-0055](0055-rolling-prerelease-dev-sha-version.md) 全部；将 [ADR-0054](0054-rolling-prerelease-version-ci-computed.md) 的「基线 = 最新正式 minor+1」取代为「基线 = 最新正式 tag 不 +1」，其 CI 自算 / 与 VERSION 解耦仍有效；[ADR-0052](0052-rolling-prerelease-channel.md) 除已被前序取代的决策外其余仍有效）

## 背景

[ADR-0055](0055-rolling-prerelease-dev-sha-version.md) 让滚动预发布版本号 = **最新正式 minor+1 基线** + `-dev.<7 位 sha>`（如 `0.18.0-dev.715989a`），判新用「基线比较 + 同基线预发布标识不同即更新」。它解决了「纯 `X.Y.Z` 一次更新后测不动」的死穴，但落地后暴露两个问题：

1. **minor+1 基线与主干真实进度脱节**：基线靠「发正式版这一动作 +1」抬升，而非反映主干实际走到哪。`0.18.0-dev` 在 `0.18.0` 尚未发布时就出现，语义上倒挂。
2. **「标识不同即更新」无序**：任何 sha 改写（哪怕 `git commit --amend` 不增内容）都让标识变 → 判更新；无法区分「主干真有新提交」与「仅 sha 变」，也无法表达「无新提交就不该有新 dev」。

用户要求**严格对齐姊妹项目 JianVideo 的脚本与机制**：基线取最新正式 tag **不 +1**、用**提交距离**作有序序号、把版本号计算**收敛成可本地复现的脚本**、移动 tag 名用 `dev`。

## 决策

1. **滚动预发布版本号 = `<最新正式 tag 基线，不 +1>-dev.<提交距离>.g<7 位短 sha>`**（如 `0.17.0-dev.3.g6b6dd71`）。
   - 基线 = `git describe --tags --abbrev=0 --match 'v[0-9]*.[0-9]*.[0-9]*'` 去 `v`（**不再 +1**，取代 [ADR-0054](0054-rolling-prerelease-version-ci-computed.md) 的 minor+1）；取不到回退 `0.0.0`。
   - 提交距离 = `git rev-list --count <tag>..HEAD`（无 tag 时退总提交数）。
   - 计算**收敛到 `scripts/dev-version.sh`**（参照 JianVideo 同名脚本），CI 与本地共用同一脚本。
2. **提交距离为 0（主干自该 tag 起无新提交）→ 脚本退出码 1 且不输出版本号**；`_build-release.yml` 的 `meta` job 据此置 `skip=true`，后续 web / binaries / jars / release job 全部跳过——避免「发完正式版后主干无新提交仍凭空滚动一个 dev」。
3. **判新改为「基线比较 + 提交距离序号」**（取代 [ADR-0055](0055-rolling-prerelease-dev-sha-version.md) 决策 3 的「标识不同即更新」）。`IsNewer(current, remote)`：
   - 先比 `X.Y.Z` 基线：远端高即更新、低即否；
   - 基线相同时：**都正式** → 不更新；**都 dev** → 比提交距离序号，远端序号大才更新（每次 push 提交距离 +1 → 真机可反复触发；无新提交序号不变 → 不误报）；**一正式一 dev** → 视为更新（预发布渠道下正式↔dev 切换都给目标渠道最新）。
4. **`semver` 结构**：[ADR-0055](0055-rolling-prerelease-dev-sha-version.md) 的 `prerelease string` 字段改为 `isPre bool` + `devSeq int`（从预发布段 `dev.<N>.g<sha>` 提取提交距离 `N`；非 dev 格式预发布段 `isPre` 为真、`devSeq` 为 -1）。`parseSemver` 仍接受 `X.Y.Z[-prerelease]`、仍拒 `+` build 元数据。
5. **移动 tag 名 `prerelease` → `dev`**（对齐 JianVideo）。仍单份滚动 Release（发布前删旧 release + tag 重建、只留最新一份）。
6. **正式渠道不变**：正式版仍纯 `X.Y.Z`、tag↔VERSION 校验、无 `-dev` 后缀。`-dev.<提交距离>.g<sha>` 只用于滚动预发布渠道。

## 理由

- **提交距离序号天然有序、对应主干真实进度**：比「标识不同」精确——无新提交（提交距离不变 / 为 0）不误报、有新提交必触发；序号单调递增可直接数值比较判新。
- **基线不 +1 让 dev 号反映真实位置**：`0.17.0-dev.N` 语义上介于「已发布的 `0.17.0`」与「下个正式版」之间，不靠「发正式版抬基线」、不倒挂。
- **收敛脚本便于本地复现**：开发者本地跑 `scripts/dev-version.sh` 即得与 CI 一致的版本号，调试在线更新无需推 CI。
- **严格对齐 JianVideo**：用户明确要求参照其脚本与更新机制，降低两项目认知与维护成本。
- 「预发布渠道每次 push 都能更新」仍是**特性非噪声**（沿用 [ADR-0055](0055-rolling-prerelease-dev-sha-version.md) 的权衡反转）：装预发布的本就是试用 / 测试方；正式渠道仍稳定纯 `X.Y.Z`、生产不受打扰。

## 后果

- `internal/update/semver.go`：`prerelease string` → `isPre bool` + `devSeq int`；新增 `parseDevSeq` 从预发布段提取提交距离；`IsNewer` 改为「基线比较 + 提交距离序号」。单测 `semver_test.go` 同步（提交距离序号用例）。
- 新建 `scripts/dev-version.sh`（中文注释，参照 JianVideo）。
- `.github/workflows/_build-release.yml` 的 `meta` job：滚动分支改调 `bash scripts/dev-version.sh`、新增 `skip` 输出并接到 web / binaries / jars / release 四个 job 的 `if:`；checkout 保留 `fetch-depth: 0`。release name 仍为 `v<版本号>` 以保 `releaseVersion` 可解析（tag=dev 非 semver 时回退解析 name）。
- `.github/workflows/prerelease.yml`：rolling-tag `prerelease` → `dev`。
- `internal/update/github.go` 无需改：`releaseVersion` 在 tag 非 semver 时回退解析 name 的逻辑不变，新版本号格式由 `parseSemver` 支持。
- 文档：`docs/ARCHITECTURE.md`、`docs/OPERATIONS.md` §2.2、`docs/API.md` 的版本号与判新描述更新；`README` / ADR 索引加本条、[ADR-0055](0055-rolling-prerelease-dev-sha-version.md) 标「已被 ADR-0056 取代」、[ADR-0054](0054-rolling-prerelease-version-ci-computed.md) 的 minor+1 部分标被取代。

## 被否的备选

- **保留 [ADR-0055](0055-rolling-prerelease-dev-sha-version.md)（minor+1 + dev.sha + 标识不同即更新）**：基线靠抬升脱节实际进度、判新无序（sha 改写也触发、无新提交无法表达不更新）。否——本 ADR 的根本动因。
- **基线 +1 但用提交距离序号**：仍倒挂（`0.18.0-dev` 早于 `0.18.0` 出现），且与 JianVideo 不一致。否，基线不 +1。
- **移动 tag 保留 `prerelease` 名**：用户要求对齐 JianVideo 改 `dev`。改 `dev`。
- **不收敛脚本、保留 CI 内联计算**：本地无法复现 CI 版本号、调试在线更新必须推 CI。否，收敛到 `scripts/dev-version.sh`。
