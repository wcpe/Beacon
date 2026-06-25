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
- FR-102 产物 / Docker ENTRYPOINT 适配后续补本规格。

---

# 更新核心（FR-97）

> 状态：开发中　·　关联 PRD：FR-97　·　决策见 [ADR-0044](../adr/0044-control-plane-online-self-update.md)

## 7. 需求（要什么）

主进程侧实现「在线更新核心」：按渠道查 GitHub Release 取最新版、与当前版本比对、下载本平台资产、SHA256 校验、原子落位 pending、以退出码 `70` 交还 launcher。仅控制面单二进制（含 `go:embed` 前端整体替换），agent jar 不在范围。HTTP 触发入口属 FR-99，本 FR 只提供可调用的更新服务方法 + 「就绪→以 70 退出」这条接通，以服务级测试驱动。

## 8. 设计（怎么做）

决策见 [ADR-0044](../adr/0044-control-plane-online-self-update.md)，此处只记落地结构。

- **包** `internal/update/`：
  - `semver.go`：自写最小 semver 比较器（`vX.Y.Z` + 可选 `-rc.N`），纯函数无副作用、不引第三方库；`dev` 哨兵视为未知。
  - `progress.go`：线程安全进度态（`idle`/`checking`/`downloading`/`verifying`/`staging`/`ready-restart`/`failed` + 百分比 + 目标版本 + 错误），进程内瞬态、不落库。
  - `github.go`：GitHub Releases API 客户端（按渠道取最新非 prerelease / prerelease release），出站经 `internal/httpx` 工厂（带代理 + 超时）。
  - `service.go`：编排 `CheckForUpdate`（查→比对，返回有无可用更新）与 `ApplyUpdate`（下载→校验→落位→请求重启）；审计经 `audit_log`。
- **渠道**：`stable` / `rc` 作入参（不读 store；store 渠道项 FR-101 加、FR-99 后续批传入）。
- **资产命名**：`beacon-<ver>-<os>-<arch>[.exe]`，5 平台 linux-amd64/arm64·windows-amd64·darwin-amd64/arm64；本平台按 `runtime.GOOS/GOARCH` 选，无对应资产即失败。
- **下载护栏**：超时（client 级）+ 大小上限（`io.LimitReader` + 实读字节校验）+ 任何失败 `os.Remove` 临时文件（资源泄露禁令）。
- **SHA256 校验**：下载 `SHA256SUMS.txt`，解析「`<hex>␣␣<filename>`」行取目标资产期望哈希，与实算二进制 SHA256 比对；不符即中止、删临时文件、状态 `failed`、不替换、进程不退。
- **原子落位**：校验通过的临时文件 `rename` 到 launcher 约定 pending 路径（运行二进制同目录 `beacon.new[.exe]`，[ADR-0045](../adr/0045-builtin-launcher-supervisor.md)）。pending 路径由 update 服务据运行二进制目录推导（与 launcher `resolvePaths` 同约定）。
- **退出码交接**：落位成功后经注入回调（`func()`）触发主进程关停信号，`cmd/beacon` 的 `run()` 返回 `errRequestUpdateRestart` 哨兵，`main()` 既有 `errors.Is` 映射到退出码 `70`。
- **失败回滚**：任何阶段失败 → 保留旧二进制、进程不退、状态 `failed` 带原因。
- **审计**：检查 / 应用 / 校验失败 / 落位完成各记一条（`system.update-check`/`system.update-apply`/`system.update-failed`，`TargetType=system`，detail 含目标版本 / 结果摘要、不含敏感）。

## 9. 验收标准（FR-97）

- `go build ./...` + `go test ./...` 全绿；`internal/update` 单测覆盖：semver 高 / 低 / 相等 / rc 序 / dev 哨兵；SHA256 通过 / 失败；落位原子；failed 各分支（无资产 / 下载超限 / 校验不符）；退出码交还（更新就绪 → run() 返回 `errRequestUpdateRestart`）。
- 以 `httptest` mock release server 跑「查 → 下载 → 校验 → 落位 → 请求重启」服务级链路（不引真网络）。
- 完整「真触发 → 换二进制 → launcher 重启 → 报新版」需 HTTP 触发（FR-99）+ Win/Linux 真机 → 待真机验。

## 10. 风险 / 待定（FR-97）

- HTTP 触发入口（端点）属 FR-99，本 FR 仅提供服务方法 + 退出接通；完整链路待 FR-99 + 真机。
- 经代理真连 GitHub 的真机维度待 FR-99 接入触发后验（FR-98 已铺底工厂与脱敏）。

