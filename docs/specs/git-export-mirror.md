# 功能规格：git 单向导出镜像（备份 / 灾备 / 外部可见）

> 状态：开发中　·　关联 PRD：FR-47　·　分支：feature/fr-47-git-export

## 1. 背景与目标

配置 / 文件树的真源是 MySQL（[ADR-0008](../adr/0008-config-soft-delete-and-effective-md5.md)、[ADR-0010](../adr/0010-file-tree-hosting-blob-channel.md)）。但库内事实**对人不可读、对外不可见**：运维想"翻历史看谁改了哪个键""把整套配置离线备份 / 灾备到另一处""让外部审计无侵入地看一眼当前配置形态"时，只能连库或走管理台。

目标：在不动真源、不引重型件的前提下，把配置 / 文件树的**源层**（global / 大区 / 小区 / 单服 各层，非合并后的有效配置）按目录结构**单向导出**成一个 git 仓库——发布 / 回滚后**异步、非阻塞、best-effort** 地 commit，commit message 带审计信息（操作者 / 动作 / 版本），可选推送远程（GitHub / Gitea）。git 仓是**派生镜像**：备份、灾备、外部可见、可 `git log` 翻历史，但**绝不作第二真源**、不参与 agent 下发。属 P3、净新 feat。

## 2. 需求（要什么）

