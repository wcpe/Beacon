# 功能规格：配置导入（上传通道）

> 状态：已交付@v0.6.0（v0.6.0 后回归：上传含 agent 自身目录被整批拒 → 方案 D 归真修复，见 [ADR-0028](../adr/0028-allow-hosting-agent-self-dir.md)）　·　关联 PRD：FR-38　·　分支：feature/fr-38-import-upload

## 1. 背景与目标
FR-14 文件树托管（通道B）已提供整文件 blob 托管：`file_object` / `file_revision` + `FileService`（按相对 `path` 整文件覆盖、scope 覆盖链、manifest 增量同步）。但运维要把一份本地 `plugins` 目录批量托管时，只能在管理台逐个文件新建/发布，效率低。本功能让运维把一份目录从管理台一次性上传，落到指定「组（group）」的文件树，实现全局复用。属第二期（P2）。沿用 [ADR-0010](../adr/0010-file-tree-hosting-blob-channel.md)，无需新 ADR。

## 2. 需求（要什么）
- 管理台上传一份目录（多文件 + 各自相对 path），目标为某环境（namespace）下的某组（group），落为 `scope=group` 的文件树。
- 复用 FR-14 通道B 的整文件覆盖语义：每个文件按相对 `path` 创建（首版）或发布新版本（已存在），不做格式解析 / 键级合并。
- 一次导入是一次原子操作：多文件写在同一事务内完成，提交成功后才唤醒文件长轮询（沿用既有 fileHub 唤醒约定）。
- 每次导入入审计（一条 `file.import`，记录目标组与文件数）。
- 校验：单文件大小上限、单次文件数上限、单次总字节上限、相对 path 安全（防穿越，复用既有 `normalizePath`）、目标组合法（`scope=group` 校验）。
- 范围内：控制面导入端点 + service 批量 upsert + 校验 + 审计；前端「导入到组」入口。
- 不做（范围外）：FR-39 在线实例反向抓取（server→agent 命令通道）；zip 包解压（仅接收前端已展开的多文件 multipart）；非 group 层导入（global/zone/server 暂不提供批量入口）。

## 3. 设计（怎么做）
- 控制面：
  - 新增 `file.import` 审计动作常量（落 VARCHAR，见 enums）。
  - `FileService.Import(params)`：在单事务内对每个文件做「存在则 publish 新版本、不存在则 create 首版」，复用既有 `appendRevision` 内部助手保持整文件覆盖语义；事务内写一条 `file.import` 审计；提交后对该组 scope 唤醒一次（`NotifyFileChange`）。早校验数量 / 大小 / path / 目标组。
  - `handler.FileHandler.Import`：解析 multipart（`namespace` / `group` 字段 + 多个文件部件，文件部件的相对 path 取自表单字段 `paths`），交 service；handler 不碰 GORM。`MaxImportFiles` / `MaxImportTotalBytes` 为 handler 侧多文件聚合上限常量；单文件大小复用 service 的 `MaxFileContentBytes`。
  - 路由：`POST /admin/v1/files/import`（admin 鉴权）。
- 分层：`router → handler（解析 multipart）→ service（事务批量 upsert + 审计 + 唤醒）→ repository`，单向依赖；handler 不碰 GORM / 内存结构。
- 前端：`web/src/api/client.ts` 加 `importFiles()`（FormData，不强加 JSON Content-Type）；`ConfigsPage` 顶部加「导入到组」对话框：选环境 + 组、选本地目录 / 多文件（`webkitdirectory` / `multiple`），上传后提示成功并刷新文件列表。

## 4. 任务拆分
- [x] 规格 + PRD FR-38「计划」→「开发中」
- [x] `file.import` 动作常量 + `FileService.Import` 事务批量 upsert
- [x] `FileHandler.Import` + 路由 + multipart 校验
- [x] service 单测（正常 / 边界 / 错误）+ server 集成（HTTP multipart → file_object → manifest → 审计 → 穿越 / 超限被拒）
- [x] 前端 `importFiles()` + 导入对话框 + 前端测试
- [x] 文档同步：PRD 状态、ARCHITECTURE 通道B 段、API、CHANGELOG

## 5. 验收标准
- 上传 N 个文件到指定组 → 成组级 `file_object`（可被组内实例拉取、出现在 manifest）；二次导入同 path → version+1。
- 导入产生一条 `file.import` 审计（含目标组与文件数）。
- 路径穿越（`../`、绝对路径、反斜杠）被拒；超数量 / 超单文件 / 超总字节被拒。
- handler / service 单测 + 集成（`//go:build integration`）+ 前端测试绿；`go test ./...` 与 `web` `pnpm test` / `pnpm build` 绿。

## 6. 风险 / 待定
- 真机 MC 端到端（导入后组内 agent 实际增量取回落盘）待 E2E / 真机阶段验证，本期以集成测试覆盖控制面闭环。
