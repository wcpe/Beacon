# ADR-0038：运维设置 store + 热生效——热改项真源由 config.yml 移到 DB

**状态**：已接受

## 背景

控制面自身配置（`config.yaml`，[FR-25](../PRD.md)）当前是**启动期一次性读入**的单一真源：`internal/config` 加载（内置默认 → YAML → env 覆盖三层），各消费者在 `cmd/beacon/main.go` 装配时拿到的值传进去、之后**改任何一项都得重启控制面**。

但其中相当一部分是**纯运维调参**、与启动 / 安全无关、且天然可在运行期生效——健康 TTL / 亚健康 / 离线宽限 / 扫描间隔、指标采样开关 / 间隔 / 保留期、长轮询挂起上限、告警 webhook URL / 超时、日志级别。运维想把心跳 TTL 从 30s 调到 45s、或临时开 DEBUG 日志、或改告警 webhook，不该被迫重启整个控制面（重启 = 内存注册表清零、agent 短暂 DEGRADED、看板断点）。

另一部分是**启动期固定 / 安全边界**，绝不能运行期改、也绝不该登录后从 UI 拿到：监听地址 `http-addr`、数据库 `database.*`（连接池启动建立）、管理面鉴权 `auth.username/password/secret/token-ttl`（口令与签名密钥是安全边界）、`agent-token`（agent 共享令牌）、git 导出仓路径 / 远程。

FR-61 把**热改项的真源从 config.yml 移到 DB 设置 store**、加设置 API（full 角色 + 审计）、各消费者从 store 读并**热生效**；config.yml 降为**引导 + 首启 seed**；启动 / 安全项**仍留文件**。本 ADR **扩展 [FR-25「config.yml 即真源」]**——不再"config.yml 是唯一真源"，而是**分层真源**：启动 / 安全项真源仍是 config.yml + env，热改项真源是 DB store（config.yml 仅在首启 seed 它）。

## 决策

1. **新建单一 key-value 设置表 `setting`（不按域分多表）。** 字段 `key`(VARCHAR 唯一) / `value`(VARCHAR 存字符串化值) / `value_type`(int/bool/string，应用层反序列化提示) / `version`(乐观锁 CAS) / 时间戳。GORM 可移植：VARCHAR + 应用层校验、**不用 ENUM/JSON 列**、不绑方言（SQLite + MySQL 同构）。选 key-value 而非类型化分表：设置项是"一把零散运维旋钮"、增删频繁、单表 CRUD 最省；类型约束由应用层 `value_type` + 校验承担。

2. **热改项与启动 / 安全项分层；启动 / 安全项绝不进 store、绝不出现在设置 API / UI。**
   - **进 store（热改）**：`health.degraded-after-sec` / `ttl-sec` / `offline-grace-sec` / `scan-interval-sec`、`metric.enabled` / `sample-interval-sec` / `retention-hours`、`longpoll.max-hold-ms`、`alert.webhook-url` / `webhook-timeout-ms`、`log.level`。
   - **留文件（启动 / 安全）**：`http-addr`、`database.*`、`auth.username/password/secret/token-ttl-sec`、`agent-token`、`git-export.repo-path/remote-url`。口令 / 密钥 / agent-token 是**安全边界**——登录后 UI 拿不到、设置 API 不暴露、不进 store。设置 API 只认白名单内的热改 key，写非白名单 key 一律拒。

3. **热生效机制：消费者「按需从 store 读」+ 设置 service 内存缓存，不引事件总线。** 各消费者本就有循环 / ticker / 每请求点（健康扫描每轮、采样每轮、长轮询每请求、告警每分发），在这些**自然时点从设置 service 读最新值**即可热生效——无需观察者 / 事件总线 / 消息队列（守"简单优先 / 禁重型件"）。设置 service 持**进程内内存缓存**（启动加载全量 + 每次 `UpdateSetting` 即时刷新该 key + 短 TTL 兜底重载），消费者高频读走缓存、不每次打 DB。`scan-interval` / `sample-interval` 这类 ticker 周期：消费者每轮比对当前设置值，变了就重置 ticker。

4. **日志级别用「原子可变级别」的 slog handler 热生效。** 标准库 slog 设置后无运行期改级别接口；包一层持 `atomic` 级别的 handler，`UpdateSetting("log.level")` 即原子改之，后续日志按新级别过滤——不重建 logger、不每条日志读 DB。

