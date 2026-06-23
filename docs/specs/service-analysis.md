# 功能规格：服务分析 / 平台用量看板（FR-73）

> 状态：开发中　·　关联 PRD：FR-73（依赖 FR-72 全量审计覆盖）　·　后端聚合端点 + 前端分析页（无新 ADR——复用既有分层与 recharts，不引新技术/新模式）。

## 1. 背景与目标
FR-72 让写操作全量落 `audit_log`（操作者 + 动作 + 目标 + 结果 + 时间）。FR-73 在其上加一个**平台运维活动**的聚合视角：新「服务分析」页，按时间窗 + 环境聚合审计活动的**计数 / 成功率 / 趋势**（发布、修改、回滚、校验失败、反向抓取、区改派、文件树变更、登录、密钥…均为 `action` 枚举值），KPI 卡片 + 图表呈现。

**与既有看板的边界**：FR-32（MC 负载 TPS/内存/人数）与 FR-30（Prometheus 指标）是**运行时遥测**；FR-73 是**运维操作审计聚合**——前者「服务器累不累」，后者「人/系统对平台做了什么」。两者数据源、页面、刷新节奏都独立，不混。

## 2. 需求
- 新「服务分析」页（`/service-analysis`）：选时间窗（近 7 天 / 30 天）+ 环境（可空 = 全部）后展示：
  - **KPI 卡片**：窗口内总操作数 / 成功数 / 失败数 / 成功率（ok ÷ total）。
  - **按动作分布**：各 `action` 计数（降序，前端 i18n 映射中文动作名），柱状或排行列表。
  - **每日趋势**：窗口内逐日操作数折线（按 UTC 日聚合）。
- 数据源 = `audit_log`（FR-7/FR-72 已落库）。**不新增审计写入维度**、不改 audit_log 表结构 / 索引（既有 `idx_audit_time` / `idx_audit_namespace` 够用）。

### 不做（守范围，不镀金）
- 不做操作人维度下钻、不做按目标对象聚合、不做导出 / 报表订阅、不做实时刷新（手动选窗口即查）。需要时后续按需加。
- 不引图表新库（用既有 recharts）、不引时序数据库 / 物化视图（审计量是运维人尺度、非遥测尺度，普通聚合查询足够）。

## 3. 后端设计（分层 router→handler→service→repository）

### 3.1 端点
`GET /admin/v1/audits/analytics?namespace=<可空>&from=<RFC3339>&to=<RFC3339>`
- 静态路由，注册在 `/audits` 之后、任何 `{id}` 通配之前（与现有 `/audits` 列表平级）。
- `from`/`to` 缺省：`to`=当前、`from`=`to`-30 天。**窗口上限 92 天**（超出 → 400，防一次性捞过量行）。
- 复用既有 `repository.AuditFilter` 的 `Namespace`/`From`/`To` 字段做过滤。

### 3.2 响应 DTO（契约，前端据此 mock）
```jsonc
{
  "from": "2026-05-25T00:00:00Z",
  "to":   "2026-06-24T00:00:00Z",
  "total":     128,
  "okCount":   119,
  "failCount":   9,
  "byAction": [                       // 降序按 count，动作枚举原值（前端 i18n 映射中文）
    { "action": "config.publish", "count": 40 },
    { "action": "zone.assign",    "count": 22 }
  ],
  "byDay": [                          // 升序按 date，UTC 日；窗口内无操作的日补 0（前端可不补，后端给有数据的日即可）
    { "date": "2026-06-01", "count": 5 },
    { "date": "2026-06-02", "count": 8 }
  ]
}
```

