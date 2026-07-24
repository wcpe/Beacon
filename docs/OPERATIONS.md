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

## 2. 升级与发布
- **升级前先备份 MySQL 与 Agent 本地状态**（见 §4）。
- 控制面：拉取已核验的 GA 产品资产，再重启服务；数据库迁移只允许 expand / 可重入回填，回滚不依赖 down migration。
- agent：按批替换 Bukkit/Bungee JAR 并重启节点，保留 `plugins/Beacon/` 本地身份、配置快照、流位置与幂等账本。控制面与 agent 的产品版本必须一致。
- 产品资产发布前使用 `SHA256SUMS.txt` 校验；平台是否发布由本次 RC 的实际资产决定，不额外引入阶段专属平台准入。

### 2.1 开发构建、发布准备与 RC/GA 流程（FR-182，[ADR-0074](adr/0074-simple-rc-ga-release-flow.md)）

- **PR 只过质量门**：pull request 运行现有质量任务，不执行 `make package`，也不上传可下载产品包。
- **`master` 临时开发构建**：质量任务全部成功后，CI 才运行现有 `make package` 并上传短期 Actions Artifact。该 Artifact 仅用于开发验证，不能直接晋级为 RC 或 GA。
- **发布准备**：发布准备只更新根 `VERSION` 与 `CHANGELOG.md`，版本由根 `VERSION` 唯一确定。
- **RC**：按目标版本创建 `vX.Y.Z-rc.N`，固定一个 commit 和一次构建出的产品资产；候选发布后不可移动、覆盖或补传。`release-verify-rc` 必须从仓库中的真实 RC tag 解析 peeled commit，并与 workflow 锁定的 40 位提交身份一致，手工传入任意 SHA 不能替代真实 tag。
- **GA**：最终 RC 与 GA 必须指向同一 commit。GA 先把最终 RC 资产原样复制到独立目录，再以 RC 下载目录为不可变基准运行 `release-verify-ga`；创建前与公开回拉后都逐项比较名称、字节大小和 SHA-256。GA 不重新编译、打包、重建、重签或替换资产。
- **在线更新**：只自动消费严格 `vX.Y.Z` 的 GA，RC 和开发 Artifact 必须显式安装。

通用校验入口：

```sh
# 只校验 VERSION、RC/GA tag 格式和 GA workflow；不要求 tag 已存在。
make release-check RELEASE_RC_TAG=v1.0.0-rc.1

# RC tag 必须真实存在，且 peeled commit 必须与锁定身份一致。
make release-verify-rc \
  RELEASE_RC_TAG=v1.0.0-rc.1 \
  RELEASE_ASSETS_DIR=rc-assets \
  RELEASE_RC_COMMIT=<RC_COMMIT>

# RC/GA tag 均须真实存在；GA 目录必须与独立 RC 基准目录逐项一致。
make release-verify-ga \
  RELEASE_RC_TAG=v1.0.0-rc.1 \
  RELEASE_RC_ASSETS_DIR=rc-assets \
  RELEASE_ASSETS_DIR=ga-assets \
  RELEASE_RC_COMMIT=<RC_COMMIT> \
  RELEASE_GA_COMMIT=<GA_COMMIT>
```

两个资产目录都必须是封闭集合：除 `SHA256SUMS.txt` 外只能包含当前版本规定的产品文件，清单必须覆盖每个产品文件且不得校验自身。随后 GA 校验会把 `SHA256SUMS.txt` 本身也作为 RC/GA 对比项，核对其文件名、字节大小和 SHA-256；因此不能通过同时修改 GA 产品文件及其自带清单来制造一套彼此自洽但已偏离 RC 的资产。

### 2.2 单进程自替换 + 自动回滚（FR-119，[ADR-0053](adr/0053-single-binary-self-replace.md)）
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
- **RC 回滚**：未晋级候选直接停止使用；不得覆盖原 RC 资产。修复后从新 commit 创建 `vX.Y.Z-rc.(N+1)`，重新构建并校验完整资产。
- **GA 运行回滚**：使用上一版已核验的 GA 产品资产和升级前备份。若 schema 不兼容，必须先停写并恢复备份，禁止执行未经验证的 down migration。
- **产品资产**：已发布版本的文件不得覆盖；发现缺陷必须发布新的 RC/GA 版本。
- **业务配置回滚**：使用管理台配置版本回滚，不需重新部署。
- **代码层回滚**：见 `sdd-rollback-change` 技能。

