# Beacon 运维手册

> 面向部署与运维 Beacon 控制面。`docker-compose` 形态为单 Beacon 容器加 SQLite 持久卷；生产 MySQL 为自行提供的外置数据库。从零搭建集群见 [wiki/build-a-cluster.md](wiki/build-a-cluster.md)。

## 1. 部署
- 默认直接执行 `docker compose up -d`；Compose 启动 Beacon 并把 SQLite 数据保存在持久卷中。
- 如需覆盖监听、认证或数据库设置，再按 `.env.example` 设置对应环境变量；不要把 namespace token、口令或数据库凭据提交进仓库。
- 使用生产 MySQL 时，先独立部署并备份数据库，再在 Beacon 配置中提供外置连接信息；不要等待不存在的 Compose MySQL healthcheck，也不会自动预置业务 namespace。
- 管理台与 API 同端口（默认 8848）。

### 1.1 单二进制免容器首启
直接跑 `beacon` 二进制时**首次启动自动脚手架、开箱即跑**：在当前目录释放 `config.yml`（默认 sqlite、零依赖可跑），**释放时把留空的 `auth.password` / `auth.secret` 就地填入随机强值（文件 0600）**，随即直接启动（sqlite 落 `beacon.db`），无需手工 `export` 或填值（`config.yml` 已存在则不覆盖）。**不再自动生成 `.env`**——凭据就在 `config.yml`，避免 `.env`（优先级更高）静默盖掉你对 `config.yml` 的改动。上手：
- 运行 `beacon` → 直接起服（控制台 WARN 提示已释放 `config.yml`）。
- 打开当前目录 `config.yml`，取 `auth.password` 登录管理台（`http://本机IP:8848`，用户名 `auth.username`，默认 `admin`）；按需改 `config.yml`（切 mysql 改 `database` 段、改口令 / 端口 / token 等）后重启即生效。
- **接 Agent**：先在管理台创建 namespace 与该 namespace 的接入 token；Agent 最小配置只填控制面 endpoint 列表和这个 token，首次注册后在「服务器 → 待确认」批准身份并分配 serverId/拓扑。不要依赖历史全局 bootstrap token。**多台机器请用 §1.3 的批量接入脚本**，不要逐台手工重复这套调用。
- 如需经环境变量覆盖（如容器内、CI、临时改口令），真实环境变量与手动放置的 `.env` 仍生效，优先级 `真实 env > .env > config.yml`。
- 管理员口令 / 签名密钥强随机、不入库，**不得沿用任何固定弱默认口令**；管理台登录与写操作（发布 / 回滚 / 改派 / 文件覆盖 / 命令下发）都要求已认证的操作者身份并随审计留痕，agent 侧共享 token 只用于防误连、不构成安全边界；生产 MySQL 按外置数据库方式部署。

