# 功能规格：配置 / 文件批量操作

> 状态：开发中　·　关联 PRD：FR-74　·　分支：feature/fr-74-bulk-ops

## 1. 背景与目标

配置中心（FR-1）与文件树托管（FR-14/FR-38）当前只支持逐条删除 / 禁用：运维要批量下线一组配置项或文件对象时，只能一个个点删除，既低效又无法保证一组操作的原子性（中途失败留下半完成态）。本功能（P2，增强 FR-38/FR-1）为配置项与文件对象提供「列表多选 + 批量删除 / 禁用 / 启用 / 导出」，把一批同类操作在**一个事务**内原子完成。

## 2. 需求（要什么）

- 后端：加批量端点，把配置项 / 文件对象的删除 / 禁用 / 启用在**一个事务**内对一批 id 原子完成；每项各记一条领域审计。
- 前端：配置 / 文件列表加多选 checkbox + 批量操作栏（删除 / 禁用 / 启用 / 导出）；空选时禁用按钮；批量删除前轻量二次确认；导出为前端拉选中项内容打包成 JSON 后 Blob 下载（best-effort，无新依赖）。
- 范围内：批量 delete（软删，与单删同义）/ disable（enabled=false）/ enable（enabled=true）；导出走前端既有 `getConfig`/`getFile` 拉内容、客户端打包。
- 不做（范围外）：批量发布 / 回滚 / 改派；跨配置与文件的混合批操作；服务端导出端点（导出纯前端 best-effort）；FR-76 的统一破坏性确认框（本 FR 自带轻量确认，不依赖 FR-76）。

## 3. 设计（怎么做）

分层不破：`router → handler → service → repository`，handler 不碰 GORM。

- **repository**：`ConfigItemRepository` / `FileObjectRepository` 各加 `SetEnabled(id uint, enabled bool) error`（仅对未软删项按 id 置 `enabled`）。软删复用既有 `SoftDelete`。
- **service**：`ConfigService` / `FileService` 各加 `BatchDelete` / `BatchSetEnabled`：在**一个** `db.Transaction` 内遍历 id，逐项软删 / 置 enabled 并写一条领域审计（复用既有 `ActionConfigDelete`/`ActionFileDelete` + 新增 `ActionConfigDisable/Enable`、`ActionFileDisable/Enable`）。任一项不存在即整批回滚（事务语义，全成或全不成）。事务提交成功后按受影响 scope 逐项唤醒长轮询（沿用单操作的 `notify` + `exportGit`）。
- **handler**：新增 `Config.Batch` / `File.Batch`，解析 body `{action, ids}`；非法 action / 空 ids → 400；按 action 分派到对应 service 方法；返回 `{action, count}`。
- **router**：`POST /admin/v1/configs/batch`、`POST /admin/v1/files/batch`（静态路由置于 `{id}` 前）；二者各项在事务内自记专项审计，登记进 `coveredWriteRoutes` 避免兜底中间件双记。
- **前端**：`web/src/api/client.ts` + `types.ts` 加 `batchConfigs` / `batchFiles`；`ConfigsPage.tsx` 新增独立的批量操作面板组件（多选列表 + 批量栏 + 轻量确认），仅动「列表选择 + 批量栏」区域，不碰编辑器单条删除逻辑（减少与 FR-76 的 rebase 冲突）。

## 4. 任务拆分

- [ ] repository：`SetEnabled`（config / file）
- [ ] service：`BatchDelete` / `BatchSetEnabled`（config / file，单事务 + 逐项审计 + 唤醒）
- [ ] handler + router：`POST /configs/batch`、`POST /files/batch`，covered 审计登记
- [ ] 后端测试先行：批量端点集成（delete/disable/enable、空 ids/非法 action 400）
- [ ] 前端：client/types 批量函数 + 批量操作面板 + 轻量确认 + 导出 Blob 下载
- [ ] 前端测试：批量面板组件单测
- [ ] 文档同步：PRD 状态、API、CHANGELOG

## 5. 验收标准

- `POST /admin/v1/configs/batch`（`{action:"delete",ids:[...]}`）后，所选配置全部软删，列表不含、逐项审计落库；`disable`/`enable` 同理置 `enabled`。
- 空 ids 或非法 action → 400；批中含不存在 id → 整批回滚（无副作用）。
- MySQL 集成下批量端点事务可移植（无方言绑定）。
- 前端：空选时批量按钮禁用；批量删除点击后弹轻量确认、确认才发请求；导出选中项触发 JSON Blob 下载。

## 6. 风险 / 待定

- 导出为 best-effort 前端打包（逐项 `getConfig`/`getFile` 取内容），大批量时多次请求；本 FR 仅做基础实现，不做服务端批量导出端点。
