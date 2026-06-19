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

## 2. 升级
- **升级前先备份 MySQL**（见 §4）。
- 控制面：拉新镜像 → `docker compose up -d beacon`（mysql 数据卷不动）。AutoMigrate 只增不删；删列 / 改类型等复杂变更它不处理，需要时再引入迁移工具并另立 ADR。
- agent：替换 jar 重启子服。控制面与 agent 应同次发布、版本号一致（[ADR-0007](adr/0007-versioning-and-release-channels.md)）。

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
用 `agent/` 下的验收模块在真机 Bukkit/Bungee 上自检「首次接入 + 发布热更 + 审计可查」，全程由 gradle（TabooLib runServer）自动下载并运行服务端，无需手工准备 MC 服。

- 先起控制面：`docker compose up -d`（或本地 `go run ./cmd/beacon`），确保 `GET /admin/v1/namespaces` 可达。
- 经 REST/管理台建一条全局配置（如 dataId `beacon-e2e.yml`）。
- Bukkit 端：`cd agent && ./gradlew :agent-e2e:runServer` —— 自动下载 Paper、加载 BeaconAgent 与验收插件，agent 注册→拉配置→apply。
- Bungee 端：`./gradlew :agent-e2e-bungee:runBungee` —— 自动下载 Waterfall，加载 BeaconAgentProxy 与验收插件。
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

## 8. 测试运行方式（单元 / 集成）

- **单元测试**（无外部依赖、快）：`go test ./...`。集成用例带 `//go:build integration` 标记、默认**不编译**，故此命令只跑纯逻辑单测——`internal/service` / `internal/server` 显示 `no test files` 属正常（其用例全为集成）。
- **集成测试**（需真实 MySQL）：先起测试库、设 DSN，再带 `integration` 标记跑：
  ```bash
  export BEACON_TEST_DSN='root:<密码>@tcp(127.0.0.1:3306)/beacon?charset=utf8mb4&parseTime=true&loc=UTC'
  go test -tags=integration ./... -count=1
  ```
  `internal/testsupport` 会在该实例上按 `beacon_<suffix>` 建独立测试库（不污染基础库）；未设 `BEACON_TEST_DSN` 时集成用例 `t.Skip`。
- **CI / 发版前**：两条都跑（`go test ./...` 与 `go test -tags=integration ./...`），E2E 另见 §7（跨平台 `go test -tags=e2e`，CI 见 `.github/workflows/e2e.yml`）。务必确认集成是 PASS 而非 SKIP。
- **前端单元测试**（vitest + React Testing Library，jsdom 环境、无外部依赖、不连后端）：`cd web && pnpm test`（监听模式 `pnpm test:watch`）。测试文件经 `tsconfig` 排除出生产 `tsc -b`，与 `make web` 的 `go:embed` 构建解耦。
