# 功能规格：反向抓取受管任务 + 两段式 scan/submit（FR-58）

> 状态：开发中　·　关联 PRD：FR-58（增强 FR-39，链首 FR-58→59→60）　·　ADR：[ADR-0037](../adr/0037-reverse-fetch-managed-task.md)（取代 ADR-0027 决策5、扩展决策1）

## 1. 背景与目标

反向抓取当前是一次性命令、超限整批失败，被运行时垃圾文件（>1MB 的 `.jsonl` 等）击穿致真机 100% 失败。FR-58 升级为**受管任务 + 两段式**：先扫元信息清单（永不失败）→ 人工选定 → 只抓选定。详见 ADR-0037。本规格定义 FR-58 的实体、状态机、两段式命令**线路契约**（控制面 ⟷ agent 双方对齐）、端点与互斥。FR-59（忽略规则/冲突 diff）、FR-60（前端审核台/任务台）在此底座上叠加。

## 2. 需求（要什么）

- **受管任务 + 状态机**：`scanning → pending-review → fetching → ingesting → done`，旁出 `failed / cancelled / expired`；可查、有进度。
- **两段式**：scan 只回元信息清单（不读内容、永不失败）；submit 只回选定 path 内容。
- **超限不整批失败**：scan 列全树（超阈值红标）；submit 仅抓选定，超阈值文件须显式确认才纳入（只拒该文件不拒整批），文件数/总字节作 submit 兜底。
- **单实例互斥**：每 (ns, serverId) 同时至多一个非终态任务，否则 409。
- **生命周期**：可取消、会过期、过期清单瞬态清空。
- **agent 两段式**：命令载荷加 `mode`(scan/submit)+`selectedPaths`；scan 只 stat 不读内容；submit 按子集读内容。async 不碰主线程、纯读不写、安全边界不变。

### 不做（范围外，留 FR-59/60）
- 忽略规则（临时/持久 DB）、目标已有版本 diff 冲突确认 → **FR-59**。
- 审核清单 UI / 任务台前端 → **FR-60**。
- 本期 submit 的"选定集"由 API 入参直接给（前端审核台 FR-60 才做交互挑选）。

## 3. 设计（怎么做）

### 3.1 数据模型（GORM 可移植：VARCHAR/TEXT、软删哨兵、无 ENUM/JSON 列）
新表 `reverse_fetch_task`：
- `id`(PK)、`namespace_code`、`server_id`、`scope`(group/server)、`group_code`、`scope_target`（scope=server 时为目标 serverId）
- `status` VARCHAR（状态机值）
- `scan_command_id` / `submit_command_id`（uint，引用 agent_command.id；0=未下发）
- `manifest` TEXT（扫描清单 JSON：`{totalFiles,totalBytes,skipped,files:[{path,size,isText,overThreshold}]}`）
- `selected_paths` TEXT（提交时选定 path 的 JSON 数组）
- `total_files` / `selected_count` / `over_threshold_count` / `skipped_count`（int 计数，进度用）
- `operator`、`note`、`created_at`、`updated_at`、`deleted_at`(软删哨兵)
- 索引：`idx_rft_ns_server(namespace_code, server_id)`；**活跃任务唯一**由应用层查 + 约束保证（见 3.4）。

`agent_command`：扩 payload 字段 `mode`("scan"/"submit"，沿用既有 land/imprint 的 mode 维度)、`selected_paths`(submit 用；存 payload JSON 内即可，不必加列)。命令状态仍 pending→fetched→done/failed/expired（不变）。

### 3.2 状态机与编排
1. admin 触发 → 互斥检查 → 建 task(`scanning`) + 下发 scan 命令(`agent_command` mode=scan, pending) + 审计 `file.reverse-fetch-scan` → 唤醒 agent SSE。
2. agent 拉 scan 命令 → 遍历 plugins 只采元信息 → POST scan 清单 → 控制面存 task.manifest + 计数、task→`pending-review`、命令→done。
3. admin 提交选定集 → task 须 `pending-review` → 校验选定（超阈值须确认）→ 存 selected_paths、下发 submit 命令(mode=submit,selectedPaths,pending)、task→`fetching`、审计 `file.reverse-fetch-submit` → 唤醒。
4. agent 拉 submit 命令 → 只读选定 path 内容 → POST /ingest → 控制面 task→`ingesting` → 复用 `FileService.Import` 落库 → task→`done`、命令→done、审计 `file.reverse-fetch-ingest`。
5. 失败任一步 task→`failed`(记因)；admin 取消非终态 → `cancelled`；超时清理 → `expired`(清空 manifest)。

### 3.3 线路契约（控制面 ⟷ agent，双方严格对齐）
- **scan 命令载荷**（控制面→agent，命令 payload）：`{"mode":"scan","scope":"group|server","group":"area1","target":"lobby-1"}`
- **scan 回传**（agent→控制面，**新端点** `POST /beacon/v1/agent/files/scan`）：
  `{"commandId":123,"files":[{"path":"AllinCore/config.yml","size":1234,"isText":true,"overThreshold":false}, ...]}`（**无 content**；agent 已排除 .jar/二进制/不安全路径，超阈值文件 `overThreshold:true` 仍列出）
