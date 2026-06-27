# Beacon 运维手册

> 面向部署与运维 Beacon 控制面。前置：docker-compose（beacon + mysql）。架构见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 1. 部署
- 复制 `.env.example` → `.env`，填 MySQL 密码与 `BEACON_BOOTSTRAP_TOKEN`。
- `docker compose up -d`；待 mysql healthcheck 通过后 beacon 自动建表（GORM AutoMigrate）+ 预置 namespace（prod/test）。
- 管理台与 API 同端口（默认 8848）。

### 1.1 单二进制免容器首启（FR-25）
直接跑 `beacon` 二进制时**首次启动自动脚手架、开箱即跑**：在当前目录释放 `config.yml`（默认 sqlite、零依赖可跑），**释放时把留空的 `auth.password` / `auth.secret` 就地填入随机强值（文件 0600）**，随即直接启动（sqlite 落 `beacon.db`），无需手工 `export` 或填值（`config.yml` 已存在则不覆盖）。**不再自动生成 `.env`**——凭据就在 `config.yml`，避免 `.env`（优先级更高）静默盖掉你对 `config.yml` 的改动。上手：
- 运行 `beacon` → 直接起服（控制台 WARN 提示已释放 `config.yml`）。
- 打开当前目录 `config.yml`，取 `auth.password` 登录管理台（`http://本机IP:8848`，用户名 `auth.username`，默认 `admin`）；按需改 `config.yml`（切 mysql 改 `database` 段、改口令 / 端口 / token 等）后重启即生效。
- **接 agent**：agent 的 `bootstrap-token` 用固定默认 `beacon-bootstrap-token` 即与控制面开箱匹配（仅防误连）；若改了控制面 `config.yml` 的 `agent-token`，各 agent 也要同步改。
- 如需经环境变量覆盖（如容器内、CI、临时改口令），真实环境变量与手动放置的 `.env` 仍生效，优先级 `真实 env > .env > config.yml`。
- 管理员口令 / 签名密钥强随机、不入库（[ADR-0009](adr/0009-control-plane-auth-pulled-forward.md)，非固定弱默认口令）；生产 MySQL 仍走上面的 compose 路径。

