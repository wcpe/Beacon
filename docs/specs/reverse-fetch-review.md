# 功能规格：反向抓取审核——忽略规则 + 冲突 diff 确认（FR-59）

> 状态：开发中　·　关联 PRD：FR-59（增强 FR-39，依赖 FR-58）　·　ADR：沿用 [ADR-0037](../adr/0037-reverse-fetch-managed-task.md)（受管任务两段式）+ 复用 FR-46 拓印 diff/自审范式，无新 ADR（conflict-review 为 ADR-0037 状态机的 spec 级扩展，经用户确认）

## 1. 背景与目标

FR-58 把反向抓取做成受管任务 + 两段式（scan 清单 → submit 选定 → ingest）。FR-59 在此底座上加**审核**：① 持久**忽略规则**（让运行时垃圾等下次扫描自动排除）；② 入库前**目标已有版本则出 diff、人工确认保留哪份**（复用 FR-46 拓印的 diff/自审范式）。纯后端（agent 不改——内容已在 submit 回传）。

## 2. 需求（要什么）

### 2.1 持久忽略规则
- 新表存忽略规则，按 **ns / 大区(group) / 实例(serverId)** 维度；规则类型 **exact（单文件精确）/ prefix（目录前缀）**。
- 扫描清单返回时，命中**当前任务作用域**活跃规则的文件标 `ignoredByRule=true`（FR-60 默认排除、可见）。
- 规则 CRUD 端点（列 / 建 / 删），建/删入审计。
- 「临时忽略」是前端行为（不选即排除，FR-60）；后端只管**持久规则**。「保存为持久规则」= 建一条规则。

### 2.2 冲突 diff 确认（conflict-review）
- submit 回传内容后，对每个选定文件查目标是否已有版本（`FindByIdentity`）。
- **无冲突** → 沿 FR-58 原路 ingesting → done（全量落库）。
- **有冲突** → 任务进新状态 `conflict-review`，**暂存本次 submit 回传的全部内容**（瞬态），不立即落库；暴露冲突清单 + 逐文件 diff（抓取值 ⟷ 已有版本）。
- 用户逐冲突文件决定**保留哪份**：取抓取（覆盖，须带 `reviewedMd5`=抓取内容 md5 过自审门）或保留已有（跳过该文件）。
- resolve 后落库：非冲突文件 + 确认覆盖的冲突文件（复用 `FileService.Create`/`Publish`），跳过保留已有的 → done。
- 忽略 / 确认 / 提交入审计。

### 不做（范围外）
- 临时忽略交互、审核清单 UI、任务台、冲突 diff 前端 → **FR-60**。
- 通配符/正则忽略（exact + prefix 足够"逐项/目录忽略"，YAGNI）。

## 3. 设计（怎么做）

### 3.1 忽略规则
- 新表 `reverse_fetch_ignore_rule`（GORM 可移植：VARCHAR、软删哨兵、无 ENUM/JSON 列）：`id` / `namespace_code` / `scope`(group/server) / `group_code` / `scope_target`(serverId，scope=server 时) / `rule_type`(exact/prefix) / `pattern` / `comment` / `operator` / 时间戳 / `deleted_at`。唯一键 `uk_ignore(namespace_code, scope, group_code, scope_target, rule_type, pattern, deleted_at)`。索引 `idx_ignore_ns_group(namespace_code, group_code)`。
- 仓库：Create / List(按 ns+scope+group+scopeTarget) / SoftDelete。
- 应用：`ReverseFetchTaskService` 返回任务详情（GET task）时，按任务 (ns, scope, group, scopeTarget) 查活跃规则，对 manifest 每文件计算 `ignoredByRule`（exact: path==pattern；prefix: strings.HasPrefix(path, pattern)）。**纯展示标记**，不硬改 manifest 存储（应用层即时算）。
- 端点：`GET/POST/DELETE /admin/v1/reverse-fetch/ignore-rules`（写端点 readonly→403、入审计 `reverse-fetch.ignore-rule-add`/`-remove`、登记 FR-72 覆盖集）。

### 3.2 conflict-review 状态机扩展
- `reverse_fetch_task` 加状态 `conflict-review`（介于 fetching 与 ingesting/done）；加瞬态列 `submit_content` TEXT（JSON `path→content`，conflict-review 期暂存 submit 回传内容，done/expired 清空——同 FR-46 imprint_content 范式）。
- 改 `ReceiveSubmitIngest`（FR-58 的 submit 回传处理）：
  1. 收 submit 回传内容（仅选定集）。
  2. 逐文件查 `FindByIdentity(ns, group, path, scope, scopeTarget)` 判冲突。
  3. **无冲突**：task→ingesting→`FileService.Import` 落库→done（FR-58 原路）。
  4. **有冲突**：暂存全部回传内容到 `submit_content`、记冲突 path 集、task→`conflict-review`（不落库）。