### 1.2 进程监督与崩溃自启（systemd / docker restart 推荐）
控制面是单进程，**进程崩溃自启交外部监督**——Beacon 自身不再带常驻监督进程。在线更新的「换版自替换 + 自动回滚」依赖外部监督在崩溃后重新拉起进程来累加重试计数、触发回退（见 [§2.2](#22-单进程自替换--自动回滚)），故**生产部署务必启用以下任一监督**：
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

### 1.3 批量接入多台服务器（脚本）
逐台手工接入（建 namespace → 每台写配置 → 启动 → 每台两步批准 → 逐台分配拓扑）在真机上约需 2×N 次接口调用，机器一多极易漏步。`scripts/ops/onboard_servers.sh` 把这条链路收敛成**一条可重入命令**：

```sh
# 1) 先干跑：只打印将做什么，不产生任何写操作（安全底线）
./scripts/ops/onboard_servers.sh --manifest .tmp/onboard.manifest --namespace demo \
  --admin-password-file <(grep -m1 '^BEACON_ADMIN_PASSWORD=' /opt/beacon/.env | cut -d= -f2-)

# 2) 确认无误再加 --apply 真正执行；重复执行会自动跳过已完成的台
./scripts/ops/onboard_servers.sh --manifest .tmp/onboard.manifest --namespace demo --apply \
  --admin-password-file <(grep -m1 '^BEACON_ADMIN_PASSWORD=' /opt/beacon/.env | cut -d= -f2-)
```

**凭据怎么到达 curl**：控制面凭据（管理台密码 / 会话 token / API key）只从文件或环境变量读入，再由脚本经 **0600 的 curl 配置文件（`-K`）**转交——`Authorization` 头与请求体都不展开进 curl 的 argv。argv 可从 `/proc/<pid>/cmdline` 读到（权限 `444`，**同机任何用户 `ps auxww` 都能看到**），因此密码、会话 token、API key 一律不得出现在命令行里。配置文件写在 `mktemp -d` 的 0700 临时目录内、用完即删，异常退出由退出钩子兜底。回归测试用一层包装 curl 记录每次调用的 argv 与配置文件权限来锁定该行为。

清单文件每行一台（`#` 注释，空白分隔的 `key=value`）：

```
serverId=onb-bc     dir=/srv/mc/onb-bc     role=proxy   target=bc_cluster:onb-bc1
serverId=onb-lobby  dir=/srv/mc/onb-lobby  role=backend target=lobby_cluster:5
serverId=onb-game-a dir=/srv/mc/onb-game-a role=backend target=zone:onb-zone1 default-entry
```

脚本按顺序做四件事，**任一步失败都给出可执行的修正指引**：
1. **预检**：控制面可达 / 凭据有效 / namespace 存在，并逐台比对 `plugins/BeaconAgent/config.yml`（代理为 `BeaconAgentProxy`）里的 `beacon.endpoints[0]` 与 `beacon.bootstrap-token`；不一致时直接点名**该改哪个文件的哪个字段、现值与应改值**（token 只打前 11 位前缀，不落明文）。加了 `--expect-token-file` 可与控制面当前 token 精确比对，否则退化为「各台之间必须一致」的弱校验。
2. **等 pending**：按 `serverWorkDir` 轮询 `agent-identities` 直到目标出现 `pending`（`--wait` 超时），已在 `active` 的直接跳过。
3. **批量批准**：逐台 `POST /admin/v2/agent-identities/{id}/approve`（带 `serverId`）→ 用返回的 `approvalRequestId` 调 `POST /admin/v2/approval-requests/{id}/approve` → **轮询到 identity 真正 `active`**（不把 `202` 当成功）。
4. **批量归属**：按端点差异自动选路——大厅成员走 `server-placement-transfers`（字符串 `serverId`），区 / BC 集群**首次分配**走 `server-assignments`、**改派**走 `server-rezones`（两者都用 servers 表数字行 id）；每条都要再批准一次，脚本内部消化。已归属到目标的直接跳过。
   - **改派比首次分配多一步**：换区是两段式——批准工单只做「清空全部归属 + 写预填目标 + 把绑定身份重入 `pending`」，归属要等该身份**再次确认**时才落地。脚本会在批准工单后自动等身份回到 `pending`、再补一次身份批准（汇总表里标为「重批准」），无需人工介入；首次分配与大厅迁移都在批准工单时立即落位，没有这一步。

末尾输出中文汇总表（哪台成功 / 哪台卡在哪一步 / 下一步做什么），退出码 `0` 全部达成、`1` 预检失败、`2` 用法错误、`3` 运行期有台未达成。

**轮换接入 token 是独立的一次性命令，不要和接入流程一起跑**（`--rotate-token-only`）：

```sh
# 只轮换、不接入；新明文按 0600 落盘。默认仍是 dry-run，加 --apply 才真轮换。
./scripts/ops/onboard_servers.sh --namespace demo --apply \
  --rotate-token-only --token-out /root/beacon-demo.token \
  --admin-password-file <(grep -m1 '^BEACON_ADMIN_PASSWORD=' /opt/beacon/.env | cut -d= -f2-)
```

- **爆炸半径**：轮换让该 namespace 下**所有既有 agent 立刻 401**（旧 token 立即失效）。轮换后必须把新明文写入每台的 `plugins/BeaconAgent/config.yml`（代理为 `BeaconAgentProxy`）的 `beacon.bootstrap-token`、重启（或重载）实例、在管理台确认身份重新 `active`，再跑一次接入流程预检——脚本在真正轮换后会把这三步打在输出里。
- **为什么拆开**：旧开关 `--rotate-token` 把「轮换」和「按期望 token 逐台比对配置」塞进同一次执行——轮换一生效，各台配置里仍是旧 token，预检必然报「与期望不一致」并退出，而新明文只在那一次响应里出现、调用方拿不到，结果是域内 agent 全断且不可恢复。该开关现已**废弃并直接报用法错误**，只保留指向 `--rotate-token-only` 的提示。
- **新明文必须有可靠去处**：`--token-out <文件>`（推荐，0600 落盘，目标已存在则拒绝覆盖，避免静默丢掉上一个 token）或 `--print-token`（只打印一次，明文会进终端回滚缓冲与会话日志，非必要不用）；两者都不给则拒绝执行。落盘失败时脚本会把这次唯一可见的明文打到 stderr，避免「轮换已生效却没人知道新 token」。
- 该子命令不执行接入流程（不拉身份、不批准、不归属），且同样默认 dry-run；`--manifest` 与它互斥。

**边界**：拓扑节点（BC 集群 / 大区 / 小区 / 大厅集群）的**创建**仍属人工规划，脚本只做归属分配；控制面凭据一律走文件或环境变量，并由脚本经 0600 的 curl 配置文件转交，不写在命令行。行为级回归测试：`sh scripts/ops/test_onboard_servers.sh`（本地伪控制面，覆盖预检报错定位、两步批准、轮询生效、三条归属端点、dry-run 零写请求、重复执行幂等、**凭据不进 argv / 0600 配置文件**、轮换子命令的落盘与拒绝覆盖）。

## 2. 升级与发布
- **升级前先备份数据库（SQLite 文件/持久卷或外置 MySQL）与 Agent 本地状态**（见 §4）。
- 控制面：拉取已核验的 GA 产品资产，再重启服务；数据库迁移只允许 expand / 可重入回填，回滚不依赖 down migration。
- agent：按批替换 Bukkit/Bungee JAR 并重启节点，保留 `plugins/Beacon/` 本地身份、配置快照、流位置与幂等账本。控制面与 agent 的产品版本必须一致。
- 产品资产发布前使用 `SHA256SUMS.txt` 校验；平台是否发布由本次 RC 的实际资产决定，不额外引入阶段专属平台准入。

### 2.1 开发构建、发布准备与 RC/GA 流程

- **PR 只过质量门**：pull request 运行现有质量任务，不执行 `make package`，也不上传可下载产品包。
- **`master` 临时开发构建**：质量任务全部成功后，CI 才运行现有 `make package` 并上传短期 Actions Artifact。该 Artifact 仅用于开发验证，不能直接晋级为 RC 或 GA。
- **发布准备**：发布准备只更新根 `VERSION` 与 `CHANGELOG.md`，版本由根 `VERSION` 唯一确定。
- **RC**：按目标版本创建 `vX.Y.Z-rc.N`，固定一个 commit 和一次构建出的 GitHub 产品资产；候选发布后不可移动、覆盖或补传。完成候选 Release 后，只向远程 Maven releases 仓库发布 `beacon-agent-api` 与 `beacon-agent-kit` 的 `X.Y.Z-rc.N` 坐标。`make release-verify-rc` 必须从仓库中的真实 RC tag 解析 peeled commit，并与发布流程锁定的 40 位提交身份一致，手工传入任意 SHA 不能替代真实 tag。
- **GA**：最终 RC 与 GA 必须指向同一 commit。GA 先把最终 RC GitHub 产品资产原样复制到独立目录，再以 RC 下载目录为不可变基准运行 `make release-verify-ga`；创建前与公开回拉后都逐项比较名称、字节大小和 SHA-256。GitHub 产品资产不得重新编译、打包、重建、重签或替换；资产晋级成功后，GA 只为 `beacon-agent-api` 与 `beacon-agent-kit` 的不同 `X.Y.Z` Maven 坐标重新生成并发布制品。
- **Maven 凭据与失败处理**：发布地址及凭据仅由发布流水线的 Secret 注入（`BEACON_PUBLISH_*`），仓库内不保存这些值。远程 Maven release 坐标不可覆盖、删除或重传；发布失败时必须让本次发布显式失败，处理前先核验远端坐标状态。
- **在线更新**：只自动消费严格 `vX.Y.Z` 的 GA，RC 和开发 Artifact 必须显式安装。

通用校验入口：

```sh
# 只校验 VERSION、RC/GA tag 格式和 GA 发布流程；不要求 tag 已存在。
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

### 2.2 单进程自替换 + 自动回滚
控制面是**单一 `beacon[.exe]`**——在线更新由主进程在自身进程内完成自我替换，无独立监督进程、无退出码交接。换版机制：
- **自替换换版**：在线更新下载校验落位 `beacon.new[.exe]`（运行二进制同目录同卷）后，主进程**优雅关停释放端口** → `rename` 让位三步（`beacon`→`beacon.old`、`beacon.new`→`beacon`）→ spawn 新进程（继承命令行参数 / 环境变量 / 工作目录 / 标准流）→ 旧进程正常退出。Windows 同样允许 rename 运行中的 exe（重命名只改目录项、已打开句柄仍指向原映像），故让位可行；换二进制失败则就地回退、以旧版重启兜底。其间有**亚秒级**端口不可用窗口，agent 按本地快照继续（fail-static），玩家进服不受影响。
- **自动回滚（崩溃循环闭合）**：换二进制成功后写 sentinel 标记（运行二进制同目录小文件，记崩溃计数 `attempt` + 目标版本）。新版**启动早期**（HTTP 起之前）自检——稳定运行**过验证期 10 秒**或收到正常关停信号（如 `Ctrl+C` / `docker stop`，视为新版已起来被操作）即判定成功，删 sentinel + 删 `.old` 清理备份；若换版后**反复起不来**（崩溃计数达阈值，默认 3）则在启动早期自动 rename 回退 `.old` 并重启旧版，最终以旧版稳定运行——**不依赖任何外部进程**。坏新版归档为 `beacon.failed[.exe]` 便于事后排查。
- **崩溃自启交外部监督**：进程崩溃由 docker / systemd 拉起（部署见 [§1.2](#12-进程监督与崩溃自启systemd--docker-restart-推荐)），自动回滚依赖此重启逐次累加 `attempt` 至阈值后回退。**裸跑 `beacon` 无外部监督时**新版崩溃即停（可接受），但仍可靠下次手动启动触发自检回退。
- **容器形态**：Docker 镜像 `ENTRYPOINT` 为 `beacon`，崩溃自启靠 compose `restart` 策略。**容器内在线更新换二进制仅临时有效**——镜像不可变，容器一旦重建即丢更新；**容器形态的生产升级一律以重拉镜像为准**（拉新镜像 → `docker compose up -d beacon`，见 §2 上文），不要依赖容器内自更新。

## 3. 健康与观测
- 健康探针：`GET /admin/v1/namespaces`（只读、无副作用）。
- 日志：beacon 容器内中文分级日志（ERROR/WARN/INFO/DEBUG）。
- 重点关注：实例失联告警、重复 serverId 告警、配置漂移告警。
- 健康分级与告警：实例按心跳陈旧度推进 `online → degraded → lost → offline`（阈值见 `config.yml` 的 `health.degraded-after-sec`/`ttl-sec`/`offline-grace-sec`，须满足 degraded < ttl < offline，错序启动即报错）。**只有失联级（`lost` / `offline`）主动告警**：`degraded` 是「心跳变陈旧但未达 TTL」的提示级态，一次网络抖动就会让一批实例同时进入而刷屏，故不单独告警（真机降噪），真要失联会随后转 `lost` 照常告警；恢复 `online` 也不告警。告警通道：**站内信**（`GET /admin/v1/alerts` 读最近 N 条，N=`alert.inbox-capacity`，进程内、控制面重启清零）+ **webhook**（配置 `alert.webhook.url` 后向其 POST 告警 JSON，留空则仅站内信）。
- 告警自动消解：实例**恢复 `online`**、被**归档 / 永久删除**，以及实例被**外部删除**（压测实例用完即删等，控制面收不到删除信号）三类情况都会把未处理告警置为 `resolved` 并记 `handled_by=system`，管理台据此区分系统自动消解与人工处理，无需人工清待办。外部删除这一类由后台清理器兜底，**四项判据同时成立**才关闭：有未处理告警、不在运行时注册表、**不在 `server` 表活动目录（`lifecycle = active`）**、且最近触发已超 `alert.orphan-timeout-hours`（默认 24h，可在设置页热改）。**在册实例即使离线很久也不会被自动关闭**（运维必须看到）；排查「告警凭空变成已处理」时先看 `handle_note`（自动消解文案）与控制面日志「失联孤儿告警已自动消解」。
### 3.1 SSE 推送流经反向代理 / Docker

agent↔控制面用单条 SSE 流 `GET /beacon/v1/agent/stream` 做 server→agent 推送（配置、文件树、覆盖集等变更通知统一走这一条流，agent 收到通知后仍用原有 HTTP 端点取内容）。若在 beacon 前放反向代理（nginx 等），须保证流不被缓冲、不被空闲超时切断：
- **关闭响应缓冲**：beacon 已对该响应输出 `X-Accel-Buffering: no`（nginx 据此关 proxy buffering）；其它代理请按等价方式关闭对 `text/event-stream` 的缓冲。nginx 还需 `proxy_http_version 1.1;` + `proxy_set_header Connection "";`。
- **调长读超时**：把代理对 agent stream 路径的读超时（nginx `proxy_read_timeout`）调到显著大于 beacon 的保活间隔（默认取长轮询挂起上限），避免空闲被误判断流。beacon 无变更时按间隔发 SSE 注释行（`: ping`）保活。
- **Docker 网络**：沿用现有"agent 能直连 beacon 地址"的可达约束，无新增端口；SSE 走与 API 同一端口（默认 8848）。
- **断流不影响判活**：健康 online/lost/offline 仍由独立心跳 + TTL 决定，SSE 抖动断流不会误判失联；agent 流断按本地快照继续、自动退避重连并对账（fail-static）。

## 4. 数据库备份与恢复（关键）
> 数据库是**配置权威库**——丢失会导致全集群配置不可恢复。务必定期备份，并在隔离环境演练恢复。

- **Compose 默认 SQLite**：先正常停止 Beacon，再备份其持久卷中的 SQLite 数据库；恢复时保持 Beacon 停止，替换数据库后再启动。不要在运行中的 SQLite 文件上直接复制。
- **外置 MySQL**：由数据库平台或受控的 `mysqldump`/恢复流程备份和恢复；命令通过安全的凭据注入方式执行，不在 shell 历史、文档或日志中拼接明文密码。
- **常态化**：至少每日备份、保留满足恢复目标的版本，并保留一份异机副本。
- **恢复演练**：导出后恢复到隔离环境，启动 Beacon 并核对 namespace、身份、拓扑与审计的完整性；确认无误后才把该备份视为可用。

## 5. 回滚
- **RC 回滚**：未晋级候选直接停止使用；不得覆盖原 RC 资产。修复后从新 commit 创建 `vX.Y.Z-rc.(N+1)`，重新构建并校验完整资产。
- **GA 运行回滚**：使用上一版已核验的 GA 产品资产和升级前备份。若 schema 不兼容，必须先停写并恢复备份，禁止执行未经验证的 down migration。
- **产品资产**：已发布版本的文件不得覆盖；发现缺陷必须发布新的 RC/GA 版本。
- **业务配置回滚**：使用管理台配置版本回滚，不需重新部署。
- **代码层回滚**：见 `sdd-rollback-change` 技能。

## 6. 排障
- beacon 起不来：先看日志与配置校验；SQLite 检查数据卷挂载、路径和权限，外置 MySQL 再检查 DSN、网络和数据库可用性。
- agent 连不上：核对控制面地址、`X-Beacon-Token`、网络连通。
- 配置不热更：看 agent 长轮询是否在连、控制面是否唤醒了受影响集合、有效配置 md5 是否真变。
- **控制面短暂不可用时不要重启子服**：agent 会按本地快照 fail-static 继续，控制面恢复后自动重连。

## 7. 端到端验收（agent 真机接入联调）
用仓库内 **agent 侧**的验收模块在真机 Bukkit/Bungee 上自检「首次接入 + 发布热更 + 审计可查」。Gradle 统一使用 `mc-testkit 0.5.0` 自动下载并编排 Paper 1.20.4 与原生 BungeeCord，无需手工准备 MC 服（也不再需要 jpenilla run-task 或 Waterfall 代验）。

> **本地前置（工具链）**：下面各节的 E2E 都需在本机构建控制面二进制并经 Gradle 起真机服务端，跑前先就位：
> - **JDK21**：运行 Gradle、Paper 与 BungeeCord。Windows 上若 `JAVA_HOME` 路径含 `!` 等特殊字符，`gradlew.bat` 可能回退到 PATH 上的旧 JDK；E2E 会调用 agent 构建目录下的 Gradle 包装器并继承当前环境，跑前把 `JAVA_HOME` 显式指向干净路径的 JDK21。
>   **本机构建 agent 制品（`make agent` / `make package`）同样要求 JDK21**：`mc-testkit 0.5.0` 插件在配置阶段就校验运行期 JVM ≥ 21，若 `JAVA_HOME` 仍指向 JDK 8 会直接失败并报 `Dependency requires at least JVM runtime version 21. This build uses a Java 8 JVM.`——注意这看起来像构建脚本出错，实为选错了 JDK。Linux/macOS 上写成 `JAVA_HOME=/path/to/jdk-21 make package` 即可（`mc-testkit 0.5.0` 之后的版本对 JVM 的要求以插件自身声明为准）。
> - **已构建前端**：控制面把管理台前端构建产物编译进二进制，跑前先在仓库根执行 `make web`（等价于 `pnpm install --frozen-lockfile && pnpm --filter @beacon/web build`），否则二进制内只会有占位页面。
> - **mc-testkit 0.5.0**：由 Gradle 从 `repo.wcpe.top` 解析正式工件，无需额外安装（只需 JDK21 + 联网）。

验收模块统一声明三个 Gradle 入口（都在 agent 构建目录下执行）：

- `servePaper`：一个 Paper 后端，注入 `BeaconAgent` 与 `BeaconE2E`。
- `serveDirectory`：同一生命周期内启动 Paper 后端 + 原生 BungeeCord，双端分别注入 Agent 与探针；BungeeCord 静态路由名固定为 `backend`。
- `serveProxy`：为代理连接探针启动原生 BungeeCord；mc-testkit 同时启动仅用于路由就绪的伴随 Paper 后端。

手动执行时进入 agent 构建目录（仓库的 `agent` 子目录），用其中的 Gradle 包装器运行 `./gradlew :agent-e2e:<任务名> --no-daemon`（Windows 对应 `gradlew.bat`），并自行提供与 E2E 相同的 `BEACON_AGENT_*` 环境变量——包括控制面 endpoint、接入 token、namespace、serverId/address、命令白名单与探针开关；**动态凭据请走环境变量，不要放进 Gradle `-P` 参数**（会进命令行）。

运行证据统一位于 mc-testkit 运行目录（Paper 为 `.../mc-testkit/run/`、BungeeCord 为 `.../mc-testkit/run-proxy/`、编排结果与 pid 为 `.../mc-testkit/results/`，均在 agent 构建目录的 `agent-e2e/build` 下）以及仓库根 `.tmp/` 下的控制面与 Gradle 标准输出 / 错误日志。**跑完请确认这些日志与产物中不含管理员口令、签名密钥、数据库 DSN、接入 token 等凭据**，涉密文件不要外发或上传。

### 7.1 三方覆盖 + 受限重载命令真机 E2E（RCE 面，启用命令白名单前必跑）

校验「三方插件文件覆盖 + 受限重载命令」整链与以下安全不变量在真机成立：命令只能是平台控制台命令、**物理上无法落到 OS shell**；命令首 token 白名单由 **agent 本地配置**持有、不由控制面下发，且**默认为空**（空白名单 = 一条都不派发）；单条命令禁含 `; & | > < $` 与换行等元字符；覆盖路径限定在该插件自身目录内，禁 `..`、绝对路径/盘符，且禁覆盖 `.jar` 与 `server.properties` 等服务器关键文件；回滚只还原文件与状态，**绝不重放重载命令**。验收插件 `BeaconE2E` 兼作被覆盖目标：种原文件 `managed.yml`、注册受限重载命令 `beacone2ereload`、轮询观测文件变更与命令收到（记到 `e2e-override-observations.log`）。

入口为纯 Go 测试、**真跨平台**（Windows/Linux/macOS 一致），由测试自管控制面 + 真 Paper 生命周期，逐相位收口、无悬挂进程。

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
go test -tags=e2e -timeout=30m ./... -run '^TestOverrideE2E$'
```

测试依次跑四相位（任一 FAIL 即测试失败）：

- **inert（空白名单）**：覆盖集发布后文件被覆盖为新内容、但受限重载命令**一条不派发**（空白名单即 inert 默认行为）。
- **filetree**：发布一个文件树文件 → agent 镜像落盘到插件真实数据目录 → 验收插件读到镜像内容。
- **ordering（放行白名单）**：验「备份原文件 → 原子覆盖 → 落盘成功后才派发命令」次序（命令收到时磁盘已是覆盖后内容），再回滚到无命令版本验「只还原事实、不重放命令」。
- **failstatic**：杀控制面后受管文件不动、命令不发。

> 也可在控制面起着时用浏览器人工自检：登录管理台 →「文件树托管」看托管文件 →「文件覆盖集」详情看**发布前 dry-run 只读预览**（将覆盖哪些文件 / 执行什么命令 + 二次确认勾选门控发布）。

**注意（当前边界）**：管理台尚未提供覆盖集成员的挂载入口——覆盖集只能先建空壳、成员需另行写入底层数据，属已知缺口（见 CHANGELOG 已知项）。因此上面的真机 E2E 是现阶段验证该能力的主要手段。

### 7.2 Proxy 目录注入真机 E2E（服务发现延伸出口）

校验「在线 `role=bukkit` 子服按 `serverId` 注入 Bungee 目录」在真机成立。控制面用 **SQLite 开发模式**（无需 Docker/MySQL）。代理侧验收插件周期把 Bungee `ServerInfo` 目录与 `beacon` 命令注册状态覆写到 `plugins/BeaconE2EProxy/e2e-directory-latest.txt`，供测试驱动断言。

入口为纯 Go 测试、**真跨平台**，由测试自管控制面，并通过单个 `serveDirectory` 生命周期启动真 Paper 子服 + 原生 BungeeCord 代理，逐相位收口。

前置：本机有 Go / JDK21 + 联网（首跑下载 Paper/BungeeCord）。**默认 sqlite、无需 docker/MySQL**。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_DB_DRIVER`（默认 `sqlite`）、`E2E_DB_DSN`（driver=mysql 时）、`E2E_BEACON_URL`（默认 `http://localhost:8848`）。运行（PowerShell；Bash 把赋值换成 `export` 即可）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./... -run '^TestDirectoryE2E$'
```

测试依次跑两相位（任一 FAIL 即测试失败）：

- **directory**：在线 `role=bukkit` 子服按 `serverId` 注入 Bungee 目录（地址含子服端口）、mc-testkit 固定静态路由 `backend` 保留不被覆盖、运行时实现标识精确为 `BungeeCord`、`beacon` 命令已注册。
- **failstatic**：杀控制面后已注入目录与手工服**不被清空**（fail-static）。

### 7.3 可观测看板真机 E2E（指标上报 → 采样落库 → 端点返真值）

纯 Go e2e，自起控制面（SQLite，经 `BEACON_METRIC_SAMPLE_INTERVAL_SEC` 调小采样间隔）+ 真 Paper + BeaconAgent，验证「agent 上报真 JVM 负载 → 采样器落 `metric_sample` → `/admin/v1/metrics/summary` 与 `/trend` 返真值 → 边界无玩家名单」整链成立。看板只展示负载数字（人数 / TPS / 内存 / CPU），**不展示任何玩家名单或身份字段**——这是不可越过的边界，名单展示属业务插件职责。

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./... -run '^TestMetricsE2E$'
```

依次断言四相位（任一 FAIL 即失败）：summary 含目标子服且 `avgMemMax>0`（真 JVM 堆）；trend 时间序列非空且字段为真值；persist 经 GORM 直读 `metric_sample` 已落样本；boundary 响应不含玩家名单 / 身份字段。

### 7.4 Bungee 注册确认真机 smoke/E2E

校验 agent 身份、注册确认、namespace 隔离入口与区服权威首次分配链路在真实 BungeeCord 目录成立。**该用例会临时备份并替换目标目录中的 `plugins/BeaconAgentProxy*.jar`，同时备份 `plugins/BeaconAgentProxy/identity.yml`、`effective-config.snapshot.json`、`file-tree.applied.json`，结束后恢复**，因此只允许指向**专用隔离目录**（不能指向你日常使用的 Bungee 安装）：目录须为绝对路径、非符号链接，且内含隔离标记文件 `.beacon-e2e-isolated` 与 `BungeeCord.jar`，否则用例直接拒绝执行。该用例仅在 Windows 上执行，其他平台自动跳过。

必填环境变量：

- `E2E_BUNGEE_DIR`：隔离 BungeeCord 目录（绝对路径，须含上述隔离标记与 `BungeeCord.jar`）。
- `E2E_ADMIN_PASS`：管理员口令。
- `E2E_AUTH_SECRET`：令牌签名密钥。
- `E2E_BOOTSTRAP_TOKEN`：接入 token。

可选环境变量：

- `E2E_BEACON_URL`：临时控制面地址，默认 `http://localhost:18848`。
- `E2E_JAVA`：指定 Java 可执行文件；未设时优先 `JAVA_HOME\bin\java.exe`。

运行：

```powershell
go test -tags=e2e -timeout=15m ./... -run '^TestP1V2BungeeRegistrationSmoke$' -v
```

测试依次断言：首启生成 `identity.yml`；新身份进入 pending 且归属目标 namespace；管理员 approve 后转 active 并继续衔接 legacy v1 online；approve 只创建未分配 proxy server；首次分配到 BC 集群成功；重启后 `identityId` 保持不变；损坏身份文件后 agent fail-closed，不静默重生成。

### 7.5 健康真值与调度决策真机 E2E（指标窗口 → 健康计算 → 调度闭环）

纯 Go e2e，自起控制面（SQLite）+ 真 Paper + BeaconAgent，验证「真 agent 指标批 → 健康计算轮产出健康真值（`/admin/v2/health*` 的 score / level / schedulable / factors 与 `/admin/v2/metrics/summary` 实例计数）→ 建区首次分配后转 schedulable → agent 面调度闭环（candidates / decide / 决策异步落库经 `/admin/v2/sched-decisions*` 可查 / report-local 降级补报）」端到端成立。

前置同 §7.1（Go / JDK21 / 已构建前端 / 联网，首跑下载 Paper 耗时可观）；**默认 sqlite、无需 docker/MySQL**。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_BEACON_URL`（默认 `http://localhost:18850`）。运行（PowerShell；Bash 把赋值换成 `export` 即可）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./... -run '^TestSchedHealthE2E$'
```

依次断言三相位（任一 FAIL 即失败）：

- **health**：`/admin/v2/health` 出现该真 agent 条目且 score∈[0,100]、level 合法，未分配阶段 reasons 含 `unassigned`；详情 factors 非空、weightsRev≥1（cpu 因子容忍宿主采集不可用哨兵 -1，真值与否作观察项记日志）；`/admin/v2/metrics/summary` backend 计数 ≥1。
- **zone**：建 bc 集群 / 大区 / 小区并首次分配该 server 后，健康视图转 `schedulable=true`（zone 归属由控制面权威指派）。
- **sched**：candidates 含该 zone 与候选 → decide 选中该服（traceId 非空）→ 决策记录（source=`control_plane`）经详情 / 列表 / summary 可查 → report-local 补报 1 条本地决策 → 详情 source=`local_fallback`。

### 7.6 本机 agent-api 调度门面 + fail-static 真机 E2E（真门面 → 杀控制面降级 → 恢复补报）

纯 Go e2e，自起控制面（SQLite）+ 真 Paper + BeaconAgent，与 §7.5 的本质区别：§7.5 用 HTTP 客户端**模拟** agent 面直调端点；本用例驱动**真 agent 的纯 Java 只读门面** `BeaconAgentProvider.get().scheduling().acquireCandidate(zone)`（经验收插件探针周期取候选、把结果落 `plugins/BeaconE2E/e2e-scheduling.log`），验证 fail-static 三条时序端到端成立。

前置同 §7.1；**默认 sqlite、无需 docker/MySQL**。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_BEACON_URL`（默认 `http://localhost:18850`）。运行（PowerShell；Bash 把赋值换成 `export` 即可）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
go test -tags=e2e -timeout=30m ./... -run '^TestSchedAgentFailStaticE2E$'
```

目标小区名经 Gradle 参数 `e2eSchedZone` 传给 agent 环境变量 `BEACON_E2E_SCHED_ZONE`（agent 启动早于建区，故不能靠自身 zone 回填）。依次断言三相位（任一 FAIL 即失败）：

- **正常路径**：建区分配后真门面观测到 `source=CONTROL_PLANE` 且选中该服、候选快照就绪（`candidates≥1`）；控制面 decide 决策落库可查 `source=control_plane`。
- **fail-static（杀控制面）**：控制面停掉后，真门面下一轮仍经本地快照返回候选 `source=LOCAL_FALLBACK` 选中该服、不阻断、无 `ACQUIRE_ERROR` 观测（探针持续产观测即 agent 未崩、玩家链路不阻断的活性证明）。
- **恢复**：重启控制面（同库）后 agent 自动回 `source=CONTROL_PLANE`；降级期本地决策经 `report-local` 补报入库可查 `source=local_fallback`。

### 7.7 `hot_reload` 真机 E2E（配置工件热更 / 回滚 / 失败回执）

本用例由纯 Go 测试自起隔离 SQLite 控制面与真实 Paper，加载真实 BeaconAgent 和 BeaconE2E 业务插件，经管理 API 驱动变更单状态机；不依赖固定服务器目录，也不以 mock 回调代替平台链路。

前置同 §7.1：本机有 Go、JDK21 和已构建前端，首次运行需联网下载 Paper。必填 `E2E_ADMIN_PASS` / `E2E_AUTH_SECRET`；可选 `E2E_BEACON_URL` 覆盖默认控制面地址。

运行：

```powershell
$env:E2E_ADMIN_PASS='<临时管理员口令>'; $env:E2E_AUTH_SECRET='<临时签名密钥>'
go test -tags=e2e -timeout=30m ./... -run '^TestDeliveryHotReloadE2E$' -v -count=1
```

测试在同一真实 Paper 进程中依次断言：

- **正向生效**：V2 配置冻结工件经数据面落盘，业务插件通过 `BeaconAgentProvider.config().onChange` 收到固定路径通知并读到新内容；控制面目标进入 `activated`，覆盖前备份存在。
- **整单回滚**：先还原备份，再触发同一路径配置回调；目标与变更单均进入 `rolled_back`，磁盘内容恢复为交付前值。
- **失败回执**：失败专用路径的业务插件监听器留证后抛出受控异常；Agent 回执失败、目标进入 `failed`，随后通过新的存活观测、Minecraft TCP 端口与 Agent online 状态共同证明没有误走重启。

证据位置：控制面日志与 Paper 日志在仓库根 `.tmp/`，业务插件观测状态与交付备份在本次运行的 Paper 运行目录内对应插件的数据目录下（`plugins/BeaconE2E/`、`plugins/BeaconAgent/delivery-backups/<orderId>/`）。

边界：本用例只证明 V2 配置工件免重启热更。普通文件与 JAR 在 `hot_reload` 下仅落盘，不触发插件框架重载；含 JAR 的变更必须依据管理台警告改用 `restart` 才能声明新 JAR 已生效。

## 8. 测试运行方式（单元 / 集成）

- **单元测试**（无外部依赖、快）：在仓库根执行 `go test ./...`。集成用例带 `//go:build integration` 标记、默认**不编译**，故此命令只跑纯逻辑单测——服务层与 HTTP 层那几包显示 `no test files` 属正常（其用例全为集成）。
- **集成测试**（需真实 MySQL）：先起测试库、设 DSN，再带 `integration` 标记跑：
  ```bash
  export BEACON_TEST_DSN='root:<密码>@tcp(127.0.0.1:3306)/beacon?charset=utf8mb4&parseTime=true&loc=UTC'
  go test -tags=integration ./... -count=1
  ```
  集成套件会在该实例上按 `beacon_<suffix>` 建独立测试库（不污染基础库）；未设 `BEACON_TEST_DSN` 时集成用例 `t.Skip`。`metric_sample` 仓库与 `/admin/v1/metrics/*` 端点集成亦在此 `-tags=integration` 套内。
- **CI / 发版前**：单测 + MySQL 集成都跑，E2E 另见 §7（跨平台 `go test -tags=e2e`）。务必确认集成是 PASS 而非 SKIP。
- **前端单元测试**（vitest + React Testing Library，jsdom 环境、无外部依赖、不连后端）：在仓库根执行 `pnpm --filter @beacon/web test`（监听模式 `pnpm --filter @beacon/web test:watch`）。测试文件不进入生产构建，与 `make web` 的前端打包解耦。

## 9. MCP 反向代理验收

启用前必须把 `mcp.public-base-url` 设为唯一 HTTPS 公网基址，并把实际 TLS 反向代理的来源网段写入 `mcp.trusted-proxy-cidrs`。代理转发 MCP、token 与 `.well-known` 时保留 Host，并固定传递 `X-Forwarded-Proto: https`、`X-Forwarded-Host`；后端不以直连或客户端自带转发头推断公网 URL。未在真实反代上分别验证 observer 与 automation 的换 token、初始化、工具发现、轮换和吊销即时失效前，不得宣称公网 MCP 已验收。

**日常运维入口**：管理台「系统 → MCP 客户端」（`/mcp-clients`）用于查看客户端清单、创建 / 轮换 / 启用 / 吊销，并只读查看入口的部署配置（启用状态、公网基址、可信网段与两个开关）——排查「外部 Agent 连不上」时可先看该页确认 `enabled` 与基址是否符合预期。该页只读展示这些**启动项**：修改仍需编辑配置文件并重启控制面。创建与轮换的明文 secret 只在提交申请的那次响应出现一次，遗失需重新申请轮换。

### 9.1 内网明文直连部署（allow-insecure-internal）

无 TLS 终止、无反向代理的内网 / 回环部署用 `mcp.allow-insecure-internal: true` 放宽为 http。此时 Host 校验改为白名单模式，**有一条容易踩空的规则**：

- `allowed-hosts` **留空时只放行与 `public-base-url` 的 host 完全一致的 Host**，不会自动放行 `127.0.0.1` 或 `localhost`。
- 因此若 MCP 客户端实际连的是 `http://127.0.0.1:<port>`（例如跑在同一台机器上的 stdio 桥接进程），而 `public-base-url` 写的是对外的内网地址，则必须在 `allowed-hosts` 中**显式**列出客户端使用的 host:port，否则请求会以 401 `ADMIN_UNAUTHORIZED` 被拒，且日志里只能看到鉴权失败、看不出是 Host 白名单导致。

```yaml
mcp:
  enabled: true
  # 对外基址（token issuer 与 audience 由它派生，客户端 audience 必须与此一致）
  public-base-url: "http://<内网地址>:19999"
  allow-insecure-internal: true
  # 显式放行本机回环入口，否则同机 MCP 客户端连 127.0.0.1 会被 401
  allowed-hosts:
    - "<内网地址>:19999"
    - "127.0.0.1:19999"
    - "localhost:19999"
```

**客户端侧对齐三要素**：直连部署下换 token 请求必须同时满足 `audience == public-base-url + /admin/v2/mcp`、`client_id/client_secret` 属于**该实例自己的库**（多套 Beacon 部署共存时最容易拿错别家的凭据，症状同样是 401 `invalid_client`）、以及 Host 命中白名单。三者任一不符都只回 401，需分别核对。

### 9.2 客户端凭据直连（长驻客户端免自行续期）

`/admin/v2/mcp` 的 bearer 接受两类凭据：

- `mct_`：经 `POST /admin/v2/oauth/token` 换取的短期 access token（15 分钟）；
- `mcs_`：客户端 secret 本身，直接当 bearer 使用（**客户端凭据直连**）。

直连模式适合**长驻客户端**（常驻 Agent 运行时、MCP 客户端进程）：它不必自己实现 15 分钟续期循环，也就不再需要外部 stdio 桥接脚本代持并刷新凭据。用法就是把 `clientSecret` **原样**写进 `Authorization: Bearer`，不额外拼前缀、不加引号或换行。

**运维要点**：

- **直连凭据没有自然过期**，等同长期凭据——生产环境**必须经 HTTPS 反向代理**；`mcp.allow-insecure-internal: true` 的明文直连只用于内网 / 回环。凭据只放配置文件或密钥管理，不要写进日志、工单或截图。
- **吊销与轮换即时生效**：直连路径每次请求按 `secret_hash` 现查现比（无缓存、无 TTL），吊销或轮换后无需重启、也无需等待窗口过期。应急处置优先用「管理台 → 系统 → MCP 客户端 → 吊销」。
- 明文 secret 只在创建 / 轮换申请的那次 `202` 响应出现一次，遗失只能重新申请轮换（重新申请同时使旧 secret 失效）。
- 直连路径**不校验 audience**（端点固定 `/admin/v2/mcp`，audience 精确比对只存在于 token 路径），故直连模式不会因 `audience` 写错而 401；token 路径的 audience 三要素对齐仍然照旧。

**401 `ADMIN_UNAUTHORIZED` 排障顺序**（直连模式）：该路径不区分内部原因，日志只显示鉴权失败，需按下表逐项核对。

| 常见原因 | 判别 / 处置 |
|---|---|
| 用了**已轮换掉的旧 secret** | 轮换批准后旧 secret 立即失效；重新申请轮换并向客户端换发新明文 |
| 客户端**已被吊销** | 管理台 `/mcp-clients` 查状态；需先走「启用」审批后方可再次使用 |
| secret **抄写不完整**（截断 / 掉字符 / 混入引号或换行） | 前缀仍可能对得上但哈希不匹配，症状与「secret 错」完全一样；对照客户端的 `secretPrefix` 前 12 位确认取的是哪一份明文 |
| 凭据**取自别的实例** | 多套 Beacon 共存时最易发生：secret 必须来自目标实例自己的库（该症状在换 token 路径同样表现为 401） |
| Host / 来源被门禁拦下 | 直连部署下仍需 Host 命中 `allowed-hosts`（见 §9.1）；此类 401 与凭据无关 |

## 10. 内部信任通道（机器注册）

单操作者内网部署下，外部管理平台（如 JianManager）批量创建实例后逐个走人工审批不可行（60 台 = 60 次审批）。`mcp.allow-machine-register` 提供一条**默认关闭**的内部信任通道：开启后，持 `X-Beacon-Token` 共享 token 的受信内部调用方经 `POST /beacon/v1/agent/data-plane/attach`（原名 `/beacon/v1/agent/register`，旧路径仍作兼容别名可用）提交的注册**直接创建 active 身份并绑定指定 serverId**，跳过人工审批。该通道**只覆盖注册**（创建未分配 server），区服分配、换区、默认入口仍各自走审批，不受本开关影响。

> 落点说明（端点更名后）：共享 token 的判定由 `agentTokenMiddleware` 完成，该中间件只挂在 `/beacon/v1/agent` 组；新端点 `data-plane/attach` **仍在该组内**（故机器注册不受更名影响）；v2 身份注册端点（`/beacon/v2/agent/register`）要求 namespace token 且不在该组内，行为不受本开关影响（仍落 pending 待人工确认）。

```yaml
mcp:
  # 允许受信内部调用方机器化注册 agent（跳过人工审批）。默认 false。
  # 仅内网单操作者部署可开启；公网部署必须保持 false。
  allow-machine-register: false
# 开启上面的开关时，本项必须换为强随机值（默认值 / 留空会导致启动校验失败）
agent-token: "<强随机值，或经环境变量 BEACON_BOOTSTRAP_TOKEN 注入>"
```

**安全警告（开启前必读）**

- 开启该开关即把共享 token 升级为**安全边界**：持有它等价于可批量注册 agent。故启动校验强制 `agent-token` 不为留空或已知默认值，否则**拒绝启动**。
- 共享 token 只在受信内网传输；公网部署必须保持开关关闭（默认即为关闭）。
- 该通道**只覆盖注册**（创建未分配 server）：区服分配、换区、默认入口仍各自走审批，不受本开关影响。
- 与 `mcp.allow-approval-decide` 相互独立：只开本开关 = 注册自动化；两者全开 = 内网端到端闭环（仅限单操作者内网）。

**审计与核查**：无论开关状态，机器注册意图都写 `identity.machine_registered`（操作者 `system:machine-register`，目标 `agent-identity/<identityId>`，detail 含 serverId、lastAddr 与调用来源 IP 及本次结果 `active`/`pending`）。开关关闭时记 `pending`（已提交待审批），开启时记 `active`（已直落）。

**回归自检**：开关关闭时行为与既有分权设计逐字一致——携共享 token 的注册仍落 pending 待人工确认；缺 / 错 token 一律 401（与开关无关）。
