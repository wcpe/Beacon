# ADR-0069：交付数据面——控制面 sha256 内容寻址 blob 中转 + agent 流式传输端口

**状态**：已接受

## 背景

FR-165「交付数据面」是 P9 交付编排（变更单模型，FR-162~171）的数据承载层。真实运维动作「换插件 jar + 改插件配置一起灰度到 1000+ 目标」要求把 `.jar`、地图、资源包等大文件从模板源分发到大批目标服，且必须支持内容去重、断点续传、校验与清理。文件内容一律不进命令通道（命令 payload / 审计 detail / 配置对象），只走流式 HTTP 数据面（见 `docs/specs/v2-delivery-orchestration.md` §4.5）。

[ADR-0058](0058-controlled-large-file-sync-channel.md) 已为 Legacy「文件同步中心」确立「控制通道 / 数据通道分离 + 控制面中转 blob」方向，交付编排沿用该方向、不另起炉灶。但既有实现不够用，需要新增能力：

- **控制面缺交付级全局 blob 存储**。FileSync 的 blob（`apps/server/internal/service/file_sync_execution.go` 的 `writeBlobFile` / `blobDir`）按 `taskID` 分目录存放、模块内私有、**无跨任务去重**；存在性只靠 `os.Stat`，**无 HEAD 端点**；下载只有 `io.Copy`、**无 Range 断点**。换 jar 场景同一 jar 内容常覆盖上千服，按 task 分目录会成倍浪费磁盘与带宽。
- **agent 侧现有传输端口无法承载大文件流式**。`agent-core` 的 `transport/HttpTransport.kt` 是全缓冲文本：`HttpRequest.body: String?` + `HttpResponse.body: String`——传文件需整读入内存，撞内存上限、无 `Content-Length` 流、无 Range。`transport/StreamTransport.kt` 只是 server→agent 单向 SSE 接收（事件 `data` 仅为通道 md5 通知、不含内容），不是双向大文件流。二者都不能支撑交付级流式上传 / 下载。

因此需要：控制面新增全局 sha256 内容寻址 blob 中转，agent 新增一个流式传输 core 端口——后者是 [ADR-0005](0005-agent-transport-codec-abstraction.md)「HTTP / JSON 库只在适配器、core 依赖抽象」在流式场景的延伸，并借鉴 [ADR-0010](0010-file-tree-hosting-blob-channel.md) / [ADR-0058](0058-controlled-large-file-sync-channel.md) 的 blob 先例。

## 决策

1. **agent 新增 core 传输端口 `BlobStreamTransport`（core 只见接口，守 [ADR-0005](0005-agent-transport-codec-abstraction.md) 与架构不变量 #5）**。与既有 `HttpTransport`（全缓冲文本）、`StreamTransport`（单向 SSE 接收）并列为第三个传输端口，方法约定：
   - `head(url, headers) → 存在 / 就绪 / size`：去重与断点判断。
   - `upload(url, headers, contentLength, body: () -> InputStream) → outcome`：流式上传，`Content-Length` 必填。
   - `download(url, headers, rangeStart, sink: OutputStream) → bytesWritten / outcome`：流式下载，`rangeStart>0` 时走 Range 续传。
   
   core 不碰具体库；OkHttp 流式实现（source / sink + Range + `Content-Length`）落 `agent-adapters` 的 `OkHttpBlobStreamTransport.kt`，为唯一触碰 OkHttp 的类。agent 侧文件读写与 HTTP 全程流式、跑异步线程（TabooLib async），不整读入内存、不阻塞 MC 主线程（架构不变量 #5）。

2. **控制面新增全局 sha256 内容寻址 blob 中转存储**。
   - 磁盘布局 `<data-dir>/delivery/blobs/<sha256 前 2 位>/<sha256>`；上传先写 `<data-dir>/delivery/tmp/` 再原子 rename 进 blob 目录。
   - 元数据表 `delivery_blob`：`sha256`（唯一索引，内容寻址主身份）/ `size_bytes` / `state`（`uploading` / `ready`）/ `last_referenced_at`（清理依据）/ `created_at`。枚举落 `VARCHAR` + 应用层校验，禁 MySQL 专有特性（架构不变量 #4，可切 Postgres）。
   - **同 sha256 天然全局去重**：多个变更单、多个文件路径共享同一 blob。
   - 复用 FileSync `writeBlobFile` 的「`TeeReader` + sha256 边收边算 + 临时文件 + 原子 rename + 落盘前哈希比对」范式，但**增强三点**：① 全局去重（FileSync 按 `taskID` 分目录、无去重）；② 新增 HEAD 存在性 / 就绪端点（FileSync 无）；③ GET 支持 `Range` 断点续传（FileSync 只有 `io.Copy`）。

