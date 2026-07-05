# 功能规格：多级灰度配置同步中心

> 状态：草拟　·　关联 PRD：FR-129 / FR-130 / FR-131 / FR-132 / FR-133　·　关联 ADR：[ADR-0058](../adr/0058-controlled-large-file-sync-channel.md)

## 1. 背景与目标

运维需要把一台在线 bukkit 子服的目录作为“黄金模板”，按环境 / 大区 / 小区 / 单服微调筛选目标，并把目录内容安全地分批同步到 1000+ 台 bukkit 子服。同步对象允许包含 `.jar`、地图、资源包等二进制大文件，因此不能复用现有 JSON ingest 和文本配置反向抓取通道。

目标是新增一个“控制面编排 + 流式文件数据通道 + 管理台实时可视化”的受控同步能力：源 agent 上传一次，控制面缓存，目标 agent 分批拉取；全程支持增量哈希、覆盖前备份、暂停 / 继续 / 终止、熔断和回滚。

## 2. 需求

### 范围内

- 运维选择在线 bukkit 子服作为黄金模板源。
- 运维填写服务器根内相对目录；源目录与目标目录同路径，不做路径映射。
- 控制面校验源和目标均为 `role=bukkit` 在线实例，排除 BungeeCord 代理。
- 源 agent 扫描目录 manifest，并把文件内容流式上传到控制面 blob 缓存。
- 目标 agent 扫描本地同路径 manifest，仅下载 hash 不同或缺失的文件。
- 目标 agent 写入前备份旧目录，写入后回传状态。
- 编排器按批次大小、批间等待、失败率阈值执行，支持暂停、继续、终止和熔断。
- 管理台 `/file-sync` 使用 5 步向导：源与目录、目标范围、灰度策略、安全检查、预览与启动。
- 最后一步通过 `create + plan` 生成持久规划预览，展示源清单就绪状态、目标集合、批次、熔断参数与风险摘要；确认后才允许启动。
- 浏览器实时进度走独立 SSE。
- 历史任务支持整次、批次、单服回滚。

### 不做

- 不做源目录到目标目录映射。
- 不同步 BungeeCord 代理。
- 不通过 SSH/SFTP。
- 不开放 agent 入站 HTTP 端口。
- 不新增 Redis、MQ、DI 框架。
- 不把文件内容写入 `agent_command`、审计 detail、JSON ingest 或配置中心文件对象。
- 不做滚动重启、蓝绿切换、玩家 drain；本功能只同步文件。
- 本轮不实现目标端 dry-run 扫描协议、不新增启动前文件级差异分页接口；目标本地 manifest 比对仍在执行期由目标 agent 完成。

## 3. 设计

### 3.1 模块

- 控制面模型：同步任务、文件清单、缓存对象、批次、目标、日志、备份点。
- 控制面服务：任务创建 / 扫描 / 上传完成 / 目标规划 / 启动 / 暂停 / 继续 / 终止 / 熔断 / 回滚。
- 控制面缓存：按 taskId 和 blob hash 落本地文件，记录大小、hash、引用任务、过期时间。
- agent 命令：新增同步相关命令类型，命令只含 taskId / batchId / targetId / directory / manifest 摘要等控制信息。
- agent 数据通道：源上传 blob，目标下载 blob；用流式 IO。
- 管理台 SSE：`/admin/v1/file-sync/tasks/{id}/events` 向浏览器推送状态和日志事件。
- 前端页面：`/file-sync`，接真实 API 与 SSE，按 5 步向导承载创建、规划预览与启动。

### 3.2 数据模型草案

- `file_sync_task`：任务主表，记录 namespace、source_server_id、directory、status、batch_size、interval_sec、failure_threshold_percent、operator、started_at、finished_at。
- `file_sync_file`：源 manifest 文件清单，记录 task_id、path、size、hash、mode、blob_key、status；任务详情的 `sourceReady`、`sourceFileCount`、`sourceTotalBytes` 由该清单与 blob 缓存状态汇总。
- `file_sync_batch`：批次表，记录 task_id、batch_no、status、planned_count、success_count、failed_count、started_at、finished_at。
- `file_sync_target`：目标表，记录 task_id、batch_id、server_id、status、backup_path、current_file_count、changed_file_count、skipped_file_count、error。
- `file_sync_log`：任务日志摘要，记录 task_id、batch_id、server_id、level、message、created_at。

枚举全部落 `VARCHAR`，manifest / 细节结构如需 JSON 一律落 `TEXT`，不使用 MySQL 专有 JSON/ENUM。

### 3.3 状态机

任务状态：

```text
draft → scanning → cached → planned → running → paused → running → succeeded
                                      │          │        │
                                      ├──────────┴────────┴→ failed
                                      ├─────────────────────→ terminated
                                      └─────────────────────→ circuit-broken
```

目标状态：

```text
pending → manifesting → backing-up → transferring → applying → succeeded
           │             │            │             │
           └─────────────┴────────────┴─────────────┴→ failed
```

回滚状态：

```text
rollback-pending → rollback-running → rollback-succeeded / rollback-failed
```

### 3.4 接口草案

管理台：