- 新端点：
  - `GET /admin/v1/reverse-fetch/tasks/{id}/conflicts` → 冲突 path 清单。
  - `GET /admin/v1/reverse-fetch/tasks/{id}/conflicts/diff?path=` → `{path, fetchedContent, fetchedMd5, existingContent, existingMd5, version}`（抓取值 ⟷ 已有版本；existing 取自 file_object 当前版本）。
  - `POST /admin/v1/reverse-fetch/tasks/{id}/resolve` → body `{decisions:[{path, action:"overwrite"|"keep", reviewedMd5?}]}`；每个 overwrite 须 reviewedMd5==该文件 fetched md5（自审门，盲确认→412 复用 `ErrImprintReviewMismatch` 或新 `REVERSE_FETCH_REVIEW_MISMATCH`）。
- resolve 落库：CAS 认领 conflict-review→ingesting（防并发双 resolve，复用 FR-46 CAS 范式）→ 事务内 Import 非冲突集 + 逐个 overwrite（Create/Publish）+ 跳过 keep → done、清空 `submit_content`、审计 `file.reverse-fetch-ingest`（detail 不含内容）。
- 过期：conflict-review 也是非终态，sweeper 一并扫陈旧→expired + 清 submit_content。互斥（FR-58 active 唯一）：conflict-review 仍占活跃。

### 3.3 复用点（FR-46 / FR-58）
- diff 内容对比、reviewedMd5 自审门、CAS 认领清瞬态 → 复用 `imprint_service` 范式（`filetree.ContentMD5`、`UpdateStatusClear*` CAS）。
- 落库 → `FileService.Create`/`Publish`（同 FR-46 `landImprint` 先 Create 后 Publish 降级）。
- 冲突的「已有版本内容」→ 取 file_object 当前 `Content`（或最新 revision）。

## 4. 任务拆分
- [ ] 模型 `reverse_fetch_ignore_rule` + 仓库 + AutoMigrate；`reverse_fetch_task` 加 `conflict-review` 状态常量 + `submit_content` 列。
- [ ] 忽略规则 service + CRUD handler/端点 + manifest `ignoredByRule` 标记 + 审计 + FR-72 覆盖集。
- [ ] conflict-review：改 ReceiveSubmitIngest 检冲突分路 + 暂存；conflicts/diff/resolve 端点 + service（CAS + 自审 + 落库）+ 审计。
- [ ] 错误码：`REVERSE_FETCH_REVIEW_MISMATCH`(412)（或复用）、冲突/状态相关。
- [ ] 测试（先行红）：规则 CRUD + manifest 标记(exact/prefix) / 无冲突直 done / 有冲突进 conflict-review 暂存不落库 / diff 返抓取⟷已有 / resolve overwrite 须自审 md5（盲确认 412）/ keep 跳过 / resolve 后 done 落库正确 / 过期清 submit_content / 互斥含 conflict-review。
- [ ] doc-sync：PRD FR-59、API.md（新端点）、ARCHITECTURE（忽略规则 + conflict-review 状态）、CHANGELOG、本规格。

## 5. 验收标准
- 持久忽略规则可建/列/删；下次扫描清单中命中规则的文件 `ignoredByRule=true`（exact/prefix 生效）。
- 提交后目标无已有版本 → 直接 done 落库；有已有版本 → 进 conflict-review、不落库、出冲突清单 + diff。
- 冲突 diff 确认：overwrite 须带正确 reviewedMd5（盲确认 412）；keep 跳过保留已有；resolve 后正确落库 done。
- 忽略 / 确认 / 提交入审计。
- 受影响组件测试全绿（`go build/test/vet ./...`，集成 `-tags=integration` 跑反向抓取审核全链路）。
- **真机/集成**：建规则→扫描见标记；制造冲突（同 path 已追踪 + 再抓）→ 进 conflict-review→diff→resolve→落库。

## 6. 风险 / 待定
- `submit_content` 瞬态可能较大（选定集内容）：done/expired 及时清；选定集应为小配置集。
- conflict-review 扩 ADR-0037 状态机一态——spec 级扩展、引 ADR-0037、不另写 ADR（用户确认）；ARCHITECTURE 同步。
- 真机冲突路径在 FR-60 UI 前以 API/集成验证；前端审核台/任务台/diff 面板归 FR-60。
