# 功能规格：开发构建与发布准备标准化

> 状态：已审核 / 实施中（2026-07-20） · 关联 PRD：FR-182 · 架构决策：[ADR-0074](../adr/0074-simple-rc-ga-release-flow.md)

## 1. 背景与目标

FR-182 将开发构建与正式版本发布分开，并为所有 SemVer 版本提供同一套简单 RC/GA 入口：

1. PR 只运行现有质量门，不发布产品资产。
2. `master` 质量门通过后可生成短期 Actions Artifact，仅用于开发验证。
3. 发布准备只修改根 `VERSION` 与 `CHANGELOG.md`。
4. RC 固定一个 commit 和一次构建出的产品资产，发布后不可变。
5. GA 与最终 RC 指向同一 commit，并原样复制经过 SHA-256 校验的产品资产。
6. 在线更新只自动消费 GA。

本规格不建立版本阶段专属的脚本、审批、外部仓库晋级、发布清单或证据链。

## 2. 范围

### 2.1 纳入范围

- PR 与 `master` 的开发构建职责边界。
- 发布准备中的 `VERSION` 与 `CHANGELOG.md` 约束。
- 通用 `vX.Y.Z-rc.N → vX.Y.Z` 发布规则。
- 产品资产闭集、文件名、大小与 SHA-256 校验。
- RC/GA commit 一致性和 GA 禁止重新构建。
- GA-only 在线更新边界。
- 通用 Make 校验入口。

### 2.2 不做

- 不维护版本阶段专属发布脚本或测试入口。
- 不要求阶段专属验收清册、兼容矩阵或稳定窗口作为发布条件。
- 不要求额外的阶段专属人工审批、候选清册、稳定窗口或 GitHub Environment 人工审批闸门。
- 不把外部制品系统作为 RC/GA 晋级前提。
- 不生成候选清单、最终清单或长期证据链。
- 不修改在线更新之外的业务功能。

## 3. 核心不变量

1. 根 `VERSION` 必须且只能包含正式 `X.Y.Z`。
2. RC tag 必须严格匹配 `vX.Y.Z-rc.N`，且 `N >= 1`。
3. GA tag 必须严格匹配 `vX.Y.Z`。
4. RC 发布后不得移动 tag、覆盖资产、删除后重建或补传产品文件。
5. RC 资产目录必须包含 `SHA256SUMS.txt`，并完整覆盖除自身外的全部产品资产。
6. 任何源码或产品资产变化都必须创建新的 RC。
7. GA 与最终 RC 必须指向同一 40 位 commit SHA。
8. GA 产品资产必须来自最终 RC；文件名、大小和 SHA-256 必须逐项一致。
9. GA 阶段不得执行编译、打包、重建或替换产品资产的命令。
10. 标准在线更新只选择严格 `vX.Y.Z` 的 GA，拒绝 RC 与开发 Artifact。

## 4. 开发构建

### 4.1 PR

- 运行仓库现有 Go、Web、Agent 与集成质量任务。
- 不执行产品发布。
- 不创建 tag 或 Release。
- 不上传可被误认为 RC/GA 的产品资产。

### 4.2 `master`

- 只有质量门全部成功后才能生成临时开发 Artifact。
- Artifact 以 source commit 和平台标识区分，并采用短期保留策略。
- Artifact 仅用于开发验证，不是 Release、RC、GA 或在线更新来源。
- 失败或取消的 run 不能作为发布来源。

## 5. 发布准备

发布准备使用独立变更，只修改：

- 根 `VERSION`：目标正式版本 `X.Y.Z`。
- `CHANGELOG.md`：该版本的用户可见变更、迁移说明和已知问题。

不得在发布准备中混入业务功能、缺陷修复、重构或发布工具改造。准备变更合入后，以目标 commit 创建 RC。

## 6. RC 发布

1. 选择已合入的目标 commit。
2. 读取根 `VERSION`，确定 RC tag `vX.Y.Z-rc.N`。
3. 运行现有质量门与一次产品构建。
4. 生成 `SHA256SUMS.txt`，使其完整覆盖本次所有产品资产。
5. 运行通用发布校验。
6. 发布不可变 RC tag 与对应 prerelease 资产。

