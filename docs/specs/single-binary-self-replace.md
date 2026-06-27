# 功能规格：控制面单进程二进制自替换 + 自动回滚

> 状态：开发中　·　关联 PRD：FR-119　·　分支：master（本仓直接 master 线性提交）

## 1. 背景与目标

现状（FR-96/FR-97，[ADR-0045](../adr/0045-builtin-launcher-supervisor.md)/[ADR-0044](../adr/0044-control-plane-online-self-update.md)）：控制面发布**两个二进制**——主二进制 `beacon` + 独立监督进程 `beacon-launcher`。在线更新靠「主进程落位 `beacon.new` → 以退出码 `70` 退出 → launcher 换二进制重启」的退出码协议完成。

三个问题驱动本次重构：
1. **部署冗余**：要分发并摆放两个文件，容器 ENTRYPOINT 也指向 launcher。
2. **原 ADR 技术前提有误**：ADR-0044/0045 反复断言「Windows 运行中 `.exe` 无法被自身进程覆盖 → 主进程自换必败 → 必须 launcher」。这**混淆了「覆盖」与「rename 让位」**——Windows 不允许覆盖运行中的 exe，但**允许 rename 它**（移动目录项，已打开的文件句柄仍指向原 inode）。姊妹项目 JianVideo 已用「rename 让位三步」在单进程内稳定自替换，证伪了「必须 launcher」前提。
3. **崩溃自启重复**：launcher 的「进程崩了自动拉起」在生产环境本就由 docker `restart` / systemd `Restart=` 提供；裸跑场景才独有，但裸跑非生产形态。

目标：**去掉 launcher，主进程单进程自我替换**，并补上 launcher 原本**不具备**的「新版起不来自动回退」能力（闭合崩溃循环）。崩溃自启明确交外部监督（docker/systemd）。阶段 P2。

## 2. 需求（要什么）

**范围内**：
- **主进程自替换**：在线更新下载校验落位 `beacon.new` 后，主进程优雅关停 HTTP（释放端口）→ rename 让位三步（`beacon`→`beacon.old`、`beacon.new`→`beacon`）→ spawn 新进程（继承参数/环境/工作目录/标准流）→ 旧进程退出。换二进制失败则就地回退、spawn 旧版继续服务（回退兜底，等价原 launcher「换失败按旧版重启」）。
- **自动回滚（崩溃循环闭合）**：换二进制成功后写 sentinel 标记；新版启动**早期自检**——① 稳定运行过验证期（10s）或 ② 收到正常关停信号（管理员介入）即判定成功，删 sentinel + 删 `.old`；若换版后**反复起不来**（崩溃计数达阈值）则在 `main` 启动早期自动 rename 回退 `.old` 并重启旧版，**不依赖任何外部进程**。
- **删除 launcher 与退出码协议**：删 `cmd/beacon-launcher/`（6 文件）+ `internal/exitcode/`（2 文件）+ 主进程退出码 70 路径。
- **构建/容器去 launcher**：`Makefile`、`Dockerfile`、`.github/workflows/_build-release.yml`（用户已授权这些关键文件）。
- **文档**：写 [ADR-0053](../adr/0053-single-binary-self-replace.md) 取代 ADR-0045 + 部分取代 ADR-0044 决策 5/6/7（旧 ADR 仅改状态行、正文不动）；`docs/OPERATIONS.md` 补 systemd/docker restart 推荐部署；同步 ARCHITECTURE/API/online-update/PRD/CHANGELOG。

**不做（范围外）**：
- **手动回滚 API（`POST /system/rollback`）+ 前端版本页回滚按钮 → FR-120**（依赖本条落地的 swap/`.old` 机制）。
- agent 不涉及（agent 自管身份、走自己发布渠道）。
- 增量/差分更新（每次全量替换，YAGNI）。
- `.old` 多版本保留（仅留 1 份上一版本）。

## 3. 设计（怎么做）

新增 `internal/update` 的自替换子模块（rename 让位 + sentinel 自检 + 回滚），跨平台逻辑从 `cmd/beacon-launcher/swap_*.go` 迁入并按 `//go:build` 分平台。主进程 `cmd/beacon` 接通「触发自替换」与「启动自检」两个挂载点。详细决策见 [ADR-0053](../adr/0053-single-binary-self-replace.md)，此处只给时序与契约、不重复决策正文。

### 3.1 落位与触发（基本沿用 FR-97）
`internal/update/service.go` 的 `ApplyUpdate`：查 Release → 下载 → SHA256 校验 → `rename` 落位 `beacon.new`（同目录同卷，路径仍由 `resolvePendingPath` 推导）→ 触发 `requestRestart` 回调（关 `updateRestartCh`）。**这条链路不变**，仅去掉注释里「交还 launcher」字样、改为「触发主进程自替换」。

### 3.2 自替换重启（替换原「退出码 70 交还 launcher」）
`cmd/beacon` 的 `run()` 在 `select` 收到 `updateRestartCh`：
1. 优雅关停 HTTP server（`srv.Shutdown`，释放端口、等 in-flight 请求含 `202` 回包完成）；
2. 调 `update.SwapAndRespawn(runPath, pendingPath)`：
   - `landBinary`：rename 让位三步（**Windows**：`beacon.exe`→`beacon.old.exe`→ `beacon.new.exe`→`beacon.exe`→删旧；**Unix**：`beacon`→`beacon.old`、`beacon.new`→`beacon`）。失败则就地回退（`.old` 改回），返回错误标记「未换成」；
   - 换成功 → 写 sentinel（`beacon.update-pending`，`attempt=0` + 目标版本）；换失败 → 不写 sentinel；
   - `respawn`：以原参数/环境/工作目录/标准流 spawn `beacon`（成功换=新版、失败=旧版），本进程随后返回、退出。
