# ADR-0046：rc 预发布渠道（语义化 rc 号 + prerelease 标志）

**状态**：已接受

## 背景

[ADR-0007](0007-versioning-and-release-channels.md) 定义了「稳定 / 快照」两条发布渠道，其中**快照渠道**为「main 每次推送 → 构建 `<VERSION>-SNAPSHOT+<sha>` → 滚动发 `latest` 预发布」。实践中该模型有两个问题：

- **滚动 `latest` 不可定位**：滚动覆盖的 `latest` 与具体提交一一对应靠 `+<sha>`，但发布物本身无稳定语义版本，消费端（控制面在线自更新 FR-97/FR-99）难以按「最新预发布版本号」做有序比较与提示。
- **与按 tag 触发的发布模型割裂**：稳定渠道是 tag 驱动（`vX.Y.Z`），快照是 push 驱动，两套触发路径维护成本高，且 main 每推就发不利于「攒一批可控试用」。

控制面在线自更新（FR-97~FR-101）需要一条**语义化、可有序比较、可被标准 GitHub API 区分**的预发布渠道，让正式版与预发布版并存且各自可被消费端精确取到最新版。

## 决策

### 预发布改用语义化 rc 号（取代 ADR-0007 快照渠道）

- 预发布版本用**语义化 rc 号** `vX.Y.Z-rc.N`（如 `v0.15.0-rc.1`），**不再用滚动 `-SNAPSHOT+<sha>` 发 `latest`**。
- **只做 rc，不做 nightly / beta**（YAGNI，用户已定）：不在 main 上每推送滚动发预发布。
- rc 由**显式打 tag** 触发，与稳定版同为 tag 驱动，两套触发路径统一为「打 tag → CI 构建发 Release」。

### 触发与命名（CI）

- `prerelease.yml` 触发 glob `v[0-9]+.[0-9]+.[0-9]+-rc.[0-9]+`，与 `release.yml` 的正式三段 glob `v[0-9]+.[0-9]+.[0-9]+` **互斥不重叠**（正式 glob 不含连字符后缀，rc glob 必带 `-rc.<数字>`）。
- 推送 rc tag → 复用与正式版同一套构建（5 平台原生矩阵含 linux-arm64、双端 jar、汇总 SHA256），经 `softprops/action-gh-release` 置 `prerelease: true`。
- 版本注入 `-ldflags` 用 **tag 去 v 后的完整串（含 `-rc.N`）**，故 rc 产物文件名与内嵌版本号自带 rc 标识、与正式版可区分。

### tag↔VERSION 校验放宽

- 可复用 workflow 的校验改为「tag 去 v 前缀、再剥 `-rc.<数字>` 后缀后 == 根 `VERSION`」；**严格只认 `-rc.<数字>`**（拒任意其它后缀防绕过），正式版无后缀路径行为不变。
- 含义：**rc 期间 `VERSION` 已指向目标正式版**（如要发 `0.15.0`，`VERSION` 即 `0.15.0`），rc tag 为 `v0.15.0-rc.1/2/…`，正式发布时打无后缀 tag `v0.15.0`。

### release notes

- 预发布的 notes 在 `generate_release_notes` 自动生成基础上**前置固定中文头**：「⚠ 预发布版本（rc）：用于试用验证，可能不稳定，勿用于生产」。正式版 body 留空、纯自动生成。

### 渠道判定（零自研端点）

- **正式渠道** = GitHub API 最新**非 prerelease** release；**预发布渠道** = 最新 **prerelease** release。
- 这是消费端（FR-99 在线更新检查）的判定行为；本渠道只保证 release 侧可被标准 GitHub API（`prerelease` 布尔）精确区分，不另起自研端点。

### 与 sdd-publish-snapshot 技能的冲突

- 全局技能 `sdd-publish-snapshot` 基于 ADR-0007 的旧**快照模型**（main 推送滚动发 `latest`、产物标 `-SNAPSHOT+<sha>`），与本 ADR 的 rc 模型冲突。
- 该技能属全局插件、**不在本仓库内擅改其正文**；本 ADR 在此标注冲突，技能取舍待用户单独定。本仓库的预发布以本 ADR 的 rc 流程为准。

## 后果

- 预发布版本号语义化、可有序比较（含 `-rc.N` 预发布序），消费端可按「最新 prerelease」精确取到、有序提示。
- 正式版与 rc 并存：稳定渠道走无后缀 tag，rc 渠道走 `-rc.N` tag，GitHub API `prerelease` 布尔天然区分，无自研端点。
- 构建逻辑单点维护：`release.yml` 与 `prerelease.yml` 同为薄触发壳，复用同一 `_build-release.yml`（DRY）。
- 不再有 main 推送滚动发 `latest` 的行为；想让人试用须显式打 rc tag。

## 备选方案

- **保留 ADR-0007 滚动 `latest` 快照**：发布物无稳定语义版本、不利消费端有序比较，且与 tag 驱动割裂。取代之。
- **加 beta / nightly 通道**：超出当前需要（YAGNI，用户已定不做）。否决。
- **自研「最新预发布」查询端点**：GitHub 标准 release API 的 `prerelease` 布尔已足够区分，无需自研。否决。
