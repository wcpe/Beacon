# Beacon 运维手册

> 面向部署与运维 Beacon 控制面。前置：docker-compose（beacon + mysql）。架构见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 1. 部署
- 复制 `.env.example` → `.env`，填 MySQL 密码与 `BEACON_BOOTSTRAP_TOKEN`。
- `docker compose up -d`；待 mysql healthcheck 通过后 beacon 自动建表（GORM AutoMigrate）+ 预置 namespace（prod/test）。
- 管理台与 API 同端口（默认 8848）。

## 2. 升级
- **升级前先备份 MySQL**（见 §4）。
- 控制面：拉新镜像 → `docker compose up -d beacon`（mysql 数据卷不动）。AutoMigrate 只增不删；删列 / 改类型等复杂变更它不处理，需要时再引入迁移工具并另立 ADR。
- agent：替换 jar 重启子服。控制面与 agent 应同次发布、版本号一致（[ADR-0007](adr/0007-versioning-and-release-channels.md)）。

## 3. 健康与观测
- 健康探针：`GET /admin/v1/namespaces`（只读、无副作用）。
- 日志：beacon 容器内中文分级日志（ERROR/WARN/INFO/DEBUG）。
- 重点关注：实例失联告警、重复 serverId 告警、配置漂移告警。

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

前置：本机有 Go / JDK21 / Docker；起一次性 MySQL（避让本机已占端口，例 host 33306）：

```powershell
docker run -d --name beacon-e2e-mysql -e MYSQL_ROOT_PASSWORD=beacon -e MYSQL_DATABASE=beacon -p 33306:3306 mysql:8.0
go build -o .tmp/beacon-e2e.exe ./cmd/beacon
```

一键编排（Windows，全程自管控制面 + 真 Paper 生命周期，逐相位收口）：

```powershell
$env:E2E_ADMIN_PASS='<管理员口令>'; $env:E2E_AUTH_SECRET='<令牌签名密钥>'
pwsh ./test/e2e/override/run-override-e2e.ps1 -MysqlPort 33306 -McPort 25566
```

脚本依次跑四相位（任一 FAIL 即退出码非 0）：

- **inert（空白名单）**：覆盖集发布后文件被覆盖为新内容、但受限重载命令**一条不派发**（ADR-0011 默认 inert）。
- **filetree（FR-14）**：发布一个文件树文件 → agent 镜像落盘到插件真实数据目录 → 验收插件读到镜像内容。
- **ordering（放行白名单）**：验「备份原文件 → 原子覆盖 → 落盘成功后才派发命令」次序（命令收到时磁盘已是覆盖后内容），再回滚到无命令版本验「只还原事实、不重放命令」。
- **failstatic**：杀控制面后受管文件不动、命令不发。

> 前端（FR-18）管理台可在控制面起着时人工 / 浏览器自检：`http://localhost:8848` 登录（admin + `BEACON_ADMIN_PASSWORD`）→「文件树托管」看托管文件 →「文件覆盖集」详情看**发布前 dry-run 只读预览**（将覆盖哪些文件 / 执行什么命令 + 二次确认勾选门控发布）。

成员挂载当前无 admin API，驱动经数据层写 `file_object`（`override_set_id>0`）造成员——属已知缺口的临时绕过（见 CHANGELOG 已知项）。Linux 下可照脚本步骤手工等价执行（`go run -tags=e2e ./test/e2e/override -phase=<inert|ordering|failstatic>`，配 `E2E_DB_DSN`/`E2E_RUN_DIR`/`E2E_ADMIN_PASS` 等环境变量）。

## 8. 测试运行方式（单元 / 集成）

- **单元测试**（无外部依赖、快）：`go test ./...`。集成用例带 `//go:build integration` 标记、默认**不编译**，故此命令只跑纯逻辑单测——`internal/service` / `internal/server` 显示 `no test files` 属正常（其用例全为集成）。
- **集成测试**（需真实 MySQL）：先起测试库、设 DSN，再带 `integration` 标记跑：
  ```bash
  export BEACON_TEST_DSN='root:<密码>@tcp(127.0.0.1:3306)/beacon?charset=utf8mb4&parseTime=true&loc=UTC'
  go test -tags=integration ./... -count=1
  ```
  `internal/testsupport` 会在该实例上按 `beacon_<suffix>` 建独立测试库（不污染基础库）；未设 `BEACON_TEST_DSN` 时集成用例 `t.Skip`。
- **CI / 发版前**：两条都跑（`go test ./...` 与 `go test -tags=integration ./...`），E2E 另见 §7。务必确认集成是 PASS 而非 SKIP。