- **submit 命令载荷**：`{"mode":"submit","scope":"...","group":"...","target":"...","selectedPaths":["AllinCore/config.yml", ...]}`
- **submit 回传**（agent→控制面，**复用** `POST /beacon/v1/agent/files/ingest`）：`{"commandId":123,"files":[{"path":"...","content":"..."}]}`（**仅选定 path**）

### 3.4 互斥
建 task 前查 `(ns, serverId)` 是否已有非终态 task；有 → 拒 `409 REVERSE_FETCH_TASK_ACTIVE`（detail 含活跃 taskId+status）。并发建任务竞态由 DB 事务 + 活跃唯一约束兜底（参考 server_drain/zone_assignment 软删唯一范式）。

### 3.5 超阈值语义
- 单文件阈值沿用 `MaxFileContentBytes`(1MB)。scan 标 `overThreshold`，**不拒**。
- submit 校验：选定集里含 `overThreshold` 文件 → 须入参带 `confirmOverThreshold:true`（FR-60 红标确认），否则拒**该文件**（`400`，列出哪些超阈值未确认），不拒整批。
- 文件数 > `MaxImportFiles` / 总字节 > `MaxImportTotalBytes` 作 submit 兜底（防异常巨大提交）。

### 3.6 admin 端点
- `POST /admin/v1/instances/{serverId}/reverse-fetch?namespace=`（**重定义**为建扫描任务）→ `{taskId,status:"scanning"}`。请求体 `{scope,group,target}`（沿旧）。
- `GET /admin/v1/reverse-fetch/tasks/{id}` → 任务详情（status/manifest/counts/命令引用）。
- `GET /admin/v1/reverse-fetch/tasks?namespace=&serverId=&status=` → 任务列表（历史，FR-60 任务台用）。
- `POST /admin/v1/reverse-fetch/tasks/{id}/submit` → `{selectedPaths,confirmOverThreshold?}`，下发 submit。
- `POST /admin/v1/reverse-fetch/tasks/{id}/cancel` → 取消。
- 写端点 readonly→403；均入审计；新写路由登记进 FR-72 `coveredWriteRoutes`（避免漂移守护失败/双记）。

### 3.7 agent-core 改动
- 命令载荷数据类加 `mode` + `selectedPaths`。
- `PluginsTreeReader`/`PluginsTreeFilter`：新增 **scan 模式**——只 `stat` 取 size + UTF-8 文本判定 + overThreshold 标记、**不读内容**、排除 .jar/二进制/不安全路径、**不因超限失败**；产出 `ScanFile(path,size,isText,overThreshold)`。
- **submit 模式**：按命令 `selectedPaths` 子集读内容回传（复用既有读内容 + 过滤，但限定在选定集）。
- `ReverseFetchExecutor`：按 `mode` 分路（scan→`uploadScan`、submit→`uploadIngest`）。
- `BeaconApiClient`：加 `uploadScan(commandId, files)`（POST /scan）；`uploadIngest` 不变。
- 全程 async、不碰 MC 主线程、HTTP/JSON 只在适配器、纯读不写、observe-only 写回守卫不变。

## 4. 任务拆分
- [ ] 后端：`reverse_fetch_task` 模型 + 仓库 + AutoMigrate；状态枚举常量。
- [ ] 后端：`ReverseFetchTaskService`（建任务+互斥、scan 回传存清单、submit 编排、ingest 落库复用 Import、取消、过期）。
- [ ] 后端：handler + 5 个 admin 端点 + agent /scan 端点；router 挂载 + FR-72 覆盖集登记。
- [ ] 后端测试（先行红）：互斥 409 / scan 永不失败列全树（含超阈值红标）/ 状态机流转 / submit 仅落选定 / 超阈值未确认拒该文件 / 取消 / 过期 / ingest 复用 Import。
- [ ] agent：命令载荷 mode+selectedPaths；scan 只列元信息不读内容不失败；submit 按选定子集回传；executor 分路 + uploadScan；agent 单测（scan 列出超限不失败、submit 子集）。
- [ ] doc-sync：PRD FR-58、API.md（新端点）、ARCHITECTURE（任务+两段式）、CHANGELOG、ADR-0027 状态注明决策5被取代、本规格。

## 5. 验收标准
- scan 不因任何超限文件失败、返回全树清单（path+size+isText+overThreshold）。
- 一实例已有活跃任务时再触发 → 409 REVERSE_FETCH_TASK_ACTIVE。
- 状态机 scanning→pending-review→fetching→ingesting→done 正确流转、可查进度；取消→cancelled、超时→expired。
- submit 仅落选定集；超阈值文件未确认被拒（仅该文件，不整批）；确认后可入。
- 受影响组件测试全绿（`go build/test/vet ./...`；`cd agent && ./gradlew test`）。
- **真机**：对 lobby-1 扫描不再整批失败、列出清单（含超限运行时文件红标）；选定小配置提交后落库；任务状态/进度可查。

## 6. 风险 / 待定
- 改 agent-core → 双端 jar 重建 + 真机重部；新工作流要求新版 agent（旧 agent 收不到 scan/submit mode）。
- manifest TEXT 大树可能数 MB：过期及时清空；本期不分页（FR-60 前端按需）。
- submit 选定集 ingest 仍复用 `FileService.Import` 整批事务——选定集应是小配置集，超阈值/总量兜底防异常。
- FR-58 本期 submit 选定由 API 直接给；真正的人工审核挑选交互在 FR-60。
