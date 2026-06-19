# 验证门与质量底线（防质量漂移）

> 项目特定的测试与质量要求。通用反模式禁令（上帝类、长方法、复制粘贴、魔法值、吞异常、资源泄露、循环依赖、静态可变单例等）仍然适用。

## 1. 验证门（权威判据）
- **每个变更必须过验证门才算完成**，判据以**入库真源**为准：① `docs/PRD.md` §6 验收标准对应项满足；② 本文 §2 高风险区相关测试通过；③ 受影响组件全部测试绿（控制面 `go test`、agent `gradle test`、前端 `build` + `pnpm test`）。
- `.tmp/实施计划.md` 的里程碑勾选**仅作开发期辅助**，不入库、不作权威判据（clone 后可能不存在）。
- **禁止**以注释、跳过、删除失败测试的方式让测试"通过"。
- 改功能代码前先跑相关测试确保通过；新增 / 改业务逻辑同步加测试。

### 1.1 测试分层（怎么分、在哪跑）
- **单元**：纯逻辑（尤其 `merge` 合并、`digest`）—— Go `testing` / Kotlin 测试，不连外部依赖，最快最多；前端组件与纯逻辑用 vitest + React Testing Library（jsdom，`cd web && pnpm test`）。
- **集成**：控制面 + 真实 MySQL（测试库 / 容器）跑配置发布/解析/长轮询；agent 对接 mock 或真实 beacon。集成用例带 `//go:build integration` 标记与单测隔离：`go test ./...` **不含**集成（`internal/service` / `internal/server` 显示 no test files 属正常），`go test -tags=integration ./...` + `BEACON_TEST_DSN` 才跑（运行方式见 `docs/OPERATIONS.md` §8）——避免集成被静默 skip 误判为"全绿"。
- **E2E**：跨平台纯 Go 入口 `go test -tags=e2e -timeout=30m ./test/e2e/{directory,override}`（默认 sqlite、无需 docker，可选 mysql），自管控制面 + 真实 agent，跑关键时序（首次接入、发布热更、目录注入、三方覆盖，见 ARCHITECTURE 时序与 PRD §6）；CI 见 `.github/workflows/e2e.yml`，运行细节见 `docs/OPERATIONS.md` §7。
- **何时跑**：单元 / 集成随每次改动与 CI；E2E 在发版前（`sdd-release-version` / `sdd-hotfix`）至少跑一遍。

## 2. 必测的高风险区
- **merge 覆盖链合并**：标量覆盖 / map 深合并 / list 整替 / null 删键 / 序列化 md5 幂等 —— 穷举单测。
- **长轮询并发**：先注册后算 md5、唤醒即重算、注册前发布不丢更新、超时返回 304。
- **重复 serverId 守卫**：故障换机（同 serverId 新 IP）不被误杀。
- **fail-static**：杀控制面后 agent 不崩、按本地快照继续、玩家可进服。
- **健康 TTL**：online → lost → offline 流转。

## 3. 质量底线（Beacon 侧重）
- 控制面分层 `router → handler → service → repository` 单向依赖；handler 不碰 GORM / 内存结构（防上帝类）。
- runtime 三锁（Registry / Hub / Health）独立不嵌套，DB IO 一律在锁外。
- `merge` 为无副作用纯函数。
- 发布 / 回滚 / 改派等多表写必须在 DB 事务内原子完成，**事务提交成功后**才触发长轮询唤醒。
- 中文分级日志（ERROR/WARN/INFO/DEBUG），不留 `println` / `fmt.Println` 等临时调试输出。
- 不硬编码凭据 / 端口 / 超时等，走配置或常量。
