# 功能规格：beacon 主命令帮助完善（FR-54，增强 FR-17）

> 状态：开发中　·　关联 PRD：FR-54　·　分支：feature/fr-54-beacon-command-help

## 1. 背景与目标

FR-17 已给 agent 落地本地运维根命令 `/beacon`（status/reload/reconnect/resync），但帮助与可发现性偏弱：
无 `help` 子命令、无参仅打印一行 USAGE、错参 / 未知子命令落到 TabooLib 默认的中英双语 generic 提示（与项目"日志 / 提示全中文"不一致）。本需求（P2，增强 FR-17）只补**命令帮助与友好提示**，**不加任何新运维能力**（守 scope-discipline）。

## 2. 需求（要什么）

范围内：
- `/beacon help` 子命令：打印含权限提示的标题 + 用法首行 + 各子命令一行用法。
- 无参 `/beacon`：打印完整用法（首行 + 各子命令用法），而非仅一行。
- 未知子命令 / 错参 `/beacon xxx`：回中文友好提示（回显出错片段 + 完整用法），取代 TabooLib 默认中英双语 generic 文案。
- 各子命令注册带 `description`，提升补全 / 帮助可发现性。

不做（范围外）：
- 不新增 / 改动任何运维动作（status/reload/reconnect/resync 行为不变）。
- 不做远程下发命令（依赖 FR-11 鉴权，本期不做）。
- 不引入新依赖、不改命令注册框架。

## 3. 设计（怎么做）

### 文案单一来源（core，TabooLib-free，守 [ADR-0005](adr/0005-agent-transport-codec-abstraction.md)）

所有帮助 / 提示文案仍收敛在 `agent-core` 的 `OpsCommandText`（纯对象、无平台依赖、双端壳共用）：
- 新增 `Subcommand(name, usage)` 值对象 + 权威清单 `SUBCOMMANDS`（顺序即展示顺序）；`USAGE_HEADER` / `USAGE_LINES` / `HELP_LINES` 均由它派生，新增子命令只改一处（杜绝复制粘贴、防文案漂移）。
- `incorrectInputLines(input)`：错参 / 无参友好提示构造——非空片段时先回 "未知子命令：<片段>"，再附完整用法；无参（null/空）只给用法、不报"未知"。

### 壳层接线（agent-bukkit / agent-bungee，对称）

- 复用既有 TabooLib `command("beacon", permission = "beacon.admin") { ... }` DSL；新增 `literal("help")` 子命令调 `HELP_LINES`，各 literal 补 `description`。
- 用 `CommandBase.incorrectCommand { sender, context, _, _ -> ... }`（TabooLib 内置失配回调）覆盖默认提示：经公共入口 `context.self()` 取触发失配的输入片段（极端边界取不到则只给用法），调 `incorrectInputLines` 回中文。
- 无参分支（根 `execute`）改打印 `USAGE_LINES`（多行）。命令体不碰平台运维动作的线程模型变化，help/usage 为纯回显、与既有 status 同形。

## 4. 任务拆分
- [x] core：`OpsCommandText` 补 `Subcommand`/`SUBCOMMANDS`/`USAGE_HEADER`/`HELP_LINES`/`incorrectInputLines`，`USAGE_LINES` 由清单派生
- [x] core 单测：用法覆盖全子命令、help 含权限、错参回显 + 用法、无参不报未知、清单与壳层对齐
- [x] 壳：双端补 `help` 子命令 + `incorrectCommand` 中文提示 + 各 literal `description`
- [x] 文档同步：PRD 状态、ARCHITECTURE §8、CHANGELOG

## 5. 验收标准
- `OpsCommandText` 单测全绿：`USAGE_LINES`/`HELP_LINES` 逐条覆盖 status/reload/reconnect/resync/help；错参提示含出错片段且附完整用法；无参不报"未知子命令"。
- 子命令清单与双端壳注册的 literal 集合一致（防漂移断言）。
- 双端壳编译通过；help/usage/错参均为中文、不依赖 FR-11。

## 6. 风险 / 待定
- `incorrectCommand` 回调取出错片段经公共 `context.self()`（`realArgs` 为 TabooLib internal、跨模块不可见）；极端边界 `self()` 取不到时降级为只给用法，不影响主路径。
- 命令交互行为（补全 / 帮助渲染）依赖 MC 服务端，纯文案逻辑已单测覆盖，端到端表现待真机 / E2E 验证。