3. **无需 JianVideo 的 800ms 延时**——Beacon 先优雅关停（响应已回包、端口已释放）再 spawn，新进程可立即绑定端口。

### 3.3 启动早期自检 + 自动回滚（全新，JianVideo 无）
`cmd/beacon` 的 `run()` **入口**（HTTP 起之前）调 `update.CheckAndAutoRollback(runPath)`：
- 读 sentinel：**不存在** → 非换版后首启，正常启动，返回；
- **存在** → 换版后启动：`attempt++` 写回，
  - 若 `attempt >= maxStartAttempts`（默认 3，即容忍 2 次崩溃后第 3 次启动果断回退）→ **自动回滚**：`beacon`→`beacon.failed`、`beacon.old`→`beacon`、删 sentinel、spawn 旧版、本进程退出（不再继续启动新版）；
  - 否则启动**验证定时器** goroutine：`Sleep(10s)` 后仍存活 → 删 sentinel + 删 `.old`（确认成功、清理备份），继续正常启动。
- **双路径确认成功**：① 验证定时器 10s；② `run()` 的 `ctx.Done()`（SIGINT/SIGTERM 正常关停=管理员/docker stop 介入，说明新版至少起来了被操作）也删 sentinel + `.old`。崩溃（panic/`os.Exit`/fatal，不走 `ctx.Done`）则两者都不触发、sentinel 保留累加 → 外部重启累加至阈值 → 回滚。

> 崩溃自启的前提是**外部监督**（docker `restart`/systemd `Restart=`）会重新拉起崩溃进程，自检在每次重启时累加 attempt、达阈值回滚。裸跑无外部监督时新版崩溃即停（用户已接受：崩溃自启交外部监督），但**仍可**靠下次手动启动触发自检回滚。

### 3.4 文件命名与生命周期
- `beacon.old[.exe]`：换版让位时由 `beacon` rename 而来，仅留 1 份；验证成功（10s/正常关停）即删；自动回滚时被 rename 回 `beacon` 消费掉。
- `beacon.new[.exe]`：更新落位的 pending 新二进制（沿用 FR-97 路径约定），换版 rename 后消失。
- `beacon.update-pending`：sentinel 小文件（JSON `{attempt, version}`），运行二进制同目录；换成功时写、验证成功/回滚时删。
- `beacon.failed[.exe]`：自动回滚时坏新版的归档（便于事后排查），下次回滚覆盖式清理。

## 4. 任务拆分
- [ ] 写 [ADR-0053](../adr/0053-single-binary-self-replace.md)；ADR-0044/0045 + `adr/README.md` 加状态行/索引。
- [ ] `internal/update` 新增自替换：`swap_windows.go`/`swap_unix.go`（迁自 launcher）+ `selfreplace.go`（`SwapAndRespawn`/`CheckAndAutoRollback`/`respawn`/sentinel 读写）+ 单测。
- [ ] `cmd/beacon/main.go`：去 `exitcode` 依赖；`run()` 入口接 `CheckAndAutoRollback`；`select` 分支改自替换；删 `errRequestUpdateRestart` 70 路径。
- [ ] `internal/update/service.go`/`progress.go`：去「交还 launcher」注释、语义改自替换（落位链路不变）。
- [ ] 删 `cmd/beacon-launcher/`（6 文件）+ `internal/exitcode/`（2 文件）。
- [ ] 构建：`Makefile`/`Dockerfile`/`_build-release.yml` 去 launcher 产物。
- [ ] 文档同步：ARCHITECTURE §9/§10 进程模型段、OPERATIONS（§2.1 改写 + §1 新增 systemd/docker 推荐）、online-update.md 标过时、API.md update 端点措辞、PRD FR-119 翻状态、CHANGELOG 未发布段。

## 5. 验收标准
1. **单测**：rename 让位三步（Win 三步含回退 / Unix 覆盖）成功与失败回退；sentinel 读写；`CheckAndAutoRollback` 各分支（无 sentinel/未达阈值启动定时器/达阈值回滚/双路径确认删除）——构造确定性用例（注入 spawn/exit 钩子，仿 JianVideo `osExit`/可测分层）。
2. **真机（Win + Linux）**：真换版成功、报新版、`.old` 在验证期后被清理。
3. **真机自动回滚**：造一个「起不来的新版」（启动即 `os.Exit(1)`/panic 的二进制）作为 release 资产，验证换版后达阈值**自动回退 `.old`**、最终以旧版稳定运行（外部 docker/systemd 监督下）。
4. **失败兜底不回归**：下载/校验失败保留旧版、进程不退、状态 failed（FR-97 既有行为）。
5. 受影响组件全绿：`go build ./...` + `go test ./...`（含 e2e 若涉及）。

## 6. 风险 / 待定
- **换二进制失败但端口已释放**：`SwapAndRespawn` 在优雅关停后做 landBinary，若 landBinary 失败则就地回退并 spawn 旧版——短暂端口空窗由 agent fail-static 兜（与原 launcher 换失败等价）。
- **正常关停误删 sentinel**：已用「双路径确认」处理——管理员 SIGTERM 视为「新版被接受」，符合直觉（能被正常操作=起来了）。
- **裸跑无外部监督时自动回滚不触发**：用户已接受（崩溃自启交 docker/systemd）；OPERATIONS 写明推荐部署。
- **`maxStartAttempts=3`/验证期 `10s`/退出延时**：工程默认，集中常量定义、可调；不外部化配置（YAGNI）。
- **e2e 既有「触发→落位→重启→报新版」时序**：原基于 launcher 重启，需改为单进程自替换路径（若 e2e 覆盖此链路）。