若任一步失败，或发布后发现资产缺失、哈希错误、源码变化，不得修补原 RC；必须修复后创建 `vX.Y.Z-rc.(N+1)`。

## 7. GA 发布

1. 选择最终 RC。
2. 确认目标 GA commit 与最终 RC tag 指向同一 commit。
3. 从最终 RC 下载完整产品资产和 `SHA256SUMS.txt`。
4. 逐项核对资产文件名、大小和 SHA-256。
5. 在同一 commit 创建 `vX.Y.Z` 正式 tag。
6. 将同一组产品资产原样发布到 GA Release，并再次执行 GA 校验。

GA 阶段不得重新编译、打包、生成或替换产品资产。任何资产变化都必须回到新的 RC。

## 8. 通用 Make 入口

### 8.1 工具契约测试

```bash
make release-test
```

运行通用发布校验工具的契约测试，不依赖版本阶段专属脚本。

### 8.2 版本、标签与 GA 工作流检查

```bash
make release-check \
  RELEASE_RC_TAG=v1.2.3-rc.1
```

`RELEASE_GA_TAG` 默认取 `v$(VERSION)`；需要时可显式传入。该入口校验 `VERSION`、RC/GA tag 格式，并检查 GA job 不包含产品构建命令。

### 8.3 RC 资产检查

```bash
make release-verify-rc \
  RELEASE_RC_TAG=v1.2.3-rc.1 \
  RELEASE_ASSETS_DIR=dist \
  RELEASE_RC_COMMIT=<40 位小写提交 SHA>
```

该入口校验 RC 身份、资产目录闭集与 `SHA256SUMS.txt`。

### 8.4 GA 一致性检查

```bash
make release-verify-ga \
  RELEASE_RC_TAG=v1.2.3-rc.1 \
  RELEASE_RC_ASSETS_DIR=dist/rc-v1.2.3-rc.1 \
  RELEASE_ASSETS_DIR=dist/ga-v1.2.3 \
  RELEASE_RC_COMMIT=<40 位小写提交 SHA> \
  RELEASE_GA_COMMIT=<同一 40 位小写提交 SHA>
```

`RELEASE_RC_ASSETS_DIR` 是只读的最终 RC 基准资产目录，`RELEASE_ASSETS_DIR` 是待发布的 GA 目标资产目录；两者必须是不同目录，禁止复用同一路径。该入口逐项比较两个目录的文件名、大小和 SHA-256，并拒绝 RC/GA commit 不一致。

## 9. 验收标准

| 验收项 | 通过条件 |
|---|---|
| 开发构建与发布分离 | PR 不发布产品资产；`master` 临时 Artifact 不作为 RC/GA 来源 |
| 版本唯一 | 根 `VERSION`、RC tag 与 GA tag 对齐 |
| RC 不可变 | 旧 RC 不移动、不覆盖、不补传；资产变化创建新 RC |
| 产品资产闭集 | `SHA256SUMS.txt` 完整覆盖全部产品资产，无缺失、重复或多余项 |
| GA 同 commit | RC 与 GA 的 40 位 commit SHA 完全相同 |
| GA 原样资产 | 文件名、大小和 SHA-256 逐项相同 |
| GA 不重建 | GA job 不包含编译、打包或产品生成命令 |
| 在线更新 | 只选择严格 `vX.Y.Z` GA，拒绝 RC 与开发 Artifact |
| 通用入口 | `make release-test`、`make release-check`、`make release-verify-rc`、`make release-verify-ga` 均可按用途执行 |

## 10. 失败处理

- `VERSION` 或 tag 格式错误：停止发布，修正后重新校验。
- `SHA256SUMS.txt` 缺失、哈希不匹配、资产缺失或多出未登记文件：停止发布；若 RC 已发布则创建新 RC。
- RC/GA commit 不一致：禁止创建或公开 GA。
- GA 流程出现构建命令：停止发布，改为从最终 RC 下载并复制资产。
- GA 公开后发现缺陷：发布后续版本，不覆盖原 GA 资产。
