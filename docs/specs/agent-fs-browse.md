# 功能规格：agent 只读交互式文件浏览

> 状态：开发中　·　关联 PRD：FR-109　·　分支：feature/fr-109-agent-fs-browse

## 1. 背景与目标

配置工作台（FR-111）要做成双面板 Xftp 式交互：左受管配置树、右某在线服**实时浏览**真实落盘的 `plugins/`。
运维点一个目录才列它的子项、点一个文件才读它的内容，像在 Xftp 里浏览远端文件系统那样。

现有反向抓取（FR-39/FR-58，见 ADR-0027/0037）是**一次性批量 scan**：把整棵 `plugins/` 树元信息扫全回传成清单，
服务「整盘快照入库」。用它撑交互浏览不合适——每展开一目录就重扫整树、大目录拉全表，非惰加载。

本特性（FR-109）给 agent-core 新增三个**只读惰加载浏览原语**，作为 FR-110/FR-111 的底座。属第二期（P2）。
本分支**只做 agent 侧能力**，不做 FR-110 控制面端点 / 命令通道接入。

## 2. 需求（要什么）

agent-core 新增三个**只读**浏览原语（依据 ADR-0049 决策 1）：

- **① 懒列目录**：给定相对路径，列出其**直接子项**（目录 / 文件，附 名称 / 大小 / 是否目录 / 是否文本），不递归整树；大目录**分页**（offset/limit，带「是否还有更多 / 总数」）。
- **② 读文件树**：按需展开某子树，**逐层有界**（限定展开深度、节点数上限），非整盘一次拉全。
- **③ 读单文件内容**：读指定**文本**文件内容，受单文件大小上限约束（复用反向抓取阈值常量），排除 `.jar` / 二进制。

范围内：

- core 纯函数：相对路径字符串安全前置闸（复用 `PluginsPathGuard`）、文本/二进制按名启发判定、分页/有界计算。
- core FS 边界（java.nio，非平台 API）：以 `plugins/` 真实根为基准的 Path 级容纳 + 符号链接逃逸判定，懒列 / 读子树 / 读单文件。
- 平台壳（bukkit/bungee）`PlatformAdapter` 新增默认方法委托 core FS 边界，根 = `pluginsBaseFolder()`。

不做（范围外）：

- FR-110 控制面只读端点 / `agent_command` 命令通道接入 / SSE 唤醒 / 审计（另分支）。
- 任何写盘 / 改盘能力（纯只读）。
- agent 自调度浏览（浏览由 FR-110 命令触发；本分支只提供被调用的原语）。

## 3. 设计（怎么做）

依据 [ADR-0049](../adr/0049-agent-fs-browse.md)（逐条落实，本规格不重复决策正文）。

新增 core 模块 `top.wcpe.beacon.agent.core.browse`：

- **`FsBrowseLimits`**：浏览专属上限常量——单页子项上限、读子树深度上限 / 节点上限；单文件内容上限复用 `PluginIngestLimits.MAX_FILE_BYTES`。
- **`FsBrowseModels`**：只读回传数据类——`BrowseEntry`（name / relPath / dir / size / text）、`DirListing`（entries / offset / limit / total / hasMore）、`TreeNode`（递归子树）、`FileContent`（relPath / content / truncated）。文本/二进制按名启发判定（复用 `PluginsTreeFilter` 的二进制扩展名口径，抽为可共用的纯函数）。
- **`FsBrowseReader`**：FS 边界（java.nio），与 `PluginsTreeReader` 同源安全口径——root `toRealPath()` 为基准、每个候选解析真实路径后 `startsWith(rootReal)`（禁符号链接逃逸）、不跟随目录链接下降。三方法：
  - `listDir(root, relPath, offset, limit)`：解析 relPath 落在 root 内 → 列直接子项（稳定按 目录优先 + 名称排序）→ 分页切片返回 `DirListing`。
  - `readTree(root, relPath, maxDepth)`：从 relPath 起逐层展开，受 `maxDepth` 与节点上限有界，返回 `TreeNode`。
  - `readFile(root, relPath)`：解析单文件 → 排除目录 / `.jar` / 二进制（NUL + 非法 UTF-8）→ 受单文件上限读取（超限 `truncated=true` 不读全文）→ 返回 `FileContent`；越权 / 不存在 / 二进制 → 返回 null（不读、不回传）。
- **`PlatformAdapter`** 新增三个默认方法（默认空/null 实现，桩不开放），bukkit/bungee 覆盖委托 `FsBrowseReader`，根 = `pluginsBaseFolder()`，**仅在 async 线程调用**（守 ADR-0049 决策 5 / 架构不变量 §5）。

安全红线（ADR-0049 决策 2-8）：限 `plugins/` 根 · path traversal 强校验（字符串闸 `PluginsPathGuard` + Path 级 `normalize().startsWith` + 符号链接解析后仍在根内 + 拒 `..`/绝对/UNC/盘符/Windows 保留名）· 纯只读不写盘 · async 不碰 MC 主线程 · 大目录分页 / 子树有界 / 单文件有界 · 排除 jar / 二进制 · fail-static · core 不碰平台 IO（守 ADR-0005，FS 边界用 java.nio 非平台 API，HTTP/JSON 不涉及）。

## 4. 任务拆分

- [x] PRD §4 FR-109 状态 计划→开发中
- [x] `FsBrowseLimits` / `FsBrowseModels`（纯函数 + 数据类）
- [x] `FsBrowseReader`（FS 边界：懒列 / 读子树 / 读单文件，path traversal + 符号链接逃逸校验）
- [x] `PlatformAdapter` 新增三默认方法 + bukkit/bungee 覆盖委托
- [x] 单测：path traversal 各类越权被拒、大目录分页、读子树有界、单文件超限不读全文、jar/二进制排除、只读性
- [x] 文档同步：ARCHITECTURE（agent 新增浏览能力）、CHANGELOG（agent 后端能力 UI 未接 → 无用户可见变更，不记）
- [x] agent 单测绿 + 双端 jar build 绿（真机浏览链路待真机验）

## 5. 验收标准

- 列目录 / 读文件限 `plugins/` 根，拒 `../` 遍历（穷举单测：`..`/绝对/UNC/盘符/保留名/符号链接逃逸均被拒、不读越权内容）。
- 只读：浏览不创建 / 不修改 / 不删除任何文件（测试断言读后盘上无新增/变更）。
- async：壳层委托方法仅在 async 线程调用（注释 + 设计约束；与既有 readPluginsTree 同口径）。
- 大目录分页：列目录超单页上限按 offset/limit 切片，带 hasMore / total。
- 读子树有界：受 maxDepth + 节点上限，不整盘一次拉全。
- 单文件有界：超单文件上限只读到上限不读全文（truncated 标记），`.jar` / 二进制不读。
- 双端 jar build 绿（bukkit/bungee）。
- 真机 agent 浏览链路过（**本分支无真机能力 → 待真机验**，由主控/用户在测试机做）。

## 6. 风险 / 待定

- 浏览原语本分支无命令通道触发（FR-110 未做），单测以直接调用 core 原语验证；真机端到端浏览待 FR-110 接入后验。
- 读子树 / 列目录的分页游标本分支用简单 offset/limit（int 偏移），FR-110/FR-111 若需稳定游标（防并发增删错位）再演进，当前 YAGNI 不预留。