## 6. 排障
- beacon 起不来：看日志是否连不上 MySQL（DSN / 网络 / healthcheck 未过）。
- agent 连不上：核对控制面地址、`X-Beacon-Token`、网络连通。
- 配置不热更：看 agent 长轮询是否在连、控制面是否唤醒了受影响集合、有效配置 md5 是否真变。
- **控制面短暂不可用时不要重启子服**：agent 会按本地快照 fail-static 继续，控制面恢复后自动重连。

## 7. 端到端验收（agent 真机接入联调）
用 `apps/agent/` 下的验收模块在真机 Bukkit/Bungee 上自检「首次接入 + 发布热更 + 审计可查」。Gradle 统一使用 `mc-testkit 0.5.0` 自动下载并编排 Paper 1.20.4 与原生 BungeeCord，无需手工准备 MC 服，也不再使用 jpenilla run-task 或 Waterfall 代验。

> **本地前置（工具链）**：下面的手动联调与 §7.1–7.3 的 Go E2E 都需在本机构建控制面二进制并经 Gradle 起真机服务端，跑前先就位：
> - **JDK21**：运行 Gradle、Paper 与 BungeeCord。Windows 上若 `JAVA_HOME` 路径含 `!` 等特殊字符，`gradlew.bat` 可能回退到 PATH 上的旧 JDK；E2E 经 harness 调 `./apps/agent/gradlew` 继承环境，跑前把 `JAVA_HOME` 显式指向干净路径的 JDK21。
> - **C 编译器（CGO）**：控制面默认 sqlite 驱动为 `mattn/go-sqlite3`（CGO），需 `CGO_ENABLED=1` 且 PATH 上有 gcc/clang，否则 `go build ./apps/server/cmd/beacon` 编不出（即便走 `E2E_DB_DRIVER=mysql` 也一样——编译期已静态 import sqlite 驱动）。
> - **已构建前端**：控制面 `go:embed apps/web/dist`，跑前先 `make web`（或根目录 `pnpm --filter @beacon/web build`），否则只会内嵌占位目录。
> - **mc-testkit 0.5.0**：默认精确从 `maven.wcpe.top` 解析正式版；不设置 `MC_TESTKIT_INCLUDE_BUILD` 即使用 Maven 工件。仅在开发 mc-testkit 插件本身、需要联调其未发布源码改动时，才可选设置该变量指向本地 mc-testkit 源码目录。

`agent-e2e` 统一声明三个入口：

- `:agent-e2e:servePaper`：一个 Paper 后端，注入 `BeaconAgent` 与 `BeaconE2E`。
- `:agent-e2e:serveDirectory`：同一生命周期内启动 Paper 后端 + 原生 BungeeCord，双端分别注入 Agent 与探针；BungeeCord 静态路由名固定为 `backend`。
- `:agent-e2e:serveProxy`：为代理连接探针启动原生 BungeeCord；mc-testkit 同时启动仅用于路由就绪的伴随 Paper 后端。

Go harness 通过环境变量向节点传递 endpoint、bootstrap token、namespace、serverId/address、命令白名单与探针开关；动态凭据不得改放 Gradle `-P` 参数。手动执行时使用 `cd apps/agent && ./gradlew :agent-e2e:<任务名> --no-daemon`，Windows 对应 `gradlew.bat`，并自行提供与 harness 相同的 `BEACON_AGENT_*` 环境变量。