- 范围内：
  - **源层导出**：导出 `config_item` 与 `file_object` 的**每一层原始内容**（不是某服合并后的有效配置），按 `namespace / group / scope / target` 目录组织，文件名取 dataId / path。让 git 树直观映射覆盖链结构。
  - **触发时机**：挂在既有发布 / 回滚 / 改派的 DB 事务**提交成功之后**，与 `ChangeNotifier` 长轮询唤醒**并列、互不阻塞**——导出在独立 goroutine 跑，主流程不等它。
  - **best-effort 不阻断**：git 任一步失败（仓库损坏 / 远程不可达 / 鉴权失败）只记 `WARN` 日志，**绝不回滚发布、绝不阻断主流程、绝不影响 agent 下发**。
  - **commit message 带审计**：每次导出 commit 的 message 含操作者 / 动作（config.publish 等）/ 受影响对象 / 版本，便于 `git log` 追溯"这次 commit 对应哪次发布"。
  - **本地裸仓 + 可选远程**：导出到一个本地仓（路径走配置），远程 URL / 凭据**走配置 / env、不入库**（[rule #14](../../.claude/rules/config-files.md)），配置了才推、没配只本地 commit。
  - **敏感不泄密**：
    - 配置项 FR-20 标 `sensitive` 的，导出其 **`enc:v1:` 密文**（与库内 at-rest 一致），密钥不入 git，明文绝不落 git。
    - 文件树新增 **path 级敏感标记 / 排除导出**：防"反向抓取（FR-39）来的第三方插件 `database.yml` 里的明文 DB 密码"被导出到 git。标记的文件从 git 导出中**整体排除**（git 里看不到该文件），库内仍保留。
  - **灾备恢复 = 手动 / 脚本化重导入**：保持单向，不引入入站同步 / 自动 restore（本期不做 restore 工具，留文档说明从 git 重建库的思路）。

- 不做（范围外）：
  - **不做入站同步 / 自动 restore**：git → 库的回灌不在本期（保持严格单向，守[架构不变量 #3](../../.claude/rules/architecture-invariants.md) 真源切分）。
  - **不把 git 当第二真源**：下游（agent 下发、有效配置解析）一律走 MySQL，git 仓挂掉 / 落后不影响任何下发。
  - **不引重型件**：不引消息队列 / 不另起 git 服务进程；本地 git 操作内嵌在控制面进程。
  - **不导出运行态事实**：注册 / 健康 / 指标样本（内存或时序）不导出（它们不是"配置事实"、易朽、无备份价值）。
  - **不导出覆盖集（FR-15）成员**：覆盖集成员（`override_set_id>0`）本期不纳入导出（避免与 targetRoot 语义纠缠），仅导出通用托管文件（`override_set_id=0`）。

## 3. 设计（怎么做）

涉及架构决策（新引 git 导出镜像、go-git vs git CLI 取舍、文件树 path 级敏感排除）→ 另写 [ADR-0030](../adr/0030-git-export-mirror.md)，此处不重复决策正文。

### 3.1 分层与依赖（守分层单向 + 简单优先）

- **`internal/gitexport`（纯逻辑包，无副作用、无第三方 git 依赖）**：导出快照的**组织与渲染**。
  - `SourceLayer`：一条源层记录（namespace / group / scopeLevel / scopeTarget / 名称 / 内容 / 是否敏感排除）。
  - `Snapshot`：一次导出的全量文件集 `map[相对路径]文件内容`，由源层记录纯函数组装。
  - `BuildPath(layer) string`：把一条源层映射到 git 仓内的相对路径（目录布局，见 §3.3），纯函数、确定性。
  - `BuildSnapshot(configLayers, fileLayers) Snapshot`：组装全量快照；敏感配置项内容取**密文原样**，敏感排除的文件**不进快照**。
  - `BuildCommitMessage(meta) string`：由审计元数据（操作者 / 动作 / 对象 / 版本）渲染 commit message，纯函数。
  - **可穷举单测**：路径布局、敏感排除、密文保留、commit message 渲染全是纯函数，不碰 git、不碰 DB。

- **`GitRepo` 端口接口（在 `internal/gitexport` 定义，依赖倒置，守[不变量 #5 同构思路](../../.claude/rules/architecture-invariants.md)）**：
  ```
  type GitRepo interface {
      // 用快照全量覆盖工作区、提交一次；configured 远程则推送。失败返回 error（由调用方降级为 WARN）。
      Commit(snapshot Snapshot, message string) error
  }
  ```
  - 实现 `goGitRepo`（go-git，**依赖门后接通**，见 §3.5）放在 `internal/gitexport`（或子包 adapter），是唯一 import go-git 的地方；纯逻辑不依赖它。
  - 未配置导出（`enabled=false` 或无实现注入）时用 `NopGitRepo` 空实现 —— 与 `ChangeNotifier`、`PublishRecorder` 等"可选注入、未注入即 no-op"一致。

- **`service.GitExportService`（导出编排，service 层）**：
  - 持有：导出源仓库（读 `config_item`/`file_object` 原始行）+ `GitRepo` 端口 + 串行化队列。
  - `ExportAsync(meta ExportMeta)`：发布路径调用，**只投递信号不阻塞**——内部单 worker goroutine 串行消费：读全量源层 → `BuildSnapshot` → `gitRepo.Commit`，任一步失败 `WARN` 不抛。
  - **串行单 worker**：git 工作区是单写资源，导出请求经一个缓冲 channel 串行化（满则丢最新触发的"合并到下一次全量导出"——反正每次都是全量快照、不丢数据），杜绝并发写同一 git 仓。

### 3.2 导出源读取（不解密、保密文）

- 新增 `repository.ExportSourceRepository`（或在导出服务内直读 GORM），**直接读 `config_item` / `file_object` 原始行、不经 `ConfigItemRepository.decryptInPlace`**——因为：
  - 敏感配置项库内 `content` 即 `enc:v1:` 密文，**原样读出正是 FR-47 要导出的密文**（解密反而泄明文）。
  - 非敏感项 `content` 即明文，原样导出。
- 只读未软删（`deleted_at = 哨兵`）、`enabled=true`、`override_set_id=0`（文件）的行。
- 这是**唯一新增的 DB 读路径**，只读、不写、与发布事务无关（导出在事务提交后异步跑）。

### 3.3 git 仓目录布局

源层按覆盖链结构组织，directory = 覆盖链坐标，便于人肉浏览与 `git log -- 某路径`：

```
configs/
  <namespace>/
    _global_/<dataId>                 # scope=global（group=__GLOBAL__ 归一为 _global_ 目录）
    <group>/_group_/<dataId>          # scope=group
    <group>/zone/<zone>/<dataId>      # scope=zone（target=zone 编码）
    <group>/server/<serverId>/<dataId># scope=server（target=serverId）
files/
  <namespace>/
    _global_/<path>                   # 同上四层，文件按其相对 path 落子目录
    <group>/_group_/<path>
    <group>/zone/<zone>/<path>
    <group>/server/<serverId>/<path>
```

- `__GLOBAL__` 占位 group 在 git 路径里渲染为 `_global_`（避免双下划线保留字直接出现在路径、也避免与真实 group 名碰撞——真实 group 名禁用 `__GLOBAL__`，见 `normalizeScope`）。
- `scope=global` 不再嵌 group 段（global 本就跨大区），直接 `configs/<ns>/_global_/<dataId>`。
- 路径段对 dataId / path / target 做**最小安全清洗**（去 `..` / 绝对前缀；这些值入库时已过 `normalizePath` / scope 校验，导出再兜一道防御性 join，纯函数可测）。
- 敏感配置项：照常导出到对应路径，**内容为 `enc:v1:` 密文**。
- 敏感排除的文件：**该路径在 git 里不存在**（快照里没有该 key）。

### 3.4 触发接线（提交后、与唤醒并列）

- `ConfigService.Create/Publish/Rollback/Delete`、`FileService.Create/Import/Publish/Rollback/Delete`、`ZoneService`（改派影响归属）在事务提交成功、`s.notify(...)` 之后，再调 `s.exportGit(meta)`——与 `notify` 并列的"提交后副作用"，**各自独立、互不阻塞**。
- `exportGit` 仅在注入了 `GitExportService` 时触发（`SetGitExporter` 可选注入，未注入即 no-op，与 `SetNotifier` 同构）。
- `ExportAsync` 内部立即返回（投 channel），真正的"读源 + 渲染 + commit"在 worker goroutine，**绝不在发布请求线程里做 git IO**（守[性能约束 #17](../../.claude/rules/architecture-invariants.md) 不在请求线程做长阻塞）。

### 3.5 git 操作机制：go-git vs git CLI（**依赖批准门**，见 [ADR-0030](../adr/0030-git-export-mirror.md)）

两条路，取舍写进 ADR-0030、**推荐 go-git**：

- **go-git（`github.com/go-git/go-git/v5`，纯 Go）**：契合单二进制 alpine 部署（无需镜像装 git）、跨平台一致；代价是**新增第三方 Go 依赖**（[rule #15](../../.claude/rules/static-analysis.md)：加依赖须先经用户批准）。
- **shell 调 git CLI**：无新 Go 依赖；代价是 alpine 运行镜像**需装 git** = 改 Dockerfile（[rule #16](../../.claude/rules/static-analysis.md)：未经指示禁改 Dockerfile），且要在代码里 exec 外部进程（更难测、更多平台差异）。

**本批落地边界**：纯逻辑（快照组装 / 路径布局 / 敏感排除 / commit message / 触发接线 / `GitRepo` 端口 + `NopGitRepo`）全部完成并单测；**`goGitRepo` 真接 go-git 的实现卡在依赖批准门**——接口与桩先就位，标注"待批准 go-git 依赖后接通"，**不擅自改 `go.mod` / `Dockerfile`**。

### 3.6 配置项（`internal/config`）

新增 `GitExportConfig`（yaml 字段 kebab-case、每字段中文注释，见 [config-files 规范](../../.claude/rules/config-files.md)）：

```yaml
# git 单向导出镜像（备份 / 灾备 / 外部可见，FR-47）；不作第二真源、失败不阻断发布
git-export:
  # 是否启用导出；false 时完全不导出（默认 false，属可选增强）
  enabled: false
  # 本地 git 仓路径（导出 commit 落此目录）
  repo-path: "beacon-config-export"
  # 可选远程推送地址（GitHub/Gitea，空则只本地 commit 不推送）
  remote-url: ""
  # 远程推送分支
  remote-branch: "main"
  # commit 作者名 / 邮箱（仅用于 git 提交身份，非鉴权）
  author-name: "beacon"
  author-email: "beacon@local"
```

- 远程**凭据**（token / 密码）走 env（如 `BEACON_GIT_EXPORT_REMOTE_TOKEN`），**不写入库 yaml**；远程 URL 可写 yaml（非机密）或 env 覆盖。

### 3.7 文件树 path 级敏感排除（数据模型）

- `model.FileObject` 增 `SensitiveExcluded bool`（列 `sensitive_excluded`，`NOT NULL DEFAULT false`，基础布尔、零方言、可切 Postgres；AutoMigrate 加列，既有行默认 false）。
- 置真 = 该文件**不导出到 git**（库内不变、下发不变，仅 git 镜像排除）。
- 写路径：`FileService.Create` 透传；admin `POST /admin/v1/files` 新增可选布尔字段 `sensitiveExcluded`（缺省 false）。前端 UI 最小或随后续完善；本 FR 重点是后端能力。
- **命名辨析**：与 `WholeFileOverride`（FR-44，合并行为）正交——一个管"是否深合并"、一个管"是否导出 git"，互不影响。

## 4. 任务拆分

- [ ] [ADR-0030](../adr/0030-git-export-mirror.md)：git 单向导出镜像（单向派生 / 非第二真源 / best-effort 非阻塞 / 敏感密文 + 文件树排除 / go-git vs CLI 取舍与推荐 go-git）；adr/README 加 0030 行。
- [ ] PRD FR-47 → 开发中。
- [ ] `internal/gitexport`：`SourceLayer` / `Snapshot` / `BuildPath` / `BuildSnapshot` / `BuildCommitMessage` 纯函数 + `GitRepo` 端口 + `NopGitRepo`（穷举单测先行）。
- [ ] `internal/config`：`GitExportConfig` + 默认 + env 覆盖（远程凭据走 env）。
- [ ] `internal/model`：`FileObject.SensitiveExcluded` + AutoMigrate。
- [ ] `internal/repository`：导出源只读读取（不解密、保密文、含敏感排除标记）。
- [ ] `internal/service`：`GitExportService`（串行 worker、ExportAsync、读源 → 渲染 → commit）+ 各发布服务 `SetGitExporter` 接线（提交后触发，与 notify 并列）。
- [ ] `internal/handler`：`POST /admin/v1/files` 透传 `sensitiveExcluded`。
- [ ] **依赖门后**：`go.mod` 加 go-git + `goGitRepo` 真实现 + 接通 `cmd/beacon` 装配（**待批准**）。
- [ ] 文档同步：ARCHITECTURE（数据模型 + 触发机制）、API（files 入参字段）、CHANGELOG 未发布段末尾追加一行。

## 5. 验收标准

- `BuildPath` 把 global / group / zone / server 四层配置项与文件分别映射到 §3.3 的确定性路径；穷举单测覆盖四层 + `__GLOBAL__` 归一 + 防御性清洗。
- `BuildSnapshot`：敏感配置项导出 `enc:v1:` 密文（不解密），敏感排除文件不进快照，普通项明文 / 整文件原样。
- `BuildCommitMessage` 含操作者 / 动作 / 对象 / 版本，可由 `git log` 追溯。
- `GitExportService.ExportAsync` 非阻塞：发布路径调用立即返回，git 失败仅 WARN、发布主流程与 `notify` 不受影响（单测以失败 `GitRepo` 桩验证发布仍成功）。
- `FileObject.SensitiveExcluded` 经 Create 透传持久化；导出源读取保留该标记并据此排除。
- 受影响组件纯逻辑测试全绿（`go test ./...`）。依赖门卡住的 `goGitRepo` 真实现如实标 blocked、不冒充完成。
- **真机 / 集成**（依赖批准接通 go-git 后、发版前）：发布一次配置 → 本地 git 仓多一个 commit、message 带审计、源层文件按目录就位、敏感项为密文；配置远程后 push 成功；杀掉 git 仓目录或断网 → 发布仍成功、仅 WARN。

## 6. 风险 / 待定

- **go-git 依赖批准门（blocker）**：往 `go.mod` 加 `github.com/go-git/go-git/v5` 需用户批准（rule #15）；或改走 git CLI 需批准改 Dockerfile（rule #16）。本批在批准前只落纯逻辑 + 接口 + 桩，`goGitRepo` 真实现待批准后接通。
- **全量快照 vs 增量 commit**：本期每次导出**全量重写工作区**再 commit（实现简单、天然自愈漂移），50 服规模下源层文件数有限、可接受；若将来文件数巨大需改增量 diff，另议。
- **敏感排除是"全有或全无"**：文件级排除，不做文件内字段级脱敏（与 FR-20 配置项整条加密同思路，避免镀金）。第三方插件明文密码场景靠"整文件排除"覆盖。
- **commit 作者身份非鉴权**：author-name/email 仅 git 提交元数据，不参与任何鉴权。
- **不做 restore**：从 git 重建库是手动 / 脚本化的离线操作，本期仅文档说明思路、不出工具。
