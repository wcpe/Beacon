# 功能规格：控制面首启脚手架 + .env 环境变量加载

> 状态：开发中　·　关联 PRD：FR-25　·　分支：feature/control-plane-bootstrap-scaffold

## 1. 背景与目标

Beacon 控制面作为单二进制部署时，运维需手工准备 `config.yml` 并 `export` 一串 `BEACON_*` 环境变量（尤其鉴权口令/密钥）才能启动，门槛高、易错。本功能让二进制"开箱即跑"：首次启动自动把配置模板释放到当前目录、自动生成含随机强鉴权凭据的 `.env` 并加载、再正常启动。属 P1「运维友好 / 单节点部署」主题的部署体验增强，**不引入新依赖**。鉴权仍强制（[ADR-0009](../adr/0009-control-plane-auth-pulled-forward.md)）——只是把"缺凭据 fail-fast、等运维补"改为"首启自助生成强随机凭据、运维按需改"，凭据仍强随机 + 经 env 注入、不入库，未弱化安全。

## 2. 需求（要什么）

范围内：
- **首启脚手架**：启动时若 `-config` 指定路径（默认 `config.yml`）不存在，自动释放内置配置模板到该路径。**已存在则跳过，绝不覆盖用户文件。**
- **首启生成 `.env`**：启动时若当前目录无 `.env`，自动生成一份 `.env`（权限 0600）使首启直接通过校验并启动、**不再 fail-fast**。其中 `BEACON_ADMIN_PASSWORD` / `BEACON_AUTH_SECRET` 用 `crypto/rand` 随机（口令写入 `.env`、用户打开即见、可改，**不输出到日志**）；`BEACON_BOOTSTRAP_TOKEN` 用**固定默认 `beacon-bootstrap-token`**——它仅防误连、非安全边界（ADR-0009），固定默认让控制面与 agent 样例配置开箱即匹配、无需逐机同步随机值。已存在 `.env` 则跳过、绝不覆盖。
- **.env 加载**：解析当前目录 `.env` 的 `KEY=VALUE` 注入进程环境变量，供既有 `applyEnv` 覆盖链消费。**真实环境变量优先**：仅填补未设置的键。
- **加载顺序**：内置默认 → `.env` 注入 env → yaml 文件 → 环境变量覆盖。生效优先级：真实 env > `.env` > yaml 文件 > 内置默认。
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
  - `EnsureFile(path, content) (released bool, err error)`：`path` 不存在则写入（0644）返回 `true`；已存在跳过返回 `false`。
  - `EnsureBootstrapEnv(path) (generated bool, err error)`：`path` 不存在则用 `crypto/rand` 生成管理员口令与签名密钥（agent 共享令牌用固定默认 `DefaultAgentToken`）、按模板写一份 `.env`（0600）返回 `true`；已存在跳过返回 `false`。
  - `LoadDotEnv(path) error`：读 `.env`，跳过空行与 `#` 整行注释、按首个 `=` 切分、去引号 trim，仅对 `os.LookupEnv` 不存在的键 `os.Setenv`；文件不存在视为正常。
- **`cmd/beacon/main.run()` 接线**：在 `config.Load` 之前，先 `EnsureFile` 释放 config 模板、`EnsureBootstrapEnv` 生成 `.env`（生成时 WARN 一句指向 `.env`、不带口令值），再 `LoadDotEnv(".env")`。
- **`config.example.yml` 修正**：补 `driver` 字段、默认 sqlite 可跑（修正 v0.3.0 引入 sqlite 默认后遗留的 MySQL-only 模板漂移）。

## 4. 任务拆分

- [x] PRD 增 FR-25 + 验收标准
- [x] 测试先行：`LoadDotEnv`（注入/真实 env 优先/缺文件/去引号/跳注释）、`EnsureFile`（释放/不覆盖）、`EnsureBootstrapEnv`（生成非空随机凭据/已存在不覆盖）
- [x] 实现：`embed.go` 内嵌、`LoadDotEnv`、`EnsureFile`、`EnsureBootstrapEnv`、`main` 接线、`config.example.yml` 修正
- [x] 文档同步：ARCHITECTURE 配置加载顺序、OPERATIONS 首启说明、CHANGELOG 未发布段

## 5. 验收标准

- 在空目录跑二进制：自动生成 `config.yml`（sqlite）与含**随机强凭据**的 `.env`，并**直接启动** HTTP 服务（sqlite 落 `beacon.db`），无需任何手工 `export` 或填值。
- 打开生成的 `.env` 取 `BEACON_ADMIN_PASSWORD`，可登录管理台（HTTP 200）。生成口令**不出现在日志**。
- 已存在的 `config.yml` / `.env` 不被覆盖（内容不变）。
- 真实环境变量与 `.env` 同名时以真实环境变量为准。
- `internal/config` 新测试通过，`go build ./...` 与 `go test ./...` 全绿。

## 6. 风险 / 待定

- `.env` 解析仅支持最小语法；复杂场景仍可用真实环境变量。
- 释放的 config 默认 sqlite；生产 MySQL 仍走 env 或编辑 `config.yml`（注释已说明）。
- 生成的是强随机口令——首登需打开 `.env` 取口令（有意取舍：避免固定弱默认口令）。
