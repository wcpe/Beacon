# 功能规格：文件树托管（通道B）

> 状态：开发中　·　关联 PRD：FR-14　·　分支：见 worktree

## 1. 背景与目标
配置中心（通道A，FR-1）按 dataId 做键级深合并，适合 yaml/json 结构化配置。但下游插件常需托管**任意文本文件树**（上百文件、多级目录、`.yml`/`.js`/`.allin`/`lang` 等），这类文件语义是**整文件**而非键级合并。本功能新增"文件树托管"子系统（通道B，见 [ADR-0010](../adr/0010-file-tree-hosting-blob-channel.md)），与通道A 平行，属第二期（P2）。

## 2. 需求（要什么）
- 整文件 blob：以相对 `path` 为键整存内容；scope 覆盖 = **整文件覆盖**（取覆盖链 global←group←zone←server 上拥有该 path 的最高层那份），不深合并、不碰 merge 包。
- 增量同步：控制面为某服算 `manifest`（path→md5）+ 独立 `fileTreeMd5`（与配置 md5 解耦），agent 比对本地已落盘 manifest 只取/删变更文件。
- 管理台文件 CRUD/发布；版本/回滚复用 append-only 思路。
- 长轮询：`fileTreeMd5` 接入，与配置 md5 **独立唤醒集合**；文件发布后按 scope 算受影响 serverId 再唤醒。
- agent 侧：文件同步器（manifest 比对增量取/删）+ 镜像落盘（原子写 临时文件→fsync→rename）；fail-static。
- 范围内：控制面模型/仓库/服务/路由/长轮询；agent 同步器 + 原子落盘。
- 不做（范围外）：FR-15 三方覆盖兼容的备份 + 受限重载命令（命令安全边界另立 ADR、gate 在鉴权之后）；鉴权（P2，另 FR）。

## 3. 设计（怎么做）
- 数据模型：`file_object`（namespace/group/scope_level/scope_target/path/content TEXT/content_md5/version/enabled + 软删哨兵，唯一键含 path）、`file_revision`（append-only），参照 `config_item.go` / `config_revision.go` / `softdelete.go`。GORM、禁 ENUM/SET/JSON 列、枚举落 VARCHAR。
- 解析：新增 `internal/filetree` 包（纯函数），按覆盖链取每个 path 的最高层那份、算 manifest 与 `fileTreeMd5`（沿用 ADR-0008 把标识名串入哈希防碰撞）。
- 分层：`repository.FileObjectRepository` / `FileRevisionRepository` → `service.FileService`（事务内 object+revision+audit 原子）+ `service.FileEffectiveService`（解析 + 长轮询）→ `handler.FileHandler`（admin）/ agent 端点。
- 长轮询：复用 `longpoll.Hub`，新增独立 Hub 实例供文件通道，避免与配置唤醒集合互相触发；`ChangeNotifier` 增 `NotifyFileChange`。

## 4. 任务拆分
- [x] 数据模型 file_object / file_revision + AutoMigrate
- [x] filetree 解析包（scope 整文件覆盖 + manifest + fileTreeMd5）+ 穷举单测
- [x] repository（FileObject / FileRevision）
- [x] service（FileService 发布/回滚/软删 + FileEffectiveService 解析/长轮询）
- [x] handler + 路由（admin CRUD/发布；agent manifest/content）
- [x] 长轮询独立唤醒集合接入
- [x] agent 侧文件同步器 + 原子落盘（fsync）：FileSyncer 差分 + FileMirrorWriter 原子写（含父目录 fsync）+ AppliedFileManifestStore + FileTreeApplier + 文件树长轮询循环；fail-static 比配置更保守
- [x] 文档同步：PRD 状态、ARCHITECTURE §3、API、CHANGELOG

## 5. 验收标准
- scope 整文件覆盖：穷举单测覆盖单层/多层覆盖/高层删 path/不同 path 并存。
- `fileTreeMd5` 幂等：相同输入恒得相同 md5，path 名纳入哈希防集合碰撞，内容变即变。
- DB 可移植：无方言专有列。
- 受影响组件单测全绿。

## 6. 风险 / 待定
- agent 侧（Kotlin）文件同步器与原子落盘（含父目录 fsync）已落地，`:agent-core:test` 与双端 `build` 通过。
- `resync` 运维命令（FR-17）强制重同步文件树的实际接线已补：`AgentLifecycle.forceSyncFileTreeNow` 旁路文件树长轮询 304、以空 `fileTreeMd5` 强拉一次清单并由 `FileTreeApplier` 幂等落盘，双端壳 `/beacon resync` 接通（仅在 `file-tree.enabled` 时生效）。真机 MC 端到端文件热更验证待 E2E 阶段补。
