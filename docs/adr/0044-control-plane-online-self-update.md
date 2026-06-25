# ADR-0044：控制面在线自更新核心（按渠道查 Release → 下载 → SHA256 → 原子落位 → 退出码交还 launcher → 回滚）

**状态**：已接受

## 背景

Beacon 控制面是单二进制（Go + 内嵌前端，[ADR-0002](0002-go-react-embedded-stack.md)）。[ADR-0045](0045-builtin-launcher-supervisor.md) 已备好「不被替换的常驻 launcher + 退出码协议（`70`=请求更新重启）+ 跨平台换二进制」机制，但 launcher 极薄、本身不查 Release、不下载、不校验——「怎么拿到新二进制并安全落位」留给主进程侧实现。本 ADR 定主进程侧**更新核心**：从哪查、怎么下、怎么校验、怎么落位、怎么交还 launcher、失败怎么兜。

范围限定：**仅控制面单二进制**（含 `go:embed` 前端，前端随二进制整体替换，无需单独更新）。**agent jar 不在范围**（agent 自管身份、走自己的发布渠道）。HTTP 触发入口（端点）属 FR-99，本 ADR 只定可被调用的更新服务方法与「就绪→以 `70` 退出」这条接通。

## 决策

1. **按渠道查 Release**：渠道（`stable` / `rc`）作**入参**传入更新服务（不在本核心读 store——store 渠道项由 FR-101 加、由 FR-99 在后续批读后传入），查 GitHub Releases API（仓库 **wcpe/Beacon**，仓址做可配项默认此值）：`stable` 取最新非 prerelease release、`rc` 取最新 prerelease。与当前 `internal/version.Version` 比较，**仅当远端严格高于当前**才视为有可用更新。
2. **自写最小 semver 比较器**（不引第三方 semver 库）：解析 `vX.Y.Z` 与可选 `-rc.N` 预发布段，按主/次/补丁数字序比较，预发布版本低于同主次补丁的正式版（`1.2.0-rc.1 < 1.2.0`），`-rc.N` 间按 N 数字序。当前版本为 `dev` 哨兵（未经打包构建）→视为未知、不提示更新。
3. **下载资产**：按本进程 `runtime.GOOS`/`GOARCH` 选 release 资产 `beacon-<ver>-<os>-<arch>[.exe]`（资产集 **5 平台**：linux-amd64/arm64、windows-amd64、darwin-amd64/arm64，仅已发布平台可自更新；本平台无对应资产即失败）。下载到临时文件，**必有超时 + 大小上限 + 任何失败清理临时文件**（资源泄露禁令）。出站**经 `internal/httpx` 工厂**（[ADR-0047](0047-update-outbound-proxy-and-secret-redaction.md)）构造 client，支持代理入参，不裸用 `net/http`。
4. **SHA256 校验**：下载 release 的 `SHA256SUMS.txt`，比对所下载二进制实算 SHA256；不通过即中止、删临时文件、状态 `failed`、不替换、进程不退。
5. **原子落位**：校验通过的新二进制 `rename` 到 launcher 约定的 pending 路径（与运行二进制**同目录、同卷**，名为 `beacon.new[.exe]`，[ADR-0045](0045-builtin-launcher-supervisor.md) 约定），同卷 rename 原子。
6. **退出码交还 launcher**：落位 pending 成功后，更新服务经注入回调请求主进程以 `exitcode.RequestUpdateRestart`（`70`）退出（`cmd/beacon` 的 `run()` 返回既有哨兵 `errRequestUpdateRestart`，`main()` 既有 `errors.Is` 已映射到退出码 `70`）。launcher 据约定换二进制后重启。
7. **失败回滚**：任何阶段（查 Release / 下载 / 校验 / 落位）失败 → 保留旧二进制原样、**进程不退**、状态 `failed` 带原因（旧版继续服务）；「换了新版但起不来」的回退由 launcher 侧兜（[ADR-0045](0045-builtin-launcher-supervisor.md)）。
8. **进度内存态不建表**：更新进度（`idle`/`checking`/`downloading`/`verifying`/`staging`/`ready-restart`/`failed` + 百分比 + 目标版本 + 错误）是**进程内瞬态**（线程安全），随进程消失，**不建数据库表**（符合「瞬态走进程内存」真源切分，[ADR-0038](0038-ops-settings-store-hot-reload.md) 同理）。
9. **审计落库**：触发检查 / 应用 / 校验失败 / 落位完成各记一条 `audit_log`（新增 `system.update-check` / `system.update-apply` / `system.update-failed` action，`TargetType=system`，`detail` 含目标版本 / 结果摘要、**不含敏感**——代理凭据等经 [ADR-0047](0047-update-outbound-proxy-and-secret-redaction.md) 脱敏）。

> **渠道语义**（`rc` 渠道的预发布约定、版本命名）由 [ADR-0046](0046-rc-release-channel.md)（批 3，FR-103）权威定义，本 ADR **不重复**，只把渠道当入参消费。

## 理由

- **按渠道查官方 Release + SHA256 校验**：GitHub Release 是发布真源，SHA256SUMS 防下载篡改 / 截断，是最小可信更新链。
- **自写最小 semver 比较器**：只需 `vX.Y.Z[-rc.N]` 这一窄格式，引第三方 semver 库是过度依赖（YAGNI、依赖管理纪律）。
- **退出码交还 launcher 而非主进程自换**：Windows 运行中 exe 文件锁，主进程不能自换（[ADR-0045](0045-builtin-launcher-supervisor.md) 已论证），落位 pending + 退出码是唯一正解，复用既有协议、零新 IPC。
- **进度内存态不建表**：进度是瞬态运行信息、跨重启无意义（重启即换版完成），建表是无谓持久化；GitHub Release + 审计已是「发生过什么」的真源。
- **失败不退、保留旧版**：自更新绝不能把可用旧版搞挂——任何不确定都回退到「旧版继续 + 状态 failed」，可观测但不致命。

## 后果

- 新增 `internal/update/` 服务包（semver 比较 / 进度态 / Release 查询 / 下载校验落位编排），分层不碰 handler（HTTP 入口在 FR-99）。
- `cmd/beacon` 装配更新服务并把「就绪→以 `70` 退出」接通既有 `errRequestUpdateRestart` 出口（经 update→main 的信号通道）。
- `internal/model/enums.go` 新增 `system.update-*` action 常量与 `system` TargetType。
- 完整「真触发 → 换二进制 → launcher 重启 → 报新版」需 HTTP 触发（FR-99）+ Win/Linux 真机，本 FR 以 mock release server 服务级测试驱动「查→下载→校验→落位→请求重启」链路。

## 被否的备选

- **建 `update_history` 表记录每次更新**：GitHub Release（发生了哪些版本）+ `audit_log`（本控制面何时检查 / 应用 / 失败）已是真源，再建表是重复持久化、徒增 schema。否。
- **引第三方 semver 库**（如 Masterminds/semver）：仅需 `vX.Y.Z[-rc.N]` 窄格式比较，自写一个无副作用纯函数足够，引库违背 YAGNI 与依赖纪律。否。
- **主进程内自换二进制 + 自重启**：Windows exe 文件锁致自换必败（[ADR-0045](0045-builtin-launcher-supervisor.md) 已否），换文件必须由不被替换的 launcher 做。否。
