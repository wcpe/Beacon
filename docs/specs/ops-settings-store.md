# 功能规格：运维设置 store + 热生效（FR-61）

> 状态：开发中　·　关联 PRD：FR-61（feat，扩展 FR-25）　·　ADR：[ADR-0038](../adr/0038-ops-settings-store-hot-reload.md)。后端；前端设置页是 FR-62。

## 1. 背景与目标
把控制面热改项的真源从 config.yml（启动期一次性读）移到 DB 设置 store，加设置 API（full 角色 + 审计），各消费者运行期从 store 读并热生效，免重启。启动 / 安全项仍留文件。详见 ADR-0038。

## 2. 需求
- 新 `setting` key-value 表 + 仓库 + 设置 service（内存缓存 + 校验 + 审计）。
- 热改项（白名单）：`health.degraded-after-sec`/`ttl-sec`/`offline-grace-sec`/`scan-interval-sec`、`metric.enabled`/`sample-interval-sec`/`retention-hours`、`longpoll.max-hold-ms`、`alert.webhook-url`/`webhook-timeout-ms`、`log.level`、`reverse-fetch.max-file-bytes`（反向抓取单文件上限，FR-58）。
  - `reverse-fetch.max-file-bytes` 热生效**不改 agent**：控制面收 scan 清单时**用该设置 + agent 上报的 size 重算 `overThreshold`**（不信 agent 的 overThreshold 标记），submit 的超阈值须确认门也读该设置。默认沿用 `MaxFileContentBytes`(1MB)。
- 各消费者改为从设置 service 读、热生效（健康扫描 / 指标采样 / 长轮询 / 告警 webhook / 日志级别）。
- config.yml 首启 seed store（缺 key 才填），之后 store 为热改项真源。
- 设置 API：`GET /admin/v1/settings`（只读可见全部热改项 + 类型 + 元信息）、`PUT /admin/v1/settings/{key}`（full 写，readonly 403，入审计 `settings.update`，校验非法值拒、白名单外 key 拒）。
- 启动 / 安全项绝不进 store / API / UI。

### 不做
- 设置页前端 → FR-62。startup/安全项任何 store/API 暴露。事件总线 / 观察者。

## 3. 设计
### 3.1 模型 `internal/model/setting.go`
`Setting{ID, Key(VARCHAR 128 唯一), Value(VARCHAR 1024), ValueType(VARCHAR 16: int/bool/string), Version(int 乐观锁), CreatedAt, UpdatedAt}`。AutoMigrate 注册。无软删（设置是固定白名单 upsert，不删）。

### 3.2 白名单 + 元数据（设置 service 内常量表）
每个热改 key 定义：key、value_type、默认值、校验（范围 / 枚举）、对应 config.yml 路径（首启 seed 用）、面向运维的中文说明。`log.level` 枚举校验 ERROR/WARN/INFO/DEBUG；秒 / 毫秒类校验正整数合理上下界；`metric.enabled` bool。

### 3.3 仓库 `internal/repository/setting_repo.go`
GetAll() / Get(key) / Upsert(key,value,valueType)（CAS version+1）。

### 3.4 设置 service `internal/service/settings_service.go`
- 持内存缓存 `map[key]value`（启动 GetAll 载入；RWMutex）。
- `GetInt/GetBool/GetString(key)`（走缓存，缺则默认）；类型化便捷取值供消费者用。
- `Update(key,value,operator,clientIP)`：白名单校验 + 类型 / 范围校验 → Upsert → 刷新缓存该 key → 审计 `settings.update`（detail `{key,value}`，绝不含密钥）→ 若 key==log.level 调日志原子级别 setter。
- `SeedFromConfig(cfg)`：对每个热改 key，store 无则用 config.yml 值 Upsert（首启种子）。
- `List()`：返回全部热改项当前值 + 类型 + 默认 + 说明 + isStartup=false（供前端 FR-62）。

### 3.5 消费者热生效改造
- `internal/runtime/health.go`：HealthScanner 持 settingsSvc，Run 每轮读 health.* 传给 SweepExpired；scan-interval 变则重置 ticker。
- `internal/service/metric_sampler.go`：每轮读 metric.enabled/sample-interval/retention；enabled=false 跳过采样；interval 变重置 ticker。
- `internal/service/stream_service.go`：长轮询挂起时读 longpoll.max-hold-ms。
- `internal/runtime/alert/*`：webhook alerter 每次分发读 url/timeout（url 空则不发）；或 dispatcher 据 url 动态启停 webhook 通道。
- `internal/pkg/log/log.go`：包 atomic-level handler，提供 `SetLevel(level)`；Setup 用它；settings service Update 时调。

### 3.6 handler + 路由
`internal/handler/settings_handler.go`：List（GET /admin/v1/settings）、Update（PUT /admin/v1/settings/{key}）。router 挂载（PUT 登记 FR-72 `coveredWriteRoutes`）。cmd/beacon/main.go 装配 settings service + SeedFromConfig + 注入消费者。

## 4. 任务拆分
- [ ] 模型 + 仓库 + AutoMigrate + 白名单元数据常量。
- [ ] settings service（缓存 + 类型取值 + Update 校验 / 审计 + SeedFromConfig + List）。
- [ ] atomic-level log handler 改造。
- [ ] 消费者改造（health / metric / longpoll / alert / log）从 store 读热生效。
- [ ] handler + 端点 + 路由 + FR-72 覆盖集 + main 装配。
- [ ] 测试（先行红）：Update 白名单 / 类型 / 范围校验 + 审计；缓存读 + 热刷新；SeedFromConfig 缺才填、已有不覆盖；各消费者读 store 值（health 阈值热改、metric enabled、longpoll、log level setter）；非热改 key / readonly 拒。
- [ ] doc-sync：PRD FR-61、API.md、ARCHITECTURE、CHANGELOG、本规格、config.example.yml 注释（热改项降为首启种子）。

## 5. 验收标准
- 改某热改项即热生效不重启（如改 health.ttl-sec → 下轮健康扫描用新值；改 log.level → 日志级别即变；改 metric.enabled=false → 停采样）。
- 启动 / 安全项不在设置 API / store，明示在文件。
- 改动入审计；非法值 / 白名单外 key / readonly 写被拒。
- config.yml 首启 seed、已 seed 后改 config.yml 热改项不影响运行值。
- 受影响组件测试全绿（go build/test/vet；集成跑设置改 + 消费验证）。
- **真机/集成**：改 health.ttl / log.level 即生效。

## 6. 风险 / 待定
- ticker 类（scan/sample interval）热改需重置 ticker，注意 goroutine 同步、不漏 tick。
- log.level atomic handler 不破坏既有日志格式 / 中文。
- 缓存一致性：单进程内存缓存 + Update 即刷新，够用（控制面单节点）。
