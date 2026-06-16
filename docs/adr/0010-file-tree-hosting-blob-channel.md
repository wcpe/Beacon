# ADR-0010：文件树托管 blob 通道（通道B），区别于配置深合并

**状态**：已接受

## 背景

配置中心（通道A，FR-1）按 dataId 做**键级深合并**（标量覆盖 / map 深合并 / list 整替 / null 删键），有效配置整体 md5 见 [ADR-0008](0008-config-soft-delete-and-effective-md5.md)。

但新需求要托管**任意文本文件树**：单插件上百文件、多级嵌套目录、`.yml`/`.js`/`.allin`/`lang` 等多种后缀（实测如 AllinCore 的 `ui-components/**`、`scripts/**`）。这类文件的语义是**整文件**，不是键级合并——对 `.allin`/`.js` 做深合并既无意义又有害。且现有 `config/effective` 把某服全部 dataId 合并后整包返回，对上千文件**根本不扩展**。

## 决策

新增"文件树托管"子系统（称**通道B**），与通道A 平行：

1. **整文件 blob**：文件以相对 `path` 为键、内容整存；scope 覆盖 = **整文件覆盖**（取覆盖链 global←group←zone←server 上拥有该 `path` 的最高层那份），**不深合并**。
2. **增量同步**：控制面为某服算出 `manifest`（path→md5）+ 独立的 `fileTreeMd5`；agent 比对本地已落盘 manifest，只取/删变更文件。`fileTreeMd5` 与配置 md5（ADR-0008）**相互独立**，各自的长轮询唤醒集合分开，互不触发无谓重算。
3. **存储**：文件 blob 落 **MySQL TEXT**（守"无新中间件"与 GORM 可移植；新表禁 `ENUM/SET/JSON` 列，枚举落 `VARCHAR`）；版本/回滚复用 `config_revision` 同款 append-only 思路。
4. **镜像落盘**：agent 把某服生效文件树**镜像到插件真实 dataFolder**（原子写：临时文件 → `fsync` → rename），让既有 File 目录加载器 / WatchService / 本地回退**零改动复用**；本地镜像本身即"控制面不可用时的本地文件回退"。
   - 注意：现有 `SnapshotStore` 的落盘**未做 fsync**，落地本通道时需补 `FileChannel.force`（含父目录），否则崩溃恢复的"先文件后 manifest"持久化序不可靠。
5. **fail-static**：控制面不可用 / 首启无目标态时，**不动任何已落盘文件、不臆测**（比配置 fail-static 更保守——无目标态宁可不覆盖）。

## 理由

- 整文件分发与键级深合并是两种本质不同的语义；强行复用 `config_item` + merge 既不符语义、又不扩展到上千文件。
- 镜像落盘让**无法改源码的三方插件零改动兼容**（它们照旧从磁盘读），并天然实现本地回退。

## 后果

- 新增文件托管相关表（`file_object` / `file_revision` 等，随实现同步 [ARCHITECTURE](../ARCHITECTURE.md) §3 与 [API](../API.md)）。
- 新增 agent 文件同步器（异步、不阻塞 MC 主线程；原子写需补 fsync）。
- 长轮询新增 `fileTreeMd5` 维度，与配置长轮询（[ADR-0006](0006-rest-long-poll-push.md)）并行：复用同一 Hub.Notify，但**唤醒集合独立**，须明确"文件发布后如何算受影响 serverId 集合"。
- 模式二（三方文件覆盖兼容，FR-15）是本通道的一个 profile，额外叠"备份 + 受限重载命令"；**命令执行的安全边界另立 ADR，且 gate 在鉴权（[ADR-0009](0009-control-plane-auth-pulled-forward.md)）之后**。

## 备选方案

- **复用 `config_item` + 深合并通道**：`.allin`/`.js` 无法键级合并，bulk effective 不扩展到上千文件。否决。
- **引对象存储（S3/MinIO）存文件**：违"无新中间件"（架构不变量 §2 / [ADR-0003](0003-no-redis-in-mvp.md)）。否决。
- **只实现 `read(path)` 透传、不落盘**：覆盖不了 AllinCore 那类直接扫真实目录的 File 加载器（绕过 read），且无本地回退。否决。