3. **新增流式路由组 `/beacon/v2/stream/delivery/blobs/{sha256}`（HEAD / PUT / GET+Range）**。
   - `HEAD`：存在性 / 就绪查询（`ready` 才算可下载 / 可跳过上传）。
   - `PUT`：模板源流式上传，`Content-Length` 必填；控制面写临时文件、边收边算 sha256，与 URL 声明比对——不符 `422` 丢弃，一致则原子 rename 并置 `ready`。
   - `GET`：目标流式下载，支持 `Range` 断点续传（中断后从已收字节续拉）。
   - 走 agent 双 header 鉴权（`X-Beacon-Token` namespace 级 + `X-Beacon-Identity`）+ **blob 归属校验中间件**：请求 identity 须属于持有该 blob 引用的活动变更单——模板源仅可上传本单待传 sha256，目标仅可下载本单清单内 sha256（spec §5.3）。

4. **资源约束与清理走运维设置热更（[ADR-0038](0038-ops-settings-store-hot-reload.md)）+ 后台清理器 goroutine**。
   - 并发上限：上传默认 4 流、下载默认 64 流；容量上限：磁盘默认 20 GiB，超限拒绝新上传并报明确错误（不静默，遵 [ADR-0057](0057-surface-desensitized-errors.md)）。
   - blob 清理：满足「所有引用它的变更单均达终态 / cancelled 且超保留期（默认 7 天）」且「不被任何活动单（rolling / paused / rolling_back）引用」才删（元数据 + 磁盘）；`uploading` 残留超 24h 一并清除。清理入审计（actor=system）。
   - 命令通道 payload **绝不含文件内容**（spec §4.5.1）；文件内容只经本流式面传输。

## 理由

- 沿用 [ADR-0058](0058-controlled-large-file-sync-channel.md) 已验证的「控制 / 数据通道分离 + 控制面中转」方向，交付编排复用而非重造；控制面中转把源服压力从「每个目标读源」降为「源上传一次、目标分批拉取」，也让暂停 / 重试 / 断点 / 回滚可从库内状态恢复。
- 全局 sha256 内容寻址天然去重：换 jar 场景同一 jar 覆盖上千服只存一份、只从源上传一份，显著省控制面磁盘与源服带宽——这正是 FileSync 按 task 分目录方案拿不到的收益。
- HEAD + Range 让弱网下上传可跳过已存在 blob、下载可断点续传，抗中断、抗重传。
- core 只依赖 `BlobStreamTransport` 抽象、OkHttp 流式只在适配器，守 [ADR-0005](0005-agent-transport-codec-abstraction.md) 与架构不变量 #5：可测（注入假 transport）、可换库（与 TabooLib / 其他插件冲突时只换适配器）。
- 保持 agent 出站模型，不给 MC 服务器新增入站端口，符合现有网络与安全边界。
- blob 落磁盘、元数据落 MySQL（`VARCHAR` 枚举、无专有特性），守「无新中间件」与 GORM 可移植（架构不变量 #2 / #4）。

## 后果

- 新增 `delivery_blob` 表、控制面 blob 中转存储、流式路由组、blob 归属校验中间件、后台清理器 goroutine。
- agent 新增 `BlobStreamTransport` core 端口与 `OkHttpBlobStreamTransport` 适配器；双端 jar 需重建并真机部署。
- 新增流式面契约需同步 [`docs/API.md`](../API.md)（§5.3）与 [`docs/ARCHITECTURE.md`](../ARCHITECTURE.md)。
- 新增流式 IO、sha256 校验、Range 续传、全局去重、并发 / 容量上限、清理与鉴权归属校验的测试。
- 数据面默认参数（上传并发 4 / 下载并发 64 / 20 GiB / 保留 7 天）为初始值，需按真机带宽与磁盘实测校准（spec §8 #4）。
- 旧 agent 不具备 `BlobStreamTransport`；控制面须在启动 / 组单时校验目标 agent 能力版本，不对旧 agent 下发交付命令。

## 备选方案

- **agent 直传目标（源 agent 对每个目标点对点推送）**：省控制面中转一跳，但 1000+ 目标反复压源服，控制面无法审计 / 去重 / 统一鉴权，暂停 / 重试 / 断点 / 回滚难以从库内恢复。否决（同 ADR-0058 已否的「源对每目标实时转发」）。
- **沿用 FileSync 按 `taskID` 分目录的 blob（不做全局去重）**：改动最小，但换 jar 场景同一内容在每单 / 每任务各存一份，磁盘与带宽成倍浪费，失去内容寻址的核心价值。否决。
- **复用现有 `HttpTransport` 全缓冲 `body: String` 传文件**：需整读入内存，撞内存上限、无流式与断点、阻塞风险高。否决。
- **引对象存储（S3 / MinIO）存 blob**：违「无新中间件」（架构不变量 #2 / [ADR-0003](0003-no-redis-in-mvp.md)）。否决。
- **文件内容塞命令 payload / JSON ingest**：破坏命令生命周期、审计、内存与 DB 边界（同 [ADR-0058](0058-controlled-large-file-sync-channel.md) 已否）。否决。