- `POST /admin/v1/file-sync/tasks`：创建任务并触发源扫描。
- `GET /admin/v1/file-sync/tasks`：历史任务列表。
- `GET /admin/v1/file-sync/tasks/{id}`：任务详情，含源清单就绪字段、批次、目标和日志。
- `POST /admin/v1/file-sync/tasks/{id}/plan`：保存目标筛选并生成批次，返回最后一步展示的规划预览。
- `POST /admin/v1/file-sync/tasks/{id}/start`：规划预览确认后开始执行；要求任务为 `planned` 且源清单已就绪。
- `POST /admin/v1/file-sync/tasks/{id}/pause`：暂停后续批次。
- `POST /admin/v1/file-sync/tasks/{id}/resume`：继续。
- `POST /admin/v1/file-sync/tasks/{id}/terminate`：紧急终止。
- `POST /admin/v1/file-sync/tasks/{id}/rollback`：按整次 / 批次 / 单服回滚。
- `GET /admin/v1/file-sync/tasks/{id}/events`：浏览器 SSE。

agent：

- `POST /beacon/v1/agent/file-sync/{taskId}/manifest`：源 / 目标回传 manifest 摘要。
- `PUT /beacon/v1/agent/file-sync/{taskId}/blobs/{hash}`：源 agent 流式上传 blob。
- `GET /beacon/v1/agent/file-sync/{taskId}/blobs/{hash}`：目标 agent 流式下载 blob。
- `POST /beacon/v1/agent/file-sync/{taskId}/targets/{targetId}/result`：目标执行结果。
- `POST /beacon/v1/agent/file-sync/{taskId}/targets/{targetId}/rollback-result`：回滚结果。

具体契约在实现时同步到 `docs/API.md`。

### 3.5 管理台向导边界

`/file-sync` 不再是一屏工作台，而是 5 步向导：

1. 源与目录：选择黄金模板源、目录，并显示源扫描 / 清单状态。
2. 目标范围：保留搜索、分组、分页和批量选择，避免 1000+ 目标一次性全量挂载。
3. 灰度策略：配置批次大小、批间等待、失败率熔断阈值，并实时计算批次数。
4. 安全检查：汇总源目录、目标范围、备份、熔断和回滚可用性，不启动任务。
5. 预览与启动：调用创建与规划接口得到持久预览，展示源清单、目标、批次和风险摘要；只有预览存在且状态允许时才显示启动动作。

本轮预览是控制面的规划预览，不承诺目标端 dry-run 扫描结果；目标端是否跳过 / 下载文件由执行期 manifest 哈希比对决定。

### 3.6 批次推进语义

启动后先下发当前 / 首个 pending 批次。某批全部目标回执后，控制面更新批次成功 / 失败计数：若失败率达到熔断阈值，任务进入 `circuit-broken`，后续 pending 批次不再下发；若未熔断且存在下一批 pending，则按 `intervalSec` 延迟调度；若不存在下一批 pending，则任务进入 `succeeded`。暂停只阻止下一批启动，不影响已完成目标；继续从下一批 pending 批次恢复。

## 4. 任务拆分

- [ ] 新增 ADR-0058 与本 spec。
- [ ] 为 Go 路径安全、目标筛选、任务状态机写失败测试。
- [ ] 新增同步任务 GORM 模型、仓库、AutoMigrate。
- [ ] 新增控制面 blob 缓存服务与清理器。
- [ ] 新增 admin 任务 API 与浏览器 SSE。
- [ ] 新增 agent 流式传输抽象与 OkHttp 实现。
- [ ] 新增 agent 源扫描 / 上传、目标下载 / 备份 / 应用 / 回滚执行器。
- [ ] 新增编排器：批次、暂停 / 继续 / 终止、熔断。
- [ ] 新增前端 API client、类型、mock 数据、5 步向导页面和测试。
- [ ] 同步 `docs/ARCHITECTURE.md`、`docs/API.md`、`CHANGELOG.md`。
- [ ] 跑 `go test ./...`、agent 测试、`cd web && pnpm test`、前端 build，并做浏览器真机验证。

## 5. 验收标准

- 选一台在线 bukkit 源服和相对目录后，控制面能拿到 manifest 与缓存 blob。
- 目标只能选 `role=bukkit`，不能选 BungeeCord。
- `/file-sync` 按 5 步推进，最后一步先展示规划预览，启动动作不会早于规划预览。
- 同步 3 批目标时，页面实时显示批次进度和每台服务器状态。
- 暂停后不再启动下一批；继续后恢复；终止后后续目标不执行。
- 某批失败率超过阈值后任务进入熔断暂停，后续批次不执行。
- 目标同 hash 文件被跳过，变更文件才下载。
- 覆盖前生成备份点，历史任务可按整次 / 批次 / 单服回滚。
- 所有错误在前端脱敏展示，不静默失败。
- 大文件传输不通过 JSON，不整体读入内存。

## 6. 风险 / 待定

- 控制面缓存目录大小和保留期需要实现硬上限，防止磁盘被任务填满。
- 旧 agent 不具备本能力，开始任务前需要能力版本校验。
- Windows 下目录备份 rename 与目标目录占用可能失败，需把失败原因回传给前端。
- 大量目标同时下载可能打满控制面带宽，第一版必须有最大并发数。
- 流式下载断点续传第一版不做；失败可重试整个文件。
