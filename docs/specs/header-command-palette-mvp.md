# 功能规格：页眉全局搜索（命令面板 MVP）

> 状态：开发中　·　关联 PRD：FR-193　·　分支：main（对齐中间版 0.31.x）

## 1. 背景与目标

管理台双段页眉（FR-187）已就位，但段 2 搜索按钮仍为 `disabled` 占位。运维在十余个页面间跳转仍依赖侧栏点选。FR-193 交付**最小可用命令面板**：页眉入口 + `Ctrl/Cmd+K` 唤起，至少导航分组可键盘选择并回车跳转。思路复用 Legacy FR-83（`docs/specs/command-palette.md`），落点在 `apps/web` 页眉而非旧 `web/`。

## 2. 需求（要什么）

- **唤起**：页眉搜索按钮可点；全局 `Ctrl/Cmd+K` 打开；`Esc` 关闭。
- **导航分组（MVP 必达）**：列出 `routes.tsx` 全站导航页，按大域分组；输入即时过滤标题；方向键选择、回车 `navigate` 并关闭。
- **可选增强（本批一并交付）**：
  - 输入像 `serverId` 的关键词时提供「在服务器中搜索」快捷项 → `/servers?keyword=`
  - 常见审计动作快捷项 → `/audits?action=`
- 范围内：纯前端；导航项静态；无新依赖（Dialog + 自实现键盘导航）。
- 不做：后端全文检索、通知写操作、配置/文件远端检索、最近访问记忆、命令执行类动作。

## 3. 设计（怎么做）

- 纯函数 `apps/web/src/features/command-palette/items.ts`：`buildNavItems` / `filterItems` / `groupItems`。
- 组件 `apps/web/src/shell/command-palette.tsx`：`Dialog` 模态；输入框 + 分组列表；`activeIndex` 键盘导航。
- 页眉 `header.tsx`：启用搜索按钮，控制 `open`；`app-shell` 或 `header` 挂 `keydown`（Ctrl/Cmd+K）。
- i18n：`common.commandPalette.*`；导航标题复用 `nav.*`。

## 4. UX / 交互

- 用户任务：运维快速跳转到目标页或带参深链。
- 进入路径：页眉搜索图标 / `Ctrl+K` / `Cmd+K`。
- 操作闭环：打开 → 输入过滤 → 回车跳转 → 面板关闭。
- 状态：空查询展示全量导航；无匹配展示空态文案；不依赖远程数据（MVP 不因接口失败白屏）。

## 5. 任务拆分

- [x] 写规格（本文）
- [x] 纯函数 + 单测
- [x] CommandPalette 组件 + 页眉/全局快捷键接线
- [x] i18n 文案
- [x] 文档：PRD FR-193 状态 → 开发中

## 6. 验收标准

- 页眉搜索可点；`Ctrl/Cmd+K` 打开；`Esc` 关闭。
- 至少导航分组；键盘上下 + 回车跳转正确路由。
- 可选深链：`/servers?keyword=`、`/audits?action=` 可用。
- 相关 vitest 绿；`tsc` 通过。

## 7. 风险 / 待定

- 深链参数目标页不消费时仍导航成功（尽力定位）。
- 与输入框内 Ctrl+K 冲突：面板为全局监听；输入框聚焦时仍允许唤起（模态覆盖）。
