# 功能规格：控制面在线更新（launcher 监督部分）

> 状态：开发中　·　关联 PRD：FR-96（本批）/ FR-97 / FR-99 / FR-101 / FR-102（后续补）　·　分支：feature/fr-96-launcher

> 本规格覆盖「控制面在线更新」整体能力，**本批（FR-96）只写 launcher 监督进程部分**；更新核心（查 Release / 下载 / 校验 / 落位 pending，FR-97）、更新 API（FR-99）、出站代理（FR-98）、前端提示（FR-100）、设置（FR-101）、产物适配（FR-102）后续在本文追加对应小节。

## 1. 背景与目标

Beacon 控制面是单二进制，裸跑形态（FR-25 开箱即跑）下进程崩溃无人拉起，且后续在线自更新（FR-97）需要一个**不被替换的常驻进程**在主进程退出后换二进制再拉起。Windows 运行中 `.exe` 被文件锁，主进程无法自换文件——必须由独立监督进程做。

目标（FR-96，P2）：新增独立第二二进制 `beacon-launcher`，作常驻监督，使控制面无需外部 systemd / docker 即自动重启，并为 FR-97 备好「换二进制 + 重启」机制。

## 2. 需求（要什么）

- 新增 `cmd/beacon-launcher`，产出 `beacon-launcher[.exe]`，与主二进制同仓、版本经同一 `-ldflags -X internal/version.Version` 注入。
- launcher **极薄**：仅标准库 `os/exec` + `os`，不连 DB、不碰业务逻辑、不引第三方依赖。
- 以子进程方式启动同目录 `beacon[.exe]`：透传命令行参数（如 `-config`）与全部环境变量，继承 stdout/stderr/stdin。
- 按**退出码协议**决策（常量集中 `internal/exitcode`、两进程共享）：
  - `0` 正常退出 → launcher 跟随退出、不重启。
  - `1` 及一切非零（含信号退出 `128+signum`）= 崩溃 → 固定间隔（3s）重启 + 连续失败计数上限（5）超限即停打 ERROR。**不指数退避、不套 sysexits。**
  - `70`（请求更新重启）→ 用 pending 新二进制原子换后重启。
- 跨平台换二进制：Linux `rename` 覆盖；Windows 旧 exe rename 为 `.old` 让位 → pending（`beacon.new.exe`）rename 就位 → 删旧。失败 / pending 缺失 → 保留旧二进制、按旧版重启、打 WARN（回滚兜底）。
- 主进程 `cmd/beacon`：现有 `os.Exit(1)` 与正常返回纳入退出码协议常量，新增「请求更新重启」出口常量（本批仅备好出口，不实现更新逻辑）。SIGINT/SIGTERM 优雅关停段不变。
- 范围内：仅控制面、不涉 agent jar。
- 不做（范围外）：launcher 自更新（查 Release / 下载 / 校验，属 FR-97）；端口 socket fd 传递；Makefile / Dockerfile / release workflow 适配（属 FR-102）。

## 3. 设计（怎么做）

模型与决策见 [ADR-0045](../adr/0045-builtin-launcher-supervisor.md)，此处不重复决策正文，仅记落地结构。

- **退出码常量** `internal/exitcode/exitcode.go`：`OK=0` / `Crash=1` / `RequestUpdateRestart=70` + `IsCrashExit(code)` 判定（非 0 非 70 即崩溃，含信号退出）。主进程与 launcher 共享引用。
- **监督循环** `cmd/beacon-launcher/supervisor.go`：`supervisor` 把「跑子进程 / 换二进制 / 退避等待」抽成可注入函数，决策逻辑纯单测；`run()` 按协议循环决策，返回 launcher 自身退出码。
- **启子进程** `cmd/beacon-launcher/main.go`：`runChild` 用 `os/exec` 启动同目录 `beacon[.exe]`，透传 `os.Args[1:]` 与 `os.Environ()`，从 `*exec.ExitError` 取退出码；`resolvePaths` 据 launcher 自身目录推导 run / pending 路径。
- **跨平台换二进制**：`swap_unix.go`（`//go:build !windows`，rename 覆盖）/ `swap_windows.go`（`//go:build windows`，rename 让位三步 + 失败回滚），统一入口 `swapBinaryFiles(runPath, pendingPath)`。
- **主进程出口** `cmd/beacon/main.go`：`run()` 返回 `error`，`main()` 映射——`nil`→`OK`、`errRequestUpdateRestart` 哨兵→`RequestUpdateRestart`、其余→`Crash`。`errRequestUpdateRestart` 由 FR-97 在落位 pending 后返回，本批仅备好。
- **端口交接**：主进程 graceful shutdown 释放端口 → 退出 → launcher 重启新进程重新监听同端口；亚秒窗口由 agent fail-static 兜（见 ADR-0045 决策4）。

## 4. 任务拆分

- [x] `internal/exitcode` 退出码常量 + 判定 + 单测。
- [x] `cmd/beacon-launcher` 监督循环 + 启子进程 + 跨平台换二进制 + 单测。
- [x] `cmd/beacon/main.go` 退出码协议接线 + 请求更新重启出口常量。
- [x] 文档同步：PRD 状态、ADR-0045、本 spec、ARCHITECTURE §9、OPERATIONS §2、CHANGELOG。

## 5. 验收标准

- `go build ./...` + `go test ./...` 全绿；`internal/exitcode` 与 `cmd/beacon-launcher` 单测覆盖三类退出码决策、崩溃退避至上限、请求更新重启换二进制成功 / 失败回退、换文件成功 / pending 缺失回滚。
- Windows 实测：launcher 拉起 beacon 子进程、崩溃按固定间隔重启、达上限停、请求更新重启触发换二进制（rename 让位 → pending 就位 → 删旧）。
- Linux 换二进制路径（rename 覆盖）及完整 Win+Linux 真机重启链路 → 待真机验。

## 6. 风险 / 待定

- 端口先退后起的亚秒窗口：依赖 agent fail-static 兜底，真机需确认窗口内玩家进服不受影响（待真机验）。
- Linux `rename` 原子覆盖路径本机未实测（Windows 开发机），逻辑经临时目录单测覆盖，真机待验。
- FR-97 更新核心、FR-102 产物 / Docker ENTRYPOINT 适配后续补本规格。
