# ADR-0028：放开控制面对 agent 自身目录的托管拦截，自我保护下沉到 agent observe-only

**状态**：已接受

## 背景

文件树托管（通道B，[ADR-0010](0010-file-tree-hosting-blob-channel.md)）的镜像落盘根 = 插件 `plugins/`，配置导入（FR-38 上传 / FR-39 反向抓取）共用 `service.normalizePath` 做入库前路径校验。

此前为防"agent 自我污染"加过一道控制面闸（commit `e7a0517`）：`normalizePath` 把顶段为 `BeaconAgent`（bukkit 壳）/ `BeaconAgentProxy`（bungee 壳）的 path 直接拒为 `INVALID_PATH`——担心运维误把 `BeaconAgent/config.yml` 之类 agent 自管文件塞进有效树后，下游 agent 按相对路径覆写自身 dataFolder、污染身份 / 快照。该闸只散落在 specs 与代码注释里，**未单独立 ADR**。

但真机 E2E（2026-06-22）暴露这道闸与 FR-39 直接相撞：**反向抓取对任何装了 agent 的在线服 100% 失效**。根因——agent 读盘 `PluginsTreeReader.read(plugins/)` 遍历整个 `plugins/`，必然带上自己的 `plugins/BeaconAgent/`（含 `config.yml` / `effective-config.snapshot.json` / `file-tree.applied.json` 三个文本文件）；这些被 ingest 上传后命中控制面保留目录闸，而 `FileService.Import` 是"任一不合法即整批拒绝"→ 整个 ingest 400、命令转 `failed`、零文件落库。FR-38 上传一份含 agent 自身目录的真实 `plugins/` 同样整批被拒。

两个能力（FR-38/FR-39）因此退回"开发中（归真）"。

## 决策

**放开控制面 `normalizePath` 对 agent 自身目录顶段（`BeaconAgent` / `BeaconAgentProxy`）的拒绝**，允许这些 path 被托管 / 导入 / 反向抓取入库。自我保护**不在控制面做**，下沉到 agent 侧一道闸：

1. **控制面**：`normalizePath` 移除 `reservedAgentSelfDirs` 拦截。**穿越（`..`）/ 绝对路径 / 反斜杠 / 空仍硬拒**——安全边界（防落盘逃逸 dataFolder 之外）不退化。
2. **agent（唯一自我保护闸，observe-only）**：`FileTreeApplier` 对顶段命中壳层注入的 `protectedSegments`（bukkit 注 `BeaconAgent`、bungee 注 `BeaconAgentProxy`，core 不硬编码、守 [ADR-0005](0005-agent-transport-codec-abstraction.md)）的 path **只观测不写回**——既不取内容、不落盘、也不删除，并打 WARN；但**仍写入 applied 清单、整轮视为已收敛**（避免每轮长轮询见到这些条目又重试造成 churn）。即"自身目录可被控制面托管为事实，但 agent 永不让它覆写自己的运行目录"。

## 理由

- **身份真源已迁移，前提消失**：[FR-41](../PRD.md)（env 注入，[ADR-0014](0014-downstream-identity-source-direction.md) 方向）落地后，agent 的 `identity.server-id` 等接入身份**优先取自环境变量**，hosted `config.yml` 不再是身份真源。即便自身目录被托管，也不构成身份污染——原拦截理由不再成立。
- **fail-static 不破、且更稳**：对**通道B 文件树同步 / 导入 / 反向抓取**这条链，自我保护由 agent `FileTreeApplier` 的 observe-only 兜底（守 [ADR-0010](0010-file-tree-hosting-blob-channel.md) 决策5），是该链上"防覆写自身"充分的一道闸；控制面那道 `normalizePath` 闸对该链冗余且有害（误杀整批 ingest）。把保护放在最接近落盘处，比在入库端做"猜测性拦截"更准。
  - **范围限定**：本 ADR 只涉 `normalizePath` 把守的通道B 文件树 / 导入 / 反向抓取链。三方覆盖集（FR-15）是另一条独立通道，目标根由 `filetree.ValidateTargetRoot`（控制面）+ agent `TargetRootSecurity` 把守、**不经 `normalizePath`、也无 observe-only 守卫**——其"是否允许把覆盖集目标根指向 agent 自身目录"属该通道既有策略（需显式 admin 指定 targetRoot），不在本次放开范围内、本 ADR 不改其行为。
- **一处后端改动同时修好 FR-38 + FR-39**，无需改 agent / 前端、无需重打 jar（agent observe-only 闸 commit `dcbbd94` 已在）。

## 后果

- 控制面库可能存下 agent 自身目录的文件对象（如 `BeaconAgent/config.yml`）作为**托管事实**；下游 agent 对这些 path observe-only、永不回写自身 dataFolder。
- 运维以"现网某台为模板"反向抓取 / 上传整份 `plugins/` 时不再被自身目录整批拒绝。
- 关联文档同步：[ADR-0010](0010-file-tree-hosting-blob-channel.md) 决策5（agent fail-static）**仍有效、不取代**——本 ADR 只取消"控制面入库前拦截自身目录"这一未立 ADR 的旧裁决，并把它记录为正式决策；specs `file-tree-hosting.md` §3.1「受保护路径」、`config-import-*`、`API.md` 文件错误码随之更新。

## 备选方案

- **agent 侧读盘时过滤自身目录**（最初设想）：让 `PluginsTreeReader` 反向抓取时排除 `BeaconAgent/`。否决——只修 FR-39 不修 FR-38（上传仍会含自身目录被拒），且把"哪些是自身目录"的判断散到读盘侧；不如统一在落盘闸 observe-only 收口。
- **保留控制面闸、只在 ingest 端跳过自身目录而非整批拒绝**：把"整批拒绝"改成"丢弃自身目录条目继续"。否决——仍是控制面对自身目录做猜测性特判，且静默丢弃运维上传的文件违背"导入即托管"语义；自我保护放 agent observe-only 已足够。
- **维持现状（保留拦截）**：FR-38/FR-39 对任何装 agent 的在线服不可用。否决。
