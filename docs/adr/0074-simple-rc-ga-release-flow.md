# ADR-0074：简化 RC/GA 发布流程

**状态**：已接受（2026-07-20）

## 背景

ADR-0072 与 ADR-0073 建立了不可变 RC、GA 同 commit 晋级和 GA-only 在线更新等正确基础，但同时把发布流程扩展为 P10 专属准入、长期审批、Central/OCI 晋级、候选清单和完整证据链。当前项目决定只保留可直接执行、可重复校验的简单 RC/GA 流程，不再维护这些额外发布层。

## 决策

### 1. 发布只包含 RC 与 GA

- 目标版本由根 `VERSION` 唯一确定，格式为 `X.Y.Z`。
- RC tag 使用 `vX.Y.Z-rc.N`，其中 `N` 为从 1 开始递增的十进制整数。
- GA tag 使用 `vX.Y.Z`。
- 开发构建不是 Release，也不能直接晋级为 GA。

### 2. RC 不可变

- 每个 RC 固定一个 commit 和一次构建出的产品资产。
- RC 发布后不得移动 tag、覆盖资产、删除后重建或补传产品文件。
- 候选失败、源码改变或任何产品资产改变时，必须从新 commit 创建下一个 RC。
- RC 资产目录必须包含 `SHA256SUMS.txt`；清单必须完整覆盖除自身外的全部产品资产。

### 3. GA 与最终 RC 使用同一 commit 和原样资产

- GA tag 与最终 RC tag 必须指向同一 commit。
- GA 只复制最终 RC 已发布的产品资产，不重新编译、打包、重建、重签或替换产品文件。
- 晋级前后必须逐项核对产品资产的文件名、大小和 SHA-256。
- GA 不得新增另一套可执行或可安装产品资产。
- 真实 GA 不额外要求 GitHub Environment 人工审批；`release` job 在 master 上对已公开 RC 完成身份与资产校验后直接原样晋级并公开。
- 本地验证或本地预备不写入真实 tag/Release，不构成 GA；截至 2026-07-20，真实 RC 与 GA 均尚未发生。

### 4. 在线更新只消费 GA

- 标准在线更新只接受非草稿、非 prerelease 且 tag 严格匹配 `vX.Y.Z` 的 GA Release。
- RC 与开发 Artifact 不进入自动更新候选；需要验证 RC 时必须显式下载对应候选。
- 历史 `prerelease` 更新设置归一为 `stable`。

### 5. 使用通用 Make 入口校验

发布校验统一使用以下入口，不再使用 P10 专属脚本：

- `make release-test`：运行发布校验工具的通用契约测试。
- `make release-check RELEASE_RC_TAG=vX.Y.Z-rc.N`：校验 `VERSION`、RC/GA tag 和 GA 工作流不构建产品资产。
- `make release-verify-rc RELEASE_RC_TAG=vX.Y.Z-rc.N RELEASE_ASSETS_DIR=<资产目录> RELEASE_RC_COMMIT=<40 位提交 SHA>`：校验 RC 身份、资产闭集和 SHA-256。
- `make release-verify-ga RELEASE_RC_TAG=vX.Y.Z-rc.N RELEASE_RC_ASSETS_DIR=<最终 RC 基准资产目录> RELEASE_ASSETS_DIR=<GA 目标资产目录> RELEASE_RC_COMMIT=<40 位提交 SHA> RELEASE_GA_COMMIT=<40 位提交 SHA>`：逐项比较 RC 基准资产与 GA 目标资产，并确认 RC/GA commit 相同。两个资产目录必须不同，禁止复用同一路径；`RELEASE_GA_TAG` 默认取 `v$(VERSION)`，也可显式传入。

## 与既有 ADR 的关系

1. **部分取代 ADR-0072**：保留不可变 RC、GA 与最终 RC 同 commit、产品资产原样晋级和 SHA-256 校验；取代其中 P10 专属 N-1 准入、四平台硬门、供应链门、Central/OCI 晋级、候选或最终清单、稳定窗口、风险分级和证据链要求。
2. **部分取代 ADR-0073**：保留通用 RC/GA 生命周期、开发产物不直接晋级与 GA-only 在线更新；取代分层准入、后置创建 tag 的强制编排、GitHub Environment 人工审批闸门及对复杂晋级流程的依赖。
3. ADR-0072 与 ADR-0073 正文保留为历史记录；当前发布流程以本 ADR 为准。
4. 本 ADR 不改变业务变更单自身的权限、审批或审计语义。

## 理由

1. 不可变 RC 与 SHA-256 已足以固定被验证和被晋级的产品资产。
2. GA 同 commit 且不重建，可以避免候选通过后正式资产发生漂移。
3. 通用 Make 入口让本地与 CI 使用同一套最小校验，无需维护版本阶段专属脚本。
4. GA-only 在线更新保持生产更新来源单一，不会误装 RC 或开发产物。
5. 删除非必需的外部发布层和过程性记录，可以降低维护成本并减少流程失配。

## 后果

### 正面

- 所有 SemVer 版本使用同一套 RC/GA 规则。
- 失败处理明确：任何资产变化都创建新的 RC。
- GA 晋级只依赖 commit 一致性、资产闭集和 SHA-256，容易本地复验。
- 在线更新继续只面向正式 GA。

### 约束

- RC 漏传或传错资产时不能原位修补，必须发布新的 RC。
- 真实 GA 不再依赖 GitHub Environment 人工审批；本地验证和本地预备仍不能写入真实 tag/Release，不能冒充已发布 GA。
- 发布者必须保留最终 RC 的完整资产目录与 `SHA256SUMS.txt`，供 GA 原样复制和复验。
- 已发布 GA 发现问题时必须发布后续版本，不能覆盖原版本资产。

## 否决方案

### 方案 A：GA 从最终 RC 的同 commit 重新构建

同 commit 不保证同字节，会重新引入工具链、依赖和时间戳差异。否决。

### 方案 B：允许覆盖 RC 或 GA 资产

覆盖会破坏已下载资产与校验和之间的对应关系。否决。

### 方案 C：继续维护 P10 专属发布体系

版本阶段专属脚本、审批、外部晋级目标、清单和证据链超出当前发布需求，且容易与通用流程漂移。否决。