运行证据统一位于：Paper 运行目录 `apps/agent/agent-e2e/build/mc-testkit/run/`、BungeeCord 运行目录 `apps/agent/agent-e2e/build/mc-testkit/run-proxy/`、编排结果与 pid `apps/agent/agent-e2e/build/mc-testkit/results/`、Go harness stdout/stderr `.tmp/*.out.log` 与 `.tmp/*.err.log`。E2E workflow 无论成功、失败或取消都会先扫描完全相同的归档候选路径，检查本轮唯一哨兵、管理员口令、签名密钥、数据库 DSN、bootstrap token、PostgreSQL 密码及全部动态 access token。固定口令和签名密钥在写入 `GITHUB_ENV` 前先注册 Actions 掩码；动态 access token 由 harness 在创建响应返回给测试前先原子写入共享指纹状态文件，记录格式仅为 `<SHA-256> <明文字节长度>`，并在对应 E2E 的私有生成状态文件追加一条 `generated` 记录，两个状态文件在非 Windows 上都必须为 `0600`。只有指纹持久化成功后 harness 才输出 `::add-mask::` 注册日志掩码；不得把动态 token 明文追加到 `GITHUB_ENV` 或另写明文 token 文件。后续独立的归档扫描步骤先校验启动标记、生成记录数、指纹数量、格式、去重和文件权限，再按记录的字节长度滑窗计算 SHA-256 检查候选文件；状态缺失或损坏时 fail closed。扫描只报告命中文件相对路径和敏感键名或“动态凭据指纹”，不回显原值。仅扫描成功才上传日志和结果；命中泄漏时 job 失败且禁止上传含密文件。

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
go test -tags=e2e -timeout=30m ./apps/server/test/e2e/override
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

入口为纯 Go 测试、**真跨平台**，由测试自管控制面，并通过单个 `serveDirectory` 生命周期启动真 Paper 子服 + 原生 BungeeCord 代理，逐相位收口；最适合 CI（见 `.github/workflows/e2e.yml`）。

前置：本机有 Go / JDK21 + 联网（首跑下载 Paper/BungeeCord）。**默认 sqlite、无需 docker/MySQL**。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_DB_DRIVER`（默认 `sqlite`）、`E2E_DB_DSN`（driver=mysql 时）、`E2E_BEACON_URL`（默认 `http://localhost:8848`）。运行（PowerShell；Bash 把赋值换成 `export` 即可）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./apps/server/test/e2e/directory
```

测试依次跑两相位（任一 FAIL 即测试失败）：

- **directory**：在线 `role=bukkit` 子服按 `serverId` 注入 Bungee 目录（地址含子服端口）、mc-testkit 固定静态路由 `backend` 保留不被覆盖、运行时实现标识精确为 `BungeeCord`、`beacon` 命令已注册。
- **failstatic**：杀控制面后已注入目录与手工服**不被清空**（fail-static）。

### 7.3 FR-32 可观测看板真机 E2E（指标上报 → 采样落库 → 端点返真值）

纯 Go e2e，自起控制面（SQLite，经 `BEACON_METRIC_SAMPLE_INTERVAL_SEC` 调小采样间隔）+ 真 Paper + BeaconAgent，验证「agent 上报真 JVM 负载 → 采样器落 `metric_sample` → `/admin/v1/metrics/summary` 与 `/trend` 返真值 → 边界无玩家名单」（[ADR-0023](adr/0023-control-plane-observability-dashboard.md)）。

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./apps/server/test/e2e/metrics
```

依次断言四相位（任一 FAIL 即失败）：summary 含目标子服且 `avgMemMax>0`（真 JVM 堆）；trend 时间序列非空且字段为真值；persist 经 GORM 直读 `metric_sample` 已落样本；boundary 响应不含玩家名单 / 身份字段。

### 7.4 P1 v2 Bungee 注册确认真机 smoke/E2E

校验第二版 agent 身份、注册确认、namespace 隔离入口与区服权威首次分配链路在真实 BungeeCord 目录成立。默认使用本机 `D:\Games\MinecraftServer\BungeeCord`，测试会临时备份并替换 `plugins/BeaconAgentProxy*.jar`，同时备份 `plugins/BeaconAgentProxy/identity.yml`、`effective-config.snapshot.json`、`file-tree.applied.json`，结束后恢复。

可选环境变量：

- `E2E_BUNGEE_DIR`：真实 BungeeCord 目录，默认 `D:\Games\MinecraftServer\BungeeCord`。
- `E2E_BEACON_URL`：临时控制面地址，默认 `http://localhost:18848`。
- `E2E_JAVA`：指定 Java 可执行文件；未设时优先 `JAVA_HOME\bin\java.exe`。

运行：

```powershell
go test -tags=e2e -timeout=15m ./apps/server/test/e2e/p1v2 -v
```

测试依次断言：首启生成 `identity.yml`；新身份进入 pending 且归属目标 namespace；管理员 approve 后转 active 并继续衔接 legacy v1 online；approve 只创建未分配 proxy server；首次分配到 BC 集群成功；重启后 `identityId` 保持不变；损坏身份文件后 agent fail-closed，不静默重生成。

