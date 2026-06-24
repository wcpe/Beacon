# 功能规格：控制面连接状态指示 + 自动重连

> 状态：开发中　·　关联 PRD：FR-78　·　分支：feature/fr-78-connection-status

## 1. 背景与目标

运维重新部署 beacon-cp（重启控制面进程）时，管理台前端不会主动告知连接已断——页面上的数据停在断开前的快照，运维往往要手动刷新页面才发现「其实早掉线了」。本功能在管理台全局加一个连接状态指示，断开时弹顶部横幅提示「正在重连」，控制面恢复后自动重连并刷新关键数据，免去手动刷新。属 P2（运维体验优化）。**纯前端**，不改后端。

## 2. 需求（要什么）

- 全局连接状态指示：页眉常驻一个小灯/角标，绿=已连接、红=已断开。
- 断开横幅：连接中断时，主内容列顶部出现横幅「控制面连接中断，正在重连…」，连上后自动消失。
- 自动重连 + 数据刷新：控制面恢复后，自动恢复轮询，并触发 react-query 失效（invalidate）以拉取最新数据，运维无需手动刷新。
- 范围内：复用既有的 react-query 轮询作为「心跳」判断连通态；只读地从查询状态派生 online/offline；横幅与小灯的展示；恢复时 `invalidateQueries`。
- 不做（范围外）：不新增任何后端端点 / 浏览器 SSE / WebSocket；不改 `internal/**`；不引入新依赖；不做断线重连退避策略调优（react-query 默认轮询即承担重试）；不做断线期间的离线写队列。

## 3. 设计（怎么做）

### 3.1 连通态从何而来（关键决策）

PRD FR-78 行原文写「前端用现有 SSE」，但本仓库**管理台（浏览器侧）并无面向前端的 SSE**：唯一的 SSE 是 `agent↔控制面`（`GET /beacon/v1/agent/stream`，见 [docs/specs/sse-server-push-transport.md](sse-server-push-transport.md)），不面向浏览器。管理台所有数据走 **react-query 轮询**（如 `SystemHeader` 每 5s 拉 `/admin/v1/system/status`）。

因此「连通态」**从既有轮询查询的成功 / 失败派生**，而非另起一条传输通道：

- `useConnectionStatus()` 读 react-query 缓存里 `system-status` 查询的状态：
  - 该查询 `isError`（轮询请求抛错，含网络断开 / 控制面进程没起来）→ 判定 `offline`。
  - 该查询有成功数据且非错误 → 判定 `online`。
  - 尚未首次返回（`isLoading` 且无错误）→ 判定 `connecting`（不弹断开横幅，避免首屏闪红）。
- `system-status` 查询本身已每 5s 轮询且 `SystemHeader` 常驻挂载（在 `Layout` 内），它天然是全局心跳；hook 不自建轮询，只读其状态，避免重复请求与新传输通道（YAGNI）。

> 不新建浏览器 SSE 是刻意的：那需要后端新端点，违反「纯前端」，且属范围镀金。轮询派生足以满足「断开提示 + 恢复刷新」。

### 3.2 自动重连 + 刷新

- react-query 轮询在控制面恢复后下一周期自然成功 → hook 状态从 `offline` 翻回 `online`。
- 在 `useConnectionStatus` 内用 `useEffect` 监听「`offline → online` 的边沿」，触发一次 `queryClient.invalidateQueries()`（不带 key，失效全部查询）使各页面立即重取最新数据。仅在边沿触发，不在每次 online 重复刷。

### 3.3 UI 接入（Layout）

- `Layout` 顶部（`SystemHeader` 之上、或并入页眉区）渲染：
  - 小灯：`online` 绿点、`offline` 红点、`connecting` 灰点。
  - `offline` 时在主内容列顶部加一条横幅（醒目底色），文案「控制面连接中断，正在重连…」。
- 文案走 i18n（`connection.*`），不硬编码。

### 3.4 模块

- 新增 `web/src/hooks/useConnectionStatus.ts`：纯读 react-query 状态 + 边沿刷新；返回 `{ status: 'online' | 'offline' | 'connecting' }`。
- 改 `web/src/components/Layout.tsx`：挂横幅 + 小灯。
- 改 `web/src/i18n/locales/zh-CN.ts`：加 `connection.*` 文案。

不涉及架构决策（不引入新传输 / 新模式），故不新增 ADR。

## 4. 任务拆分

- [ ] 测试先行：`useConnectionStatus` hook 单测（online/offline/connecting 派生 + 恢复时触发 invalidate）。
- [ ] 测试先行：`Layout` 断开横幅 + 小灯渲染单测。
- [ ] 实现 `useConnectionStatus.ts`。
- [ ] `Layout.tsx` 接入横幅与小灯。
- [ ] i18n `connection.*` 文案。
- [ ] 文档同步：PRD 状态（开发中）、CHANGELOG 未发布段、本规格。

## 5. 验收标准

- 控制面轮询请求失败时，管理台顶部出现「控制面连接中断，正在重连…」横幅且小灯转红。
- 控制面恢复（轮询请求重新成功）后，横幅消失、小灯转绿，并自动触发一次全量查询失效（无需手动刷新页面即见新数据）。
- 首屏加载（尚无数据、未出错）不误弹断开横幅。
- `cd web && pnpm test` 全绿（含新用例）、`pnpm build` 通过。

## 6. 风险 / 待定

- PRD 行原文「前端用现有 SSE」与实现「复用现有轮询派生」措辞不一致：实质都是「复用既有连接、不新建传输」，已在 §3.1 说明取舍。PRD 行据此微调措辞。
- 单测无法覆盖真实浏览器中重部 beacon-cp 的端到端表现 → 标「待真机浏览器验」。
