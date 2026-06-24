# 功能规格：审计全文检索 + 导出

> 状态：开发完成（待真机浏览器验）　·　关联 PRD：FR-84（增强 FR-7）　·　分支：feature/fr-84-audit-search-export

## 1. 背景与目标
审计页（FR-7 / FR-30 / FR-73）当前只能按 namespace / operator / action / targetType / targetRef / 时间范围过滤。
运维排障时常需要「在 detail 里找某关键字」（如某 serverId、某 dataId、某错误片段），以及把过滤后的审计
导出成文件（CSV 给表格软件 / JSON 给脚本）离线分析或留档。本功能给审计加 detail 关键字检索与导出端点。属 P2。

## 2. 需求（要什么）
- detail 关键字检索：审计查询新增 `detailKeyword`，对 `detail` 列做 LIKE 子串匹配，与现有过滤维度叠加（AND）。
- 导出端点：`GET /admin/v1/audits/export`，复用与 `List` 完全相同的过滤参数（含新 `detailKeyword`），
  按 `format=csv|json` 输出全部命中记录（不分页、全量导出）；**流式**输出（按分页游标边查边写，不一次性全读内存）。
- 前端：审计页加「detail 关键字」搜索框 + 「导出 CSV / 导出 JSON」按钮，下载当前过滤条件下的全量结果。
- 范围内：detail LIKE 检索、CSV/JSON 流式导出、前端搜索框 + 导出按钮。
- 不做（范围外）：不引入全文索引引擎 / 分词 / 高亮；导出不做异步任务 / 邮件投递；不改 audit_log 表结构。

## 3. 设计（怎么做）
控制面分层不破（router → handler → service → repository，handler 不碰 GORM）。

- **repository**：`AuditFilter` 加 `DetailKeyword string`；`List` 在其非空时追加
  `db.Where("detail LIKE ?", "%"+escapeLike(kw)+"%")`——用 GORM 占位符、不写方言函数，保 Postgres 可移植；
  对 `%` / `_` / `\` 做转义（`ESCAPE '\'`）避免用户输入被当通配符。
  新增 `Stream(f, batch, fn)`：按 `id` 降序游标分页（`Limit(batch).Offset(...)` 复用同一过滤），
  逐批回调，service 边查边写，避免一次性全量载入内存（性能约束 §17）。
- **service**：`List` 透传 `DetailKeyword`（已有 page/size 规整逻辑不变）；
  新增 `Export(f, format, w)`：校验 format（仅 csv/json，非法 → `ErrInvalidParam`），
  调 `repo.Stream` 边查边写：CSV 先写表头再逐行写，JSON 写成数组流式逐条 encode。
- **handler**：`List` 解析 `detailKeyword` 透传；新增 `Export`：解析过滤 + format，
  设 `Content-Type` 与 `Content-Disposition`（文件名带时间戳），委托 service 写入 `http.ResponseWriter`。
  导出错误在写头前返回统一错误体；流式写出过程中出错只记日志（头已发无法改状态码）。
- **router**：`/audits/export` 静态路由置于 `/audits` 与 `/audits/analytics` 一组，受同一鉴权链保护。
- **前端**：`client.ts` 的 `AuditFilter` 加 `detailKeyword?`；新增 `exportAuditsUrl(filter, format)` 拼出带令牌？
  —— 改为 `exportAudits(filter, format)`：用 `fetch` 带 `Authorization` 头取 Blob 后 `createObjectURL` 触发下载
  （沿用既有 FR-74 配置批量导出的 Blob 下载手法，不把令牌放进 URL）。`AuditsPage` 加搜索框 + 两个导出按钮。

不涉及架构决策（无新技术 / 新模式 / 推翻旧决策），无需 ADR。

## 4. 任务拆分
- [x] repository：`AuditFilter.DetailKeyword` + `List` LIKE（转义）+ `Stream` 游标分页；单测
- [x] service：`List` 透传 + `Export(format, w)` 流式写 CSV/JSON；单测（含非法 format）
- [x] handler + router：`Export` 端点 + `/audits/export` 路由
- [x] 集成测试（真 MySQL）：detail LIKE 命中 + CSV/JSON 导出流
- [x] 前端：`AuditFilter.detailKeyword` + `exportAudits` + 搜索框 + 导出按钮；vitest
- [x] 文档同步：PRD 状态、API.md、CHANGELOG

## 5. 验收标准
- `AuditFilter.DetailKeyword` 非空时 `List` 仅返回 detail 含该子串的记录，与其它过滤 AND 叠加；`%`/`_` 不被当通配符。
- `GET /admin/v1/audits/export?...&format=csv` 返回 CSV（首行表头 + 全量命中行，时间倒序），`Content-Type: text/csv`。
- `GET /admin/v1/audits/export?...&format=json` 返回 JSON 数组（全量命中），`Content-Type: application/json`。
- `format` 非 csv/json → `400 INVALID_PARAM`。
- 导出复用 `List` 同过滤（含 detailKeyword）；流式分批查询，不一次性全量载入内存。
- 前端审计页可输 detail 关键字检索、点导出按钮下载 CSV/JSON 文件。
- `go build ./... && go test ./...` 绿；真 MySQL 集成验 detail LIKE + 导出流；前端 `pnpm test` + `pnpm build` 绿。

## 6. 风险 / 待定
- LIKE 子串检索在大表上无索引、可能慢——MVP 运维量级可接受；如成瓶颈再议（不提前优化，YAGNI）。
- 导出全量无上限——运维审计量级可控；流式输出已避免内存峰值，暂不加行数硬上限。