### 7.5 FR-146/147 健康真值与调度决策真机 E2E（指标窗口 → 健康计算 → 调度闭环）

纯 Go e2e，自起控制面（SQLite）+ 真 Paper + BeaconAgent，验证「真 agent 指标批 → 健康计算轮产出健康真值（`/admin/v2/health*` 的 score / level / schedulable / factors 与 `/admin/v2/metrics/summary` 实例计数）→ 建区首次分配后转 schedulable → agent 面调度闭环（candidates / decide / 决策异步落库经 `/admin/v2/sched-decisions*` 可查 / report-local 降级补报）」端到端成立。

前置同 §7.1（Go / JDK21 / CGO / 已构建前端 / 联网，首跑下载 Paper 耗时可观）；**默认 sqlite、无需 docker/MySQL**。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_BEACON_URL`（默认 `http://localhost:18850`）。运行（PowerShell；Bash 把赋值换成 `export` 即可）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./apps/server/test/e2e/schedhealth
```

依次断言三相位（任一 FAIL 即失败）：

- **health（FR-147）**：`/admin/v2/health` 出现该真 agent 条目且 score∈[0,100]、level 合法，未分配阶段 reasons 含 `unassigned`；详情 factors 非空、weightsRev≥1（cpu 因子容忍宿主采集不可用哨兵 -1，真值与否作观察项记日志）；`/admin/v2/metrics/summary` backend 计数 ≥1。
- **zone**：建 bc 集群 / 大区 / 小区并首次分配该 server 后，健康视图转 `schedulable=true`（zone 归属由控制面权威指派）。
- **sched（FR-146）**：candidates 含该 zone 与候选 → decide 选中该服（traceId 非空）→ 决策记录（source=`control_plane`）经详情 / 列表 / summary 可查 → report-local 补报 1 条本地决策 → 详情 source=`local_fallback`。

### 7.6 FR-148 本机 agent-api 调度门面 + fail-static 真机 E2E（真门面 → 杀控制面降级 → 恢复补报）

纯 Go e2e，自起控制面（SQLite）+ 真 Paper + BeaconAgent，与 §7.5 的本质区别：§7.5 用 Go HTTP 客户端**模拟** agent 面直调端点；本用例驱动**真 agent 的纯 Java 只读门面** `BeaconAgentProvider.get().scheduling().acquireCandidate(zone)`（经 BeaconE2E 探针周期取候选、把结果落 `plugins/BeaconE2E/e2e-scheduling.log`），验证 FR-148 的 fail-static 三条时序端到端成立。

前置同 §7.1；**默认 sqlite、无需 docker/MySQL**。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_BEACON_URL`（默认 `http://localhost:18850`）。运行（PowerShell；Bash 把赋值换成 `export` 即可）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./apps/server/test/e2e/schedagent
```

目标小区名经 `-Pe2eSchedZone` 注入 agent 环境变量 `BEACON_E2E_SCHED_ZONE`（agent 启动早于建区，故不能靠自身 zone 回填）。依次断言三相位（任一 FAIL 即失败）：

- **正常路径**：建区分配后真门面观测到 `source=CONTROL_PLANE` 且选中该服、候选快照就绪（`candidates≥1`）；控制面 decide 决策落库可查 `source=control_plane`。
- **fail-static（杀控制面）**：`cp.Stop()` 后真门面下一轮仍经本地快照返回候选 `source=LOCAL_FALLBACK` 选中该服、不阻断、无 `ACQUIRE_ERROR` 观测（探针持续产观测即 agent 未崩、玩家链路不阻断的活性证明）。
- **恢复**：重启控制面（同库）后 agent 自动回 `source=CONTROL_PLANE`；降级期本地决策经 `report-local` 补报入库可查 `source=local_fallback`。

### 7.7 FR-171 `hot_reload` 真机 E2E（配置工件热更 / 回滚 / 失败回执）

本用例由纯 Go 测试自起隔离 SQLite 控制面与真实 Paper，加载真实 BeaconAgent 和 BeaconE2E 业务插件，经管理 API 驱动变更单状态机；不依赖固定服务器目录，也不以 mock 回调代替平台链路。

前置同 §7.1：本机有 Go、JDK21、CGO 编译器和已构建前端，首次运行需联网下载 Paper。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_BEACON_URL` 覆盖默认控制面地址。