### 3.3 实现要点（**可移植 GORM，禁方言专有**）
- **禁用 `DATE()` / `date_trunc` 等方言日期函数**（不可移植到 Postgres）。日聚合在 **Go 侧**做：repo 仅取窗口内投影行 `created_at, result, action`（`Select` 三列 + `AuditFilter` 过滤 + `Order created_at asc`），service 单遍扫描计 `total/okCount/failCount`、累加 `byAction` map、按 `created_at.UTC()` 的 `YYYY-MM-DD` 累加 `byDay` map，再排序成 DTO。
- 窗口已被 92 天上限约束，投影行量是运维尺度（人/agent 写操作，非遥测高频），单遍内存聚合可接受；不再额外 LIMIT 截断（截断会让聚合失真）。
- 分层：repo 新增 `ScanForAnalytics(f AuditFilter) ([]AuditAnalyticsRow, error)`（只读投影，纯 DB IO）；聚合逻辑（含日分桶 / 排序 / 成功率不在此算）放 `AuditService.Analytics(f) (*AuditAnalytics, error)`；handler 解析 query → 调 service → `render.WriteJSON`。handler 不碰 GORM。
- `okCount` 判定：`result == ActionResultOk`（复用既有枚举常量，不硬编码字面量）。

### 3.4 测试（集成，**真 MySQL 验可移植** + sqlite 单测）
- `internal/server/router_integration_test.go`（或审计测试文件）：插入跨多日、多 action、ok/fail 混合的审计行 → `GET /audits/analytics?from=&to=` → 断言 `total/okCount/failCount`、`byAction` 计数与降序、`byDay` 逐日计数与升序；断 `namespace` 过滤生效；断窗口 > 92 天 → 400。
- service 单测（sqlite/testing）：日分桶按 UTC、成功率分母为 0 时不 panic（total=0 → okCount/failCount=0、byAction/byDay 空数组非 null）。
- **必须 `go test -tags=integration` 连真 MySQL 跑过**（GROUP BY/投影查询方言无关、空结果序列化为 `[]`）。

## 4. 前端设计（纯前端，复用 FR-32 看板范式）
- 新页 `web/src/pages/ServiceAnalysisPage.tsx`，路由 `/service-analysis`（`web/src/App.tsx` 注册 + `Layout.tsx` 侧栏加导航项，置于「审计」附近）。
- 复用 `DashboardPage` 范式：环境 Combobox（FR-51，可空清回全部）+ 时间窗 Tabs（近 7 天 / 30 天）+ KPI `StatCard` 网格 + recharts `LineChart`（每日趋势）/ 排行（按动作，`BarChart` 或排行列表）。
- 新增 client：`web/src/api/client.ts` `getAuditAnalytics(params: {namespace?; from?; to?}): Promise<AuditAnalytics>`；`web/src/api/types.ts` 加 `AuditAnalytics`/`AuditActionCount`/`AuditDayCount` 类型（与 §3.2 契约对齐）。
- 动作枚举 → 中文：复用 `AuditsPage` 既有 action i18n 映射（同一份 key，不复制）。
- i18n `serviceAnalysis` 命名空间新增页面文案。

### 测试（vitest + RTL）
- mock `getAuditAnalytics` 返样例 → 渲染断言 KPI 数值（含成功率计算）、按动作排行条目、趋势点数；切时间窗 Tabs 触发以新 `from/to` 重查；切环境触发带 `namespace` 重查。图表用轻量桩替身（同 `DashboardPage.test`）。

## 5. 验收标准
- `GET /audits/analytics` 返窗口内聚合（total/ok/fail/byAction/byDay），namespace + 时间窗过滤生效，> 92 天 400。
- 服务分析页：选窗口/环境后 KPI + 动作分布 + 每日趋势正确呈现，与 FR-32 负载看板清晰区分。
- 后端 `go test`（含 `-tags=integration` 真 MySQL）全绿；前端 `pnpm test` + `pnpm build` 全绿。
- **真机浏览器**（末批）：服务分析页可选窗口/环境、KPI 与图表随数据呈现。

## 6. 风险 / 待定
- 审计量增长：窗口 92 天上限 + 运维尺度写入，单遍 Go 聚合可接受；若未来审计量级跃升（接入高频系统写），再评估迁 SQL 侧分组（届时用可移植的应用层日键或 ADR 决策方言策略）。
- UTC 日分桶：趋势按 UTC 日，跨时区运维看图需知；与审计列表 `createdAt`（UTC）一致，不引本地时区歧义。