5. **config.yml 首启 seed store、之后 store 为热改项真源。** 启动时：① 仍读 config.yml + env（启动 / 安全项照旧用）；② 对每个热改 key，**若 store 无该 key 则用 config.yml 的值 seed 进 store**（首启填充）、若 store 已有则**以 store 为准**（运维改过的不被 config.yml 覆盖）。即 config.yml 对热改项只是"出厂默认 / 首启种子"，运行真源是 store。env 覆盖只对启动 / 安全项有意义（热改项运行期由 store 定）。

6. **设置 API：读对 full / readonly 都开，写仅 full + 审计。** `GET /admin/v1/settings`（列全部热改项当前值 + 类型 + 是否启动项说明）对登录者只读可见；`PUT /admin/v1/settings/{key}`（改单项）是写方法、经 `readonlyWriteGuard` 只读角色 403、且每次改入审计 `settings.update`（detail 记 key + 新值，**不记任何密钥 / 口令**——它们本就不在 store）。设置值校验（范围 / 类型 / 枚举如 log.level）在 service 层，非法值拒。

## 理由

- **运维调参不该重启控制面**：热改项天然运行期可变，移进 store + 按需读即热生效，省掉"改个 TTL 重启全控制面"的代价（注册表清零 / agent 抖动）。
- **分层真源不破 FR-25 初衷**：FR-25 要的是"配置有单一权威、不散落"；本 ADR 把热改项的权威从 config.yml 挪到 store（仍单一），启动 / 安全项权威仍是文件——各项仍只有一个真源，没有"两处都能改互相打架"。
- **启动 / 安全项留文件是安全底线**：口令 / 签名密钥 / agent-token 进 DB / UI 等于把安全边界搬到登录后可改、可读，风险大；它们必须留在文件 + env、登录后不可见。
- **按需读 + 缓存而非事件总线**：消费者数量少、读点天然（循环 / 请求），加观察者 / 总线是过度工程（违反架构不变量"禁 MQ / 重型件"）；按需读 + 内存缓存最省、够用。
- **key-value 单表**：运维旋钮零散、会增删，单表 + 应用层类型最灵活、迁移最省（AutoMigrate 一张表）。

## 后果

- 多一张 `setting` 表 + `SettingRepository` + `SettingsService`（持内存缓存 + 校验 + 审计）+ `SettingsHandler`（GET 列 / PUT 改）。
- 各热改消费者（健康扫描器 / 指标采样器 / 长轮询 StreamService / 告警 webhook / 日志）从"启动时拿值"改为"运行期从设置 service 读"；改动集中、不动其业务逻辑，只换取值来源。
- `cmd/beacon/main.go` 装配：启动后 seed store（热改项缺则用 config.yml 值填）、把 settings service 注入各消费者。
- config.yml 里热改项**降为首启种子**：改 config.yml 的热改项**只在 store 还没该 key 时**生效（首启）；已 seed 后改 config.yml 不再影响运行值（要改走设置 API）。`config.example.yml` 注释须说明这点。
- 日志级别需包一层 atomic-level handler（`internal/pkg/log` 小改）。
- 设置变更全程审计、可追溯。

## 备选方案

- **按域分类型化多表（health_settings / alert_settings …）**：字段有类型约束，但表数多、加一个旋钮就动一张表 / 迁移，维护重。被否（key-value 单表更适合零散旋钮）。
- **观察者 / 事件总线推送设置变更**：实时但复杂、要管多消费者订阅 / 进程重启丢消息，且违反"禁 MQ / 重型件"。被否（按需读 + 缓存够用）。
- **热改项也能 env 覆盖运行值**：env 是启动期快照、运行期不变，拿它做"运行真源"语义拧巴；env 只管启动 / 安全项。被否（热改项真源是 store）。
- **把 auth / agent-token 也搬进 store 方便 UI 改**：把安全边界搬到登录后可改 / 可读，风险大、违背"敏感项走 env 不入库"。被否（安全项铁留文件）。
- **彻底废弃 config.yml、全量进 DB（含启动项）**：启动期还没连上 DB 就需要 http-addr / database.dsn，鸡生蛋；且 DB 连接本身要 dsn。被否（启动 / 安全项必须文件先行）。