运行：

```powershell
$env:E2E_ADMIN_PASS='<临时管理员口令>'; $env:E2E_AUTH_SECRET='<临时签名密钥>'
go test -tags=e2e -timeout=30m ./apps/server/test/e2e/hotreload -run '^TestDeliveryHotReloadE2E$' -v -count=1
```

测试在同一真实 Paper 进程中依次断言：

- **正向生效**：V2 配置冻结工件经数据面落盘，BeaconE2E 业务插件通过 `BeaconAgentProvider.config().onChange` 收到固定路径通知并读到新内容；控制面目标进入 `activated`，覆盖前备份存在。
- **整单回滚**：先还原备份，再触发同一路径配置回调；目标与变更单均进入 `rolled_back`，磁盘内容恢复为交付前值。
- **失败回执**：失败专用路径的业务插件监听器留证后抛出受控异常；Agent 回执失败、目标进入 `failed`，随后通过新的存活观测、Minecraft TCP 端口与 Agent online 状态共同证明没有误走重启。

证据位置：控制面日志 `.tmp/beacon-hotreload.{out,err}.log`，Paper 日志 `.tmp/paper-hotreload.{out,err}.log`，业务插件观测 `apps/.tmp/e2e-run/bukkit/plugins/BeaconE2E/e2e-delivery-hot-reload.log`，交付备份 `apps/.tmp/e2e-run/bukkit/plugins/BeaconAgent/delivery-backups/<orderId>/`。

边界：本用例只证明 V2 配置工件免重启热更。普通文件与 JAR 在 `hot_reload` 下仅落盘，不触发插件框架重载；含 JAR 的变更必须依据管理台警告改用 `restart` 才能声明新 JAR 已生效。

## 8. 测试运行方式（单元 / 集成）

- **单元测试**（无外部依赖、快）：`go test ./...`。集成用例带 `//go:build integration` 标记、默认**不编译**，故此命令只跑纯逻辑单测——`apps/server/internal/service` / `apps/server/internal/server` 显示 `no test files` 属正常（其用例全为集成）。
- **集成测试**（需真实 MySQL）：先起测试库、设 DSN，再带 `integration` 标记跑：
  ```bash
  export BEACON_TEST_DSN='root:<密码>@tcp(127.0.0.1:3306)/beacon?charset=utf8mb4&parseTime=true&loc=UTC'
  go test -tags=integration ./... -count=1
  ```
  `apps/server/internal/testsupport` 会在该实例上按 `beacon_<suffix>` 建独立测试库（不污染基础库）；未设 `BEACON_TEST_DSN` 时集成用例 `t.Skip`。FR-32 的 `metric_sample` 仓库与 `/admin/v1/metrics/*` 端点集成亦在此 `-tags=integration` 套内。
- **agent 侧集成测试**（需真实 Redis）：agent-adapters 对真实 Redis 的集成用例（含 FR-31 名册 `HGETALL` 全表读）默认连 `localhost:16379` 无密码，连不上即 `assumeTrue` 跳过。先起 Redis、再跑：
  ```bash
  # 默认 16379，可经 BEACON_REDIS_TEST_HOST / BEACON_REDIS_TEST_PORT / BEACON_REDIS_TEST_PASSWORD 覆盖
  cd apps/agent && ./gradlew :agent-adapters:cleanTest :agent-adapters:test --tests '*RedisMessageTransportIntegrationTest'
  ```
  绿不等于真跑——须确认 `apps/agent/agent-adapters/build/test-results/test` 报告里该类 `skipped=0`（跳过即 Redis 没连上）。
- **CI / 发版前**：单测 + MySQL 集成 + agent Redis 集成都跑，E2E 另见 §7（跨平台 `go test -tags=e2e`，CI 见 `.github/workflows/e2e.yml`）。务必确认集成是 PASS 而非 SKIP。
- **前端单元测试**（vitest + React Testing Library，jsdom 环境、无外部依赖、不连后端）：`cd web && pnpm test`（监听模式 `pnpm test:watch`）。测试文件经 `tsconfig` 排除出生产 `tsc -b`，与 `make web` 的 `go:embed` 构建解耦。