---

# 更新 API（FR-99）

> 状态：开发中　·　关联 PRD：FR-99　·　沿用 [ADR-0044](../adr/0044-control-plane-online-self-update.md)（不另写 ADR）

## 11. 需求（要什么）

把 FR-97 的更新核心接到 `/admin/v1` HTTP 面，作 FR-100 前端消费的**一份端点契约真源**：

- `GET /system/update-check`（只读，full/readonly 皆可见）：返回当前版本 / 渠道 / 有无更新 / 可用版本 / release 日志 / 发布时间 / release URL / 检查时间 / 缓存到期。渠道从 store 读（`update.channel`，默认 stable）。**服务端内存缓存**（TTL=`update.check-interval-hours`），命中不打 GitHub；`?force=true` 绕缓存刷新。GitHub 不可达 / 限流 / 解析失败 → `status=check-failed`（非 5xx、不阻断页面）；`current=="dev"` → 不提示（`hasUpdate=false`、`isDevBuild=true`）。
- `GET /system/update`（状态）：读 FR-97 进度内存态（阶段 / 百分比 / 目标版本 / 错误），不查库。
- `POST /system/update`（触发）：调 FR-97 `ApplyUpdate`；写方法经既有 `readonlyWriteGuard`（readonly 403）+ 审计（复用 `system.update-apply`，纳入 `coveredWriteRoutes`）。
- **不做自动定时应用**（仅手动触发；自动检查开关 / 周期是 FR-101 的 store 项，前端据此轮询）。

## 12. 设计（怎么做）

分层 `router → handler → service`，handler 不构造 `http.Client`（出站经 FR-97 内部、已走 FR-98 工厂）、不读 store：

- **`internal/update`（核心，极小改动）**：`CheckResult` 补 `IsDevBuild`（`current=="dev"`）与 `PublishedAt`（`ghRelease` 增 `published_at` JSON 字段，原样透传、不参与比较）。核心不读 store、不持缓存（保持 FR-97 渠道作入参的约束）。
- **`internal/service/UpdateService`（编排 + 缓存）**：包装更新核心（窄接口 `updateCore`）+ 设置 store 窄读口（`updateSettingsReader`，读 `update.channel`/`update.proxy-url`/`update.check-interval-hours`）。`Check` 持进程内内存缓存（`sync.Mutex` + 注入时钟 `now` 便于单测）：非 force 且缓存未过期且渠道未变 → 回缓存；force / 过期 / 渠道变 → 重查并缓存结果（含 check-failed 也缓存，避免连环打不可达的 GitHub）。GitHub 错误在此降级为 `check-failed` 视图、**不向 handler 返错**（故端点恒 200）。`Status` 透传核心 `Snapshot`；`Apply` 读 store 渠道 / 代理后调核心。
- **`internal/handler/UpdateHandler`**：`Check`/`Status`/`Apply` 三方法，从 context 取 operator、`clientIP(r)` 取来源 IP，调 service；`Apply` 失败经 `render.WriteError`（核心返回的普通错误 → `500 INTERNAL`，原因已由核心审计 + 日志留痕）。
- **路由**：`/system/update-check`(GET) / `/system/update`(GET) / `/system/update`(POST) 无条件注册（与 System/Settings 一致，handler 仅请求期解引用）；POST 登记进 `coveredWriteRoutes`（核心自记专项审计，兜底中间件跳过避免双记）。
- **装配** `cmd/beacon/main.go`：`service.NewUpdateService(updateService, settingsService)` + `handler.NewUpdateHandler(...)`，经 `Handlers.Update` 注入路由。

## 13. 验收标准（FR-99）

- `go build ./...` + `go test ./...` 全绿。
- `internal/service` 单测覆盖：报有更新（按 store 渠道 / 代理透传）/ 缓存命中不重复打 GitHub / force 绕缓存 / TTL 过期重查 / 渠道变更失效缓存 / GitHub 不可达降级 check-failed（不报错）/ dev 不提示 / 状态读内存态 / 触发用 store 渠道 + 代理 / 核心错误上抛。
- `internal/update` 单测覆盖：`CheckForUpdate` 回填 `PublishedAt` 与 `IsDevBuild`。
- `internal/server` 守护：`POST /admin/v1/system/update` 在 `coveredWriteRoutes`（不与专项审计双记）；既有路由对账测试通过（无条件注册 + 覆盖集合双向相等）。
- readonly 403 与 GitHub 真连（经代理）维度 → 待真机验（同 FR-97 待验项）。

