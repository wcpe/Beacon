# 功能规格：控制面首启脚手架 + .env 环境变量加载

> 状态：开发中　·　关联 PRD：FR-25　·　分支：feature/control-plane-bootstrap-scaffold

## 1. 背景与目标

Beacon 控制面作为单二进制部署时，运维需手工准备 `config.yml` 并 `export` 一串 `BEACON_*` 环境变量（尤其鉴权口令/密钥）才能启动，门槛高、易错。本功能让二进制"开箱即跑"：首次启动自动把配置模板释放到当前目录、**释放时把留空的鉴权口令/密钥就地填入随机强值**、再正常启动。属 P1「运维友好 / 单节点部署」主题的部署体验增强，**不引入新依赖**。鉴权仍强制（[ADR-0009](../adr/0009-control-plane-auth-pulled-forward.md)）——只是把"缺凭据 fail-fast、等运维补"改为"首启自助生成强随机凭据、运维按需改"，凭据仍强随机、落在 `config.yml`（即真源）、不入库，未弱化安全。

## 2. 需求（要什么）

> 修订（首启 `.env` 覆盖 `config.yml` 修复）：原设计在释放 `config.yml` 之外还自动生成一份含重叠字段（`BEACON_HTTP_ADDR` / `BEACON_BOOTSTRAP_TOKEN` / `BEACON_ADMIN_USERNAME`）的 `.env`，因 `.env` 优先级高于 `config.yml`，会静默盖掉运维对 `config.yml` 的修改。现改为**不自动生成 `.env`**，把随机凭据**就地填入释放的 `config.yml`**；下文已反映此修订。

范围内：
- **首启脚手架**：启动时若 `-config` 指定路径（默认 `config.yml`）不存在，自动释放内置配置模板到该路径，**并把模板里留空的 `auth.password` / `auth.secret` 就地填入 `crypto/rand` 随机强值**（文件权限 0600，口令写入 `config.yml`、用户打开即见、可改，**不输出到日志**），使首启直接通过校验并启动、**不再 fail-fast**。**已存在则跳过，绝不覆盖用户文件。** `agent-token` 用模板里的固定默认 `beacon-bootstrap-token`——仅防误连、非安全边界（ADR-0009），固定默认让控制面与 agent 样例配置开箱即匹配、无需逐机同步随机值。
- **不自动生成 `.env`**：凭据落在 `config.yml`（即真源），不再额外写 `.env`——避免自动生成的 `.env` 因优先级更高而静默盖掉 `config.yml`。
- **.env 加载（保留）**：若当前目录存在**运维手动放置**的 `.env`，解析其 `KEY=VALUE` 注入进程环境变量，供既有 `applyEnv` 覆盖链消费。**真实环境变量优先**：仅填补未设置的键。
- **加载顺序**：内置默认 → `.env` 注入 env → yaml 文件 → 环境变量覆盖。生效优先级：真实 env > `.env` > `config.yml` > 内置默认。
- 释放的 config 模板默认 **sqlite**（本地零依赖、可直接跑），注释标明切 mysql 的方式。

不做（范围外）：
- 不引入 `godotenv` 等第三方库（手写最小解析 + `crypto/rand` 生成）。
- 不支持 `.env` 复杂语法：变量插值、多行值、行尾内联注释、`export` 之外的 shell 特性一律不解析。
- **不使用固定弱默认口令**（如 admin/admin）——生成的是强随机凭据，避免已知默认口令的安全风险。
- 不改 agent 侧配置加载。

## 3. 设计（怎么做）

控制面侧（根 `embed.go` + `internal/config` + `cmd/beacon`）：
- **根包内嵌模板**：`embed.go` 内嵌 `config.example.yml`（与 `web/dist` 同包），暴露为 `[]byte`，供首启释放。
- **`internal/config` 无状态函数**：
  - `EnsureConfigFile(path, template) (released bool, err error)`：`path` 不存在则把 `template` 释放为 `config.yml`、把留空的 `auth.password` / `auth.secret` 就地填入 `crypto/rand` 随机强值（`injectCredentials` 纯函数做字符串替换、保留注释；随机值经 base64url 仅含安全字符）、写入（0600）返回 `true`；已存在跳过返回 `false`。
  - `LoadDotEnv(path) error`：读 `.env`，跳过空行与 `#` 整行注释、按首个 `=` 切分、去引号 trim，仅对 `os.LookupEnv` 不存在的键 `os.Setenv`；文件不存在视为正常。
- **`cmd/beacon/main.run()` 接线**：在 `config.Load` 之前，先 `EnsureConfigFile` 释放含随机凭据的 `config.yml`（释放时 WARN 一句指向 `config.yml`、不带口令值），再 `LoadDotEnv(".env")`（仅当运维手动放置 `.env` 时生效）。
- **`config.example.yml` 修正**：补 `driver` 字段、默认 sqlite 可跑（修正 v0.3.0 引入 sqlite 默认后遗留的 MySQL-only 模板漂移）。

## 4. 任务拆分

- [x] PRD 增 FR-25 + 验收标准
- [x] 测试先行：`LoadDotEnv`（注入/真实 env 优先/缺文件/去引号/跳注释）、`EnsureFile`（释放/不覆盖）、`EnsureBootstrapEnv`（生成非空随机凭据/已存在不覆盖）
- [x] 实现：`embed.go` 内嵌、`LoadDotEnv`、`EnsureFile`、`EnsureBootstrapEnv`、`main` 接线、`config.example.yml` 修正
- [x] 文档同步：ARCHITECTURE 配置加载顺序、OPERATIONS 首启说明、CHANGELOG 未发布段

## 5. 验收标准

- 在空目录跑二进制：自动释放含**随机强凭据**的 `config.yml`（sqlite），**不生成 `.env`**，并**直接启动** HTTP 服务（sqlite 落 `beacon.db`），无需任何手工 `export` 或填值。
- 打开生成的 `config.yml` 取 `auth.password`，可登录管理台（HTTP 200）。生成口令**不出现在日志**。
- 已存在的 `config.yml` 不被覆盖（内容不变）。
- 改 `config.yml` 的 `http-addr` / `agent-token` 等项重启后**生效**（不再被自动生成的 `.env` 静默盖掉）；运维手动放置 `.env` 或设真实环境变量时，按 `真实 env > .env > config.yml` 覆盖。
- `internal/config` 新测试通过，`go build ./...` 与 `go test ./...` 全绿。

## 6. 风险 / 待定

- `.env` 解析仅支持最小语法；复杂场景仍可用真实环境变量。
- 释放的 config 默认 sqlite；生产 MySQL 仍走 env 或编辑 `config.yml`（注释已说明）。
- 生成的是强随机口令——首登需打开 `config.yml` 取口令（有意取舍：避免固定弱默认口令）。
