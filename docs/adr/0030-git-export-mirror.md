# ADR-0030：git 单向导出镜像——派生备份 / 灾备 / 外部可见，不作第二真源

**状态**：已接受

## 背景

配置 / 文件树的真源是 MySQL（[ADR-0008](0008-config-soft-delete-and-effective-md5.md) 配置软删与有效 md5、[ADR-0010](0010-file-tree-hosting-blob-channel.md) 文件树托管）。但库内事实**对人不可读、对外不可见**：运维要"`git log` 翻历史看谁改了哪个键""把整套配置离线备份 / 灾备到另一处""让外部无侵入地看一眼当前配置形态"，目前只能连库或走管理台。FR-47 要在不动真源、不引重型件、不破坏[架构不变量 #3 真源切分](../../.claude/rules/architecture-invariants.md)的前提下，把配置 / 文件树的**源层**按目录结构**单向导出**成一个 git 仓。

要拍板的点（否则极易跑偏成"双真源 / 阻断发布 / 泄密 / 引重型件"）：

1. git 仓是真源还是派生？会不会被 agent 下发依赖？
2. 导出何时触发、会不会阻断 / 拖慢发布主流程？失败怎么办？
3. 敏感数据（FR-20 配置密文、反向抓取来的第三方插件明文 DB 密码）如何不落 git 明文？
4. git 操作用什么机制实现（纯 Go 库 vs 调 git CLI）？对单二进制部署 / 依赖 / 镜像的影响？

## 决策

1. **git 仓是单向派生镜像，绝不作第二真源。** 数据只从 MySQL **单向**流向 git（发布 / 回滚 / 改派提交后导出），git **永不**回灌库、**永不**参与 agent 下发或有效配置解析。下游一律走 MySQL；git 仓损坏 / 落后 / 不存在，对任何下发零影响。灾备恢复 = **手动 / 脚本化重导入**（保持严格单向，不引入入站同步 / 自动 restore）。这守住[不变量 #3](../../.claude/rules/architecture-invariants.md)："配置 / 版本 / zone 分配 / 审计的真源 = MySQL"，git 只在"存储 / 版本"层、下游于 MySQL。

2. **导出 best-effort、异步、非阻塞，挂在发布事务提交之后，与长轮询唤醒并列。** 触发点在 `ConfigService` / `FileService` / `ZoneService` 的写路径**事务提交成功后**，与 `ChangeNotifier` 的"提交后唤醒"并列——导出在**独立 worker goroutine**串行跑，发布请求线程**只投递信号、立即返回**，绝不在请求线程做 git IO（守[性能约束](../../.claude/rules/architecture-invariants.md)不在请求线程长阻塞）。git 任一步失败（仓库损坏 / 远程不可达 / 鉴权失败）**只记 `WARN`，绝不回滚发布、绝不阻断主流程、绝不影响 agent 下发**。这与 [ADR-0019](0019-health-alert-channel-abstraction.md) 告警通道"逐通道兜错、失败仅 WARN 不阻断扫描"同精神。

3. **导出的是"源层"而非"有效配置"，按覆盖链坐标组织目录。** 导出 `config_item` / `file_object` 的**每一层原始内容**（global / 大区 / 小区 / 单服 各层独立），不是某服合并后的有效配置——让 git 树直观映射覆盖链结构、可 `git log -- 某层路径` 追溯。目录布局 `configs/<ns>/{_global_|<group>/_group_|<group>/zone/<zone>|<group>/server/<serverId>}/<dataId>` 与 `files/<ns>/.../<path>`（详见 [spec](../specs/git-export-mirror.md) §3.3）。每次导出**全量重写工作区再 commit**（简单、天然自愈漂移；50 服规模源层文件数有限、可接受）。commit message 带审计元数据（操作者 / 动作 / 对象 / 版本）。

4. **敏感不泄密：配置导密文 + 文件树 path 级排除。**
   - FR-20 标 `sensitive` 的配置项，导出其库内 **`enc:v1:` 密文原样**（导出源读取**不解密**——解密反而泄明文），密钥（`BEACON_CONFIG_ENCRYPTION_KEY`）不入 git（[ADR-0018](0018-config-encryption-at-rest.md)）。
   - `file_object` **新增 `SensitiveExcluded` path 级标记**：置真则该文件**整体不导出到 git**（库内保留、下发不变，仅 git 镜像排除），用于防"反向抓取（FR-39）来的第三方插件 `database.yml` 明文 DB 密码"落 git。文件级"全有或全无"排除，不做文件内字段级脱敏（与 FR-20 配置项整条加密同思路，避免镀金）。
   - 远程**凭据**走 env、**不入库 yaml**（[rule #14](../../.claude/rules/config-files.md)）；远程 URL 非机密，可写 yaml 或 env 覆盖。

5. **git 操作机制：纯逻辑与 git 实现解耦（`GitRepo` 端口），实现推荐 go-git。** 快照组装 / 路径布局 / 敏感排除 / commit message 渲染 / 触发接线全是**无副作用纯逻辑**（落 `internal/gitexport` 纯函数 + service 编排），经 `GitRepo` 端口接口与真正读写 git 仓的实现解耦——与 [ADR-0005](0005-agent-transport-codec-abstraction.md) 让 core 依赖 `HttpTransport` 端口、具体库只在适配器同构。具体实现二选一：
   - **go-git（`github.com/go-git/go-git/v5`，纯 Go）—— 推荐**：契合单二进制 alpine 部署（[ADR-0002](0002-go-react-embedded-stack.md)、`CGO_ENABLED=0` 静态链接），**无需在运行镜像装 git**、跨平台一致；代价是新增一个第三方 Go 依赖。
   - **shell 调 git CLI —— 备选**：无新 Go 依赖；代价是 alpine 运行镜像**需装 git**（改 Dockerfile）、代码 exec 外部进程（更难测、平台差异多）。

   未配置导出（`enabled=false` 或无实现注入）时用 `NopGitRepo` 空实现（与 `ChangeNotifier` / `PublishRecorder` "可选注入、未注入即 no-op"一致）。

## 理由

- **派生而非真源**：git 天生适合"版本化的文本快照 + 可读历史 + 离线副本"，正好补 MySQL"对人不可读、不可离线翻历史"的短板；但它**最终一致、可丢可重建**的特性决定了它只能是派生镜像，让它当真源会与"注册/健康内存真源、配置/审计 MySQL 真源"的清晰切分冲突、引一致性噩梦。
- **导出源层而非有效配置**：有效配置是 per-server 合并产物（N 服 × M dataId 组合爆炸、且丢失"谁覆盖谁"的结构），导出它既冗余又不可读；源层一一对应库内事实、结构清晰、增量 diff 小。
- **best-effort 非阻塞**：发布是玩家配置命脉，git 是锦上添花的旁路；让旁路阻断命脉是本末倒置。失败仅 WARN、独立 goroutine、提交后触发，保证 git 永远拖不垮发布。
- **端口解耦 + 纯逻辑先行**：把"导出什么、长什么样"（纯逻辑、可穷举测）与"怎么写进 git"（IO、依赖）分开，既能在依赖批准前完成绝大部分工作并测透，也让 go-git / git CLI 可替换、不绑死。
- **推荐 go-git**：单二进制 + alpine 极小镜像是本项目既定部署形态（[ADR-0002](0002-go-react-embedded-stack.md)、ARCHITECTURE §9）；go-git 纯 Go、免装 git、静态链接，最契合，代价仅一个依赖；调 CLI 要改 Dockerfile 装 git、还要处理 exec 与平台差异，反而更重。

## 后果

- 新增 `internal/gitexport` 纯逻辑包（`SourceLayer` / `Snapshot` / `BuildPath` / `BuildSnapshot` / `BuildCommitMessage` + `GitRepo` 端口 + `NopGitRepo`），无副作用、可穷举单测。
- 新增 `service.GitExportService`（串行单 worker、`ExportAsync` 投递不阻塞、读源 → 渲染 → commit、失败 WARN）；各发布服务加 `SetGitExporter` 可选注入、提交后与 `notify` 并列触发。
- 新增导出源只读读取（直读 `config_item` / `file_object` 原始行、**不解密**保密文、含排除标记），是唯一新增 DB 读路径，只读、与发布事务无关。
- `model.FileObject` 增 `sensitive_excluded`（基础布尔、零方言、AutoMigrate 加列、既有行默认 false）；`POST /admin/v1/files` 增可选 `sensitiveExcluded`。
- `internal/config` 增 `GitExportConfig`（`enabled` / `repo-path` / `remote-url` / `remote-branch` / `author-name` / `author-email`，远程凭据走 env）。
- **依赖批准门（blocker）**：往 `go.mod` 加 `github.com/go-git/go-git/v5` 须用户批准（[rule #15](../../.claude/rules/static-analysis.md)）；选 git CLI 则改 Dockerfile 须批准（[rule #16](../../.claude/rules/static-analysis.md)）。批准前只落纯逻辑 + `GitRepo` 端口 + `NopGitRepo` + 触发接线并测透，`goGitRepo` 真实现与 `cmd/beacon` 接通待批准后补。
- **不取代任何既有 ADR**：本 ADR 是净新能力，与 [ADR-0008](0008-config-soft-delete-and-effective-md5.md) / [ADR-0010](0010-file-tree-hosting-blob-channel.md) / [ADR-0018](0018-config-encryption-at-rest.md) 互补；git 只读消费它们已定义的真源与密文边界。
- 密钥轮换 / 历史密文重导 / 自动 restore 均不在本期（范围外，避免镀金），按需另起新 ADR。

## 备选方案

- **git 作第二真源 / 双向同步**：违[不变量 #3](../../.claude/rules/architecture-invariants.md) 真源切分，引一致性冲突与回灌噩梦。否决——严格单向派生。
- **导出合并后的有效配置（per-server）**：组合爆炸、丢覆盖链结构、冗余且不可读。否决——导出源层。
- **导出同步阻塞发布 / 失败回滚发布**：git 旁路拖垮配置命脉，本末倒置。否决——异步 best-effort 仅 WARN。
- **敏感配置导明文（仅靠 git 仓私有）**：依赖远程访问控制，仓一泄即明文外泄、且违 at-rest 初衷。否决——导密文，密钥不入 git。
- **不做文件树排除、全量导文件树**：反向抓取来的第三方插件明文密码会落 git。否决——加 path 级排除标记。
- **git CLI（shell exec）作默认实现**：要改 Dockerfile 装 git、exec 外部进程更难测且平台差异多，与单二进制 alpine 部署相悖。降为备选——推荐 go-git。
- **引消息队列 / 另起 git 服务进程做导出**：违[不变量 #2 禁重型件](../../.claude/rules/architecture-invariants.md)。否决——进程内 worker goroutine 串行化即可。