## 14. 风险 / 待定（FR-99）

- `POST /system/update` 的细分失败（无更新 / 本平台无资产 / 校验不符）统一回 `500 INTERNAL`（原因入审计 + 日志），未做逐项 apperr 映射（守范围、避免镀金）；前端据进度端点 `error` 与审计观察具体原因。
- 经代理真连 GitHub 的真机维度（检查 / 应用全链路、readonly 角色 403）待真机验。

---

# 前端提示与更新模态框（FR-100）

> 状态：开发中　·　关联 PRD：FR-100　·　纯前端、无新 ADR（消费 FR-99 三端点契约）

## 15. 需求（要什么）

把 FR-99 的检查 / 进度 / 触发端点接到管理台 UI：页眉版本徽章在有可用更新时叠小红点，点击弹更新模态框展示版本 / 渠道 / release 日志 / 外链并提供「立即检查 / 立即更新」，应用后展示进度并在重启后自动重连回显新版本。**只做 UI，更新执行机制由 FR-97/96 实现。**

## 16. 设计（怎么做）

- **独立低频检查 query** `web/src/hooks/useUpdateCheck.ts`：独立 `useQuery(['update-check'])`，**不复用 systemStatus 的 5s 健康轮询**（那是控制面健康高频心跳，高频检查会打爆 GitHub）；`refetchInterval` 取 store 的 `update.check-interval-hours`（默认 6h、下界 1h），`update.auto-check-enabled=false` 时禁用自动轮询、仅手动。设置值复用既有 `['settings']` 查询（react-query 同 key 去重）。`refresh()` 用 `queryClient.fetchQuery` 走 `?force=true` 绕服务端缓存、回填同一缓存（供模态框「立即检查」），自动轮询仍走非强制。`deriveAutoCheckEnabled` / `deriveIntervalMs` 为纯函数（缺项 / 越界兜默认 + 下界保护，单测穷举）。
- **版本徽章红点** `web/src/components/SystemHeader.tsx`：版本徽章改可点击按钮，`status=ok && hasUpdate && !isDevBuild` 时叠右上角红点（`role=status`）；`check-failed` / `isDevBuild` / 无更新均不叠。点击打开模态框。
- **更新模态框** `web/src/components/UpdateModal.tsx`：复用 `ui/dialog`。展示当前 / 可用版本、渠道、发布时间、release 日志、外链、「立即检查」「立即更新」。**release 日志安全渲染**：`releaseNotes` 作纯文本子节点交由 React 转义（`whitespace-pre-wrap` 保留换行），**禁止 `dangerouslySetInnerHTML` 注入原文**（防 XSS）。「立即更新」经 FR-76 `DestructiveConfirmDialog` 二次确认（提示控制面短暂重启 / 管理台不可用 / agent 按本地快照继续）→ `POST /system/update` → 启用 `['update-progress']` 轮询展示 phase/percent → 复用 FR-78 `useConnectionStatus` 识别「掉线→重连」边沿、重连成功回显新版本；触发失败回显 error。
- **设置聚合页接入** `web/src/pages/settings/VersionInfoTab.tsx`：FR-94 预留的系统信息块「版本与更新」子 tab 由占位改为本组件（当前版本 / 渠道 / 更新状态 + 同一更新模态框入口）。

## 17. 验收标准（FR-100）

- `cd web && pnpm test --run` 全绿 + `pnpm build` 通过。
- 单测覆盖：红点各分支显隐（true / false / check-failed / dev）、模态框字段渲染、releaseNotes 安全渲染（裸 HTML 不注入 DOM 节点）、「立即检查」走 force、「立即更新」二次确认 + POST + 进度轮询、自动检查开关 / 周期派生。
- 浏览器真机（构造高版本 release → 页眉小红点 → 模态框展示日志 → 点更新走重启重连回显新版本）→ 待真机验。

## 18. 风险 / 待定（FR-100）

- 重启窗口的「掉线→重连」边沿依赖 FR-78 `useConnectionStatus`（复用 system-status 5s 心跳派生）；重启亚秒窗口与重连后版本回显的真机时序待真机验。
- `releaseNotes` 当前按纯文本渲染（不解析 Markdown），如需富文本渲染须引入受信 sanitize 库（属后续增强、本期不做）。