### 1.2 进程监督与崩溃自启（systemd / docker restart 推荐）
控制面是单进程，**进程崩溃自启交外部监督**（[ADR-0053](adr/0053-single-binary-self-replace.md)）——Beacon 自身不再带常驻监督进程。在线更新的「换版自替换 + 自动回滚」依赖外部监督在崩溃后重新拉起进程来累加重试计数、触发回退（见 [§2.1](#21-单进程自替换--自动回滚fr-119adr-0053)），故**生产部署务必启用以下任一监督**：
- **容器（推荐）**：`docker-compose.yml` 已配 `restart: unless-stopped`，beacon 容器崩溃即由 Docker 自动重启，无需额外配置。
- **裸跑（systemd）**：用 systemd 托管 `beacon`，关键是 `Restart=on-failure` + `RestartSec`，示例：
  ```ini
  [Unit]
  Description=Beacon 控制面
  After=network-online.target

  [Service]
  Type=simple
  WorkingDirectory=/opt/beacon
  ExecStart=/opt/beacon/beacon -config /opt/beacon/config.yml
  Restart=on-failure
  RestartSec=3s

  [Install]
  WantedBy=multi-user.target
  ```
  `systemctl daemon-reload && systemctl enable --now beacon` 后即托管；崩溃 3 秒后自动重启。
- **裸跑无任何监督**：进程崩溃即停、需手动启动；此时换版后「新版起不来」的自动回退要到下次手动启动才触发（可接受，但不建议生产如此部署）。

## 2. 升级
- **升级前先备份 MySQL**（见 §4）。
- 控制面：拉新镜像 → `docker compose up -d beacon`（mysql 数据卷不动）。AutoMigrate 只增不删；删列 / 改类型等复杂变更它不处理，需要时再引入迁移工具并另立 ADR。
- agent：替换 jar 重启子服。控制面与 agent 应同次发布、版本号一致（[ADR-0007](adr/0007-versioning-and-release-channels.md)）。
- **发布产物平台覆盖（FR-102）**：每个正式 tag 由 CI 在原生 runner 上 CGO=1 构建（非交叉编译，因 sqlite 经 go-sqlite3 需 CGO），覆盖 **5 个平台**——`linux-amd64`、`linux-arm64`（GitHub 原生 arm64 runner）、`windows-amd64`、`darwin-amd64`、`darwin-arm64`（明确不含 windows-arm64）。每个平台产出单一主二进制 `beacon-<ver>-<target>[.exe]`，并由 `SHA256SUMS.txt` 校验。双端 agent jar 与平台无关、各发布只构建一次。

### 2.2 滚动预发布渠道（FR-117，[ADR-0052](adr/0052-rolling-prerelease-channel.md)，取代 [ADR-0046](adr/0046-rc-prerelease-channel.md) 的 rc 模型）
渠道收敛为**正式版（stable）/ 滚动预发布（prerelease）两条**，预发布随 master 自动滚动刷新，便于试用最新并喂 in-app 在线更新使更新功能可测：

- **正式版触发（不变）**：打**无后缀**正式 tag `vX.Y.Z`（如 `git tag v0.17.0 && git push origin v0.17.0`），CI 的 `release.yml` 触发、`prerelease=false`，行为同前；tag↔VERSION 严格校验（tag 去 `v` == 根 `VERSION`）。
- **滚动预发布触发**：**推 master 即自动发布**。CI 的 `prerelease.yml` 由 master push 触发，调用同一套构建（5 平台原生矩阵单一主二进制 + 双端 jar + `SHA256SUMS.txt`），以**固定移动 tag `prerelease`** force-update **覆盖发布同一个** prerelease Release（`prerelease=true`，**只留最新一份、不堆 Release 列表**）；**版本号由 CI 自算 = 最新正式 release 的 minor+1**（patch 归零、与 `VERSION` 解耦、自动领先最新正式版，[ADR-0054](adr/0054-rolling-prerelease-version-ci-computed.md)；如最新正式 0.17.0 → 滚动预发布 0.18.0），不参与 tag↔VERSION 校验。release notes **从 `CHANGELOG.md` 的「## 未发布」段提取** + 前置中文警示头「⚠ 滚动预发布版本…勿用于生产」。
- **渠道判定（按版本号）**：控制面在线自更新（FR-99/FR-100）的「正式（stable）」渠道按 GitHub API 最新**非 prerelease** release 判定，「预发布（prerelease）」渠道按最新 **prerelease** release 判定；**是否有更新按语义版本号 `X.Y.Z` 比较**——渠道版 > 当前运行版才提示，**同 `X.Y.Z` 滚动覆盖不提示**（你已在最新，重拉 / 重启即可），**跨号才提示**。滚动预发布的版本号取 Release 标题（CI 自算的 `v<X.(Y+1).0>`，[ADR-0054](adr/0054-rolling-prerelease-version-ci-computed.md)），因其 tag 为移动标签 `prerelease` 非语义版本。
- **用途**：给运维 / 试用方装预发布产物先验，并让 in-app 更新 / 切渠道功能有真实 Release 可检可更可测；确认无误后打无后缀正式 tag 发版。
- **回退**：预发布仅供试用、不进生产；发现问题直接弃用、修后随下次 master push 自动覆盖刷新即可，无需特殊回滚。生产升级一律以**正式 Release** 为准。
- **注意**：全局技能 `sdd-publish-snapshot` 基于旧快照 / rc 模型，与本节滚动预发布模型方向趋同但仍属全局插件；本仓库预发布以本节流程（ADR-0052）为准。

### 2.1 单进程自替换 + 自动回滚（FR-119，[ADR-0053](adr/0053-single-binary-self-replace.md)）
控制面是**单一 `beacon[.exe]`**——在线更新由主进程在自身进程内完成自我替换，无独立监督进程、无退出码交接。换版机制：
- **自替换换版**：在线更新（FR-97）下载校验落位 `beacon.new[.exe]`（运行二进制同目录同卷）后，主进程**优雅关停释放端口** → `rename` 让位三步（`beacon`→`beacon.old`、`beacon.new`→`beacon`）→ spawn 新进程（继承命令行参数 / 环境变量 / 工作目录 / 标准流）→ 旧进程正常退出。Windows 同样允许 rename 运行中的 exe（重命名只改目录项、已打开句柄仍指向原映像），故让位可行；换二进制失败则就地回退、以旧版重启兜底。其间有**亚秒级**端口不可用窗口，agent 按本地快照继续（fail-static），玩家进服不受影响。
- **自动回滚（崩溃循环闭合）**：换二进制成功后写 sentinel 标记（运行二进制同目录小文件，记崩溃计数 `attempt` + 目标版本）。新版**启动早期**（HTTP 起之前）自检——稳定运行**过验证期 10 秒**或收到正常关停信号（如 `Ctrl+C` / `docker stop`，视为新版已起来被操作）即判定成功，删 sentinel + 删 `.old` 清理备份；若换版后**反复起不来**（崩溃计数达阈值，默认 3）则在启动早期自动 rename 回退 `.old` 并重启旧版，最终以旧版稳定运行——**不依赖任何外部进程**。坏新版归档为 `beacon.failed[.exe]` 便于事后排查。
- **崩溃自启交外部监督**：进程崩溃由 docker / systemd 拉起（部署见 [§1.2](#12-进程监督与崩溃自启systemd--docker-restart-推荐)），自动回滚依赖此重启逐次累加 `attempt` 至阈值后回退。**裸跑 `beacon` 无外部监督时**新版崩溃即停（可接受），但仍可靠下次手动启动触发自检回退。
- **容器形态**：Docker 镜像 `ENTRYPOINT` 为 `beacon`，崩溃自启靠 compose `restart` 策略。**容器内在线更新换二进制仅临时有效**——镜像不可变，容器一旦重建即丢更新；**容器形态的生产升级一律以重拉镜像为准**（拉新镜像 → `docker compose up -d beacon`，见 §2 上文），不要依赖容器内自更新。

## 3. 健康与观测
- 健康探针：`GET /admin/v1/namespaces`（只读、无副作用）。
- 日志：beacon 容器内中文分级日志（ERROR/WARN/INFO/DEBUG）。
- 重点关注：实例失联告警、重复 serverId 告警、配置漂移告警。
- 健康分级与告警（FR-28）：实例按心跳陈旧度推进 `online → degraded → lost → offline`（阈值见 `config.yml` 的 `health.degraded-after-sec`/`ttl-sec`/`offline-grace-sec`，须满足 degraded < ttl < offline，错序启动即报错）。进入异常态（degraded/lost/offline）主动告警，恢复 online 不告警。告警通道：**站内信**（`GET /admin/v1/alerts` 读最近 N 条，N=`alert.inbox-capacity`，进程内、控制面重启清零）+ **webhook**（配置 `alert.webhook.url` 后向其 POST 告警 JSON，留空则仅站内信）。

### 3.1 SSE 推送流经反向代理 / Docker（FR-24，[ADR-0015](adr/0015-sse-server-push-transport.md)）
agent↔控制面用单条 SSE 流 `GET /beacon/v1/agent/stream` 做 server→agent 推送。若在 beacon 前放反向代理（nginx 等），须保证流不被缓冲、不被空闲超时切断：
- **关闭响应缓冲**：beacon 已对该响应输出 `X-Accel-Buffering: no`（nginx 据此关 proxy buffering）；其它代理请按等价方式关闭对 `text/event-stream` 的缓冲。nginx 还需 `proxy_http_version 1.1;` + `proxy_set_header Connection "";`。
- **调长读超时**：把代理对 agent stream 路径的读超时（nginx `proxy_read_timeout`）调到显著大于 beacon 的保活间隔（默认取长轮询挂起上限），避免空闲被误判断流。beacon 无变更时按间隔发 SSE 注释行（`: ping`）保活。
- **Docker 网络**：沿用现有"agent 能直连 beacon 地址"的可达约束，无新增端口；SSE 走与 API 同一端口（默认 8848）。
- **断流不影响判活**：健康 online/lost/offline 仍由独立心跳 + TTL 决定，SSE 抖动断流不会误判失联；agent 流断按本地快照继续、自动退避重连并对账（fail-static）。

## 4. MySQL 备份与恢复（关键）
> MySQL 是**配置权威库**——丢了等于全集群配置全没。务必定期备份。
- 备份：`docker exec beacon-mysql mysqldump -u root -p<密码> beacon > beacon-$(date +%F).sql`
- 恢复：`docker exec -i beacon-mysql mysql -u root -p<密码> beacon < beacon-backup.sql`
- 数据卷 `beacon-mysql-data` 持久化；迁移机器时连卷一起搬。
- **常态化**：建议 cron 每日 dump + 保留近 N 天 + 异机各存一份（别与 MySQL 同机）。
- **恢复演练**：上线前至少完整演练一次恢复（导出 → 空库导入 → 起 beacon 校验配置仍在），确认备份真能用。

## 5. 回滚
- **控制面版本回滚**：部署上一个稳定镜像 tag（见 GitHub Releases）。
- **业务配置回滚**：用管理台的配置版本回滚——这是 Beacon 自带能力，**不需重新部署**。
- **代码层回滚**：见 `sdd-rollback-change` 技能。

## 6. 排障
- beacon 起不来：看日志是否连不上 MySQL（DSN / 网络 / healthcheck 未过）。
- agent 连不上：核对控制面地址、`X-Beacon-Token`、网络连通。
- 配置不热更：看 agent 长轮询是否在连、控制面是否唤醒了受影响集合、有效配置 md5 是否真变。
- **控制面短暂不可用时不要重启子服**：agent 会按本地快照 fail-static 继续，控制面恢复后自动重连。

## 7. 端到端验收（agent 真机接入联调）
用 `agent/` 下的验收模块在真机 Bukkit/Bungee 上自检「首次接入 + 发布热更 + 审计可查」，全程由 gradle（jpenilla run-task 的 run-paper/run-waterfall 插件）自动下载并运行服务端，无需手工准备 MC 服。

> **本地前置（工具链）**：下面的手动联调与 §7.1–7.3 的 Go E2E 都需在本机构建控制面二进制并经 gradle 起真机服务端，跑前先就位：
> - **JDK21**：跑 gradle 与 MC 服务端（Paper 1.20.4 / Waterfall 1.20 需 Java 17+）。Windows 上若 `JAVA_HOME` 路径含 `!` 等特殊字符，`gradlew.bat` 可能回退到 PATH 上的旧 JDK；E2E 经 harness 调 `./agent/gradlew` 继承环境，跑前把 `JAVA_HOME` 显式指向干净路径的 JDK21。
> - **C 编译器（CGO）**：控制面默认 sqlite 驱动为 `mattn/go-sqlite3`（CGO），需 `CGO_ENABLED=1` 且 PATH 上有 gcc/clang，否则 `go build ./cmd/beacon` 编不出（即便走 `E2E_DB_DRIVER=mysql` 也一样——编译期已静态 import sqlite 驱动）。
> - **已构建前端**：控制面 `go:embed web/dist`，跑前先 `make web`（或 `cd web && pnpm build`），否则报 `pattern all:web/dist: no matching files found`。

- 先起控制面：`docker compose up -d`（或本地 `go run ./cmd/beacon`），确保 `GET /admin/v1/namespaces` 可达。
- 经 REST/管理台建一条全局配置（如 dataId `beacon-e2e.yml`）。
- Bukkit 端：`cd agent && ./gradlew :agent-e2e:runServer` —— run-paper 自动下载 Paper、加载 BeaconAgent 与验收插件，agent 注册→拉配置→apply。
- Bungee 端：`./gradlew :agent-e2e-bungee:runBungee` —— run-waterfall 自动下载 Waterfall，加载 BeaconAgentProxy 与验收插件。
- 验证：`GET /admin/v1/instances` 看 serverId online；改配置发布后看验收插件数据目录的 `e2e-observations.log` 是否出现新值（业务插件经 agent Java 只读 API 读到热更）；`GET /admin/v1/audits` 查发布记录。

### 7.1 FR-15 三方覆盖 + 受限重载命令真机 E2E（RCE 面，启用命令白名单前必跑）

校验「三方插件文件覆盖 + 受限重载命令」整链与 [ADR-0011](adr/0011-third-party-file-override-and-restricted-reload-command.md) 安全不变量在真机成立。验收插件 `BeaconE2E` 兼作被覆盖目标：种原文件 `managed.yml`、注册受限重载命令 `beacone2ereload`、轮询观测文件变更与命令收到（记到 `e2e-override-observations.log`）。

入口为纯 Go 测试、**真跨平台**（Windows/Linux/macOS 一致），由测试自管控制面 + 真 Paper 生命周期，逐相位收口、无悬挂进程；CI 亦可跑（见 `.github/workflows/e2e.yml`）。

前置：本机有 Go / JDK21 + 联网（首跑下载 Paper，约 12 分钟）。**默认 sqlite、无需 docker/MySQL**；如需切 MySQL，另起一次性库并经 `E2E_DB_DRIVER=mysql` + `E2E_DB_DSN` 指向它。

必填环境变量：

- `E2E_ADMIN_PASS`：管理员口令。
- `E2E_AUTH_SECRET`：令牌签名密钥。

可选环境变量：

- `E2E_DB_DRIVER`：数据库驱动，`sqlite`（默认）或 `mysql`。
- `E2E_DB_DSN`：`E2E_DB_DRIVER=mysql` 时必填，指向测试 MySQL。
- `E2E_BEACON_URL`：控制面地址，默认 `http://localhost:8848`。

运行（PowerShell；Bash 把赋值换成 `export` 即可，命令同）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./test/e2e/override
```

测试依次跑四相位（任一 FAIL 即测试失败）：

- **inert（空白名单）**：覆盖集发布后文件被覆盖为新内容、但受限重载命令**一条不派发**（ADR-0011 默认 inert）。
- **filetree（FR-14）**：发布一个文件树文件 → agent 镜像落盘到插件真实数据目录 → 验收插件读到镜像内容。
- **ordering（放行白名单）**：验「备份原文件 → 原子覆盖 → 落盘成功后才派发命令」次序（命令收到时磁盘已是覆盖后内容），再回滚到无命令版本验「只还原事实、不重放命令」。
- **failstatic**：杀控制面后受管文件不动、命令不发。

> 前端（FR-18）管理台可在控制面起着时人工 / 浏览器自检：`http://localhost:8848` 登录（admin + `BEACON_ADMIN_PASSWORD`）→「文件树托管」看托管文件 →「文件覆盖集」详情看**发布前 dry-run 只读预览**（将覆盖哪些文件 / 执行什么命令 + 二次确认勾选门控发布）。

成员挂载当前无 admin API，驱动经数据层写 `file_object`（`override_set_id>0`）造成员——属已知缺口的临时绕过（见 CHANGELOG 已知项）。

### 7.2 Proxy 目录注入真机 E2E（FR-4 服务发现延伸出口）

校验「在线 `role=bukkit` 子服按 `serverId` 注入 Bungee 目录」在真机成立。控制面用 **SQLite 开发模式**（无需 Docker/MySQL）。`agent-e2e-bungee` 的 `DirectoryE2EProbe` 周期把 Bungee `ServerInfo` 目录与 `beacon` 命令注册状态覆写到 `plugins/BeaconE2EProxy/e2e-directory-latest.txt`，供 Go 驱动断言。

入口为纯 Go 测试、**真跨平台**，由测试自管控制面 + 真 Paper 子服 + 真 Waterfall 代理生命周期，逐相位收口；最适合 CI（见 `.github/workflows/e2e.yml`）。

前置：本机有 Go / JDK21 + 联网（首跑下载 Paper/Waterfall）。**默认 sqlite、无需 docker/MySQL**。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_DB_DRIVER`（默认 `sqlite`）、`E2E_DB_DSN`（driver=mysql 时）、`E2E_BEACON_URL`（默认 `http://localhost:8848`）。运行（PowerShell；Bash 把赋值换成 `export` 即可）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./test/e2e/directory
```

测试依次跑两相位（任一 FAIL 即测试失败）：

- **directory**：在线 `role=bukkit` 子服按 `serverId` 注入 Bungee 目录（地址含子服端口）、手工服务器（Waterfall 默认 `lobby`）保留不被覆盖、`beacon` 命令已注册。
- **failstatic**：杀控制面后已注入目录与手工服**不被清空**（fail-static）。

### 7.3 FR-32 可观测看板真机 E2E（指标上报 → 采样落库 → 端点返真值）

纯 Go e2e，自起控制面（SQLite，经 `BEACON_METRIC_SAMPLE_INTERVAL_SEC` 调小采样间隔）+ 真 Paper + BeaconAgent，验证「agent 上报真 JVM 负载 → 采样器落 `metric_sample` → `/admin/v1/metrics/summary` 与 `/trend` 返真值 → 边界无玩家名单」（[ADR-0023](adr/0023-control-plane-observability-dashboard.md)）。

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./test/e2e/metrics
```

依次断言四相位（任一 FAIL 即失败）：summary 含目标子服且 `avgMemMax>0`（真 JVM 堆）；trend 时间序列非空且字段为真值；persist 经 GORM 直读 `metric_sample` 已落样本；boundary 响应不含玩家名单 / 身份字段。

## 8. 测试运行方式（单元 / 集成）

- **单元测试**（无外部依赖、快）：`go test ./...`。集成用例带 `//go:build integration` 标记、默认**不编译**，故此命令只跑纯逻辑单测——`internal/service` / `internal/server` 显示 `no test files` 属正常（其用例全为集成）。
- **集成测试**（需真实 MySQL）：先起测试库、设 DSN，再带 `integration` 标记跑：
  ```bash
  export BEACON_TEST_DSN='root:<密码>@tcp(127.0.0.1:3306)/beacon?charset=utf8mb4&parseTime=true&loc=UTC'
  go test -tags=integration ./... -count=1
  ```
  `internal/testsupport` 会在该实例上按 `beacon_<suffix>` 建独立测试库（不污染基础库）；未设 `BEACON_TEST_DSN` 时集成用例 `t.Skip`。FR-32 的 `metric_sample` 仓库与 `/admin/v1/metrics/*` 端点集成亦在此 `-tags=integration` 套内。
- **agent 侧集成测试**（需真实 Redis）：agent-adapters 对真实 Redis 的集成用例（含 FR-31 名册 `HGETALL` 全表读）默认连 `localhost:16379` 无密码，连不上即 `assumeTrue` 跳过。先起 Redis、再跑：
  ```bash
  # 默认 16379，可经 BEACON_REDIS_TEST_HOST / BEACON_REDIS_TEST_PORT / BEACON_REDIS_TEST_PASSWORD 覆盖
  cd agent && ./gradlew :agent-adapters:cleanTest :agent-adapters:test --tests '*RedisMessageTransportIntegrationTest'
  ```
  绿不等于真跑——须确认 `agent/agent-adapters/build/test-results/test` 报告里该类 `skipped=0`（跳过即 Redis 没连上）。
- **CI / 发版前**：单测 + MySQL 集成 + agent Redis 集成都跑，E2E 另见 §7（跨平台 `go test -tags=e2e`，CI 见 `.github/workflows/e2e.yml`）。务必确认集成是 PASS 而非 SKIP。
- **前端单元测试**（vitest + React Testing Library，jsdom 环境、无外部依赖、不连后端）：`cd web && pnpm test`（监听模式 `pnpm test:watch`）。测试文件经 `tsconfig` 排除出生产 `tsc -b`，与 `make web` 的 `go:embed` 构建解耦。
