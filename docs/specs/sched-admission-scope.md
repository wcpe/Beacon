# 功能规格：调度准入作用域（按节点自声明的标签收窄候选）

> 状态：开发中（代码已实现并通过本地验证门；待合并与真机验收）　·　关联 PRD：FR-244　·　分支：feature/sched-admission-scope　·　依赖：ADR-0087（决策）/ FR-243（声明写入面与只读真源）/ FR-227（标签上限）　·　纯后端（无 UX 段）

## 1. 背景与目标

调度决策目前只问"这台健不健康、能不能派"，**不问"这台是不是我要的那一类"**。接入方需要在请求候选时表达"我只要满足某些条件的节点"——例如只要某个玩法模式、某一档放量范围的服——而这些区分此前**没有任何可表达的入口**（`decide` 只认 `zone` / `purpose` / `plugin`）。

FR-243 已经给了节点一个**运行期刷新自己键值标签**的窄写入面，标签真源落在控制面运行期注册表里。本规格把这份真源**只读接入**调度准入判定：调用方在 `decide` 请求里给出**准入作用域**（若干备选，每个备选是一组"候选节点必须自己声明过"的键值），控制面在**生成候选的那一刻**按它收窄候选。语义归属、真源边界与"为什么不构成第二真源"由 [ADR-0087](../adr/0087-scheduling-admission-scope-over-self-declared-labels.md) 决策，本文只写**契约与判定行为**。

**目标**：让"我要哪一类节点"成为可表达、可判定、结论可区分的契约；调用方拿不到结果时能分清"确实没有"与"此刻判不了"。

## 2. 需求（要什么）

### 2.1 做什么

1. **准入作用域请求键**：`POST /beacon/v2/agent/schedule/decide` 新增可选请求键 `admissionScope`，形如 `[{k:v,...}, ...]`。
2. **判定语义（四条）**：**备选之间 OR、备选之内 AND、逐项精确相等**；比较**不做 trim、不做大小写折叠**（归一化只由**写声明的一方**做一次，读取侧不得再归一，否则同一事实会出现两种"相等"）。
3. **键必须是规整形态**：`key` 去掉首尾空白后必须与原文相等，否则 `400 INVALID_PARAM`（写入侧已保证规整，读取侧据此可做逐项精确相等）。
4. **上限**：备选数 ≤ 8、单个备选键数 ≤ 20（对齐 FR-227 的 `key ≤ 32` / `value ≤ 128` / 单节点 `≤ 20`）。
5. **无约束力即不收窄**：缺键 / `null` / 空数组**逐位等同旧行为**（不按标签收窄、不读真源、不报 503）；**无约束力的退化形态**（全部备选都是空对象，如 `[{}]`）同样按不限制处理。
6. **收窄结果可数**：响应新增可选键 `admissionExcludedCount`（`omitempty`，只在 `> 0` 时下发）= 因**准入作用域**被排除的台数（**健康原因不计**）。
7. **全部候选都被作用域滤掉时有专属失败码**：`failReason = no_candidate_in_scope`——这是**稳定事实**（换服 / 调服务范围才能改口）。
8. **判不了要明说**：新增 `503 admission_unavailable`——作用域非空但本进程**没有**自声明标签的读取真源时给出。这是**当前状态、可重试**；**绝不**退化成"忽略作用域照旧决策"，也**不**报成"没有候选"。
9. **排除原因可读**：决策明细 `excluded` 新增原因码 `admission_scope_mismatch`，且**作用域排除先于健康排除**判定、拼接在前。
10. **候选可带标签**：`GET /beacon/v2/agent/schedule/candidates` 每台候选新增可选键 `labels`（对象），三态见 §3.2。
11. **门面可表达作用域、可分辨三态**：agent 门面新增两条 `default` 重载、`AdmissionScope` 值对象、`ScheduleResult.state()`（三态）、`ScheduleResult.admissionExcludedCount()` 与 `CandidateView.labels()`；**既有签名一字未改**。
12. **降级路径同口径**：fail-static 路径（控制面不可达 / 快照降级）同样按三态给结论，不把"看不到"印成"确实没有"。
13. **FR-243 读回补齐一并入库**：`ServiceInstance.metadata()` / `capacity()` 暴露本进程可直接读回的声明值（本次与准入作用域同批入库，规格真源仍在 [agent-self-declaration-runtime-refresh](agent-self-declaration-runtime-refresh.md) §3.5）。

### 2.2 明确不做（范围外）

- **不进健康打分**：作用域只是候选门槛，不参与健康分与等级（健康仍是 FR-147 的既有真源）。
- **不参与候选排序**：候选之间的先后仍是既有 highest_score 契约；作用域只决定"在不在候选里"，不决定"谁排前面"。
- **不进 `?tag.*=` 过滤真源**：发现侧标签过滤的真源仍是控制面权威标签 `server_tag`（FR-227）；节点自声明与它是两个不同事实，本次**不合并、不同步**。
- **不新增第二真源**：决策读的就是 **FR-243 声明端点写进注册表的那一份**；本规格不新建表、不新增写入面、不做双写。
- **不解释 key 语义**：Beacon 不知道 `mode=xxx` 是什么意思（ADR-0086 §5 延续）；本规格也**不解释值结构**。
- **不碰 zone 权威**：作用域里不含"我在哪个区"；zone 仍由控制面 DB 权威指派（ADR-0004）。
- **不为无约束力形态报错**：`[{}]` 这类退化形态按"不限制"处理，**不**返回 400（理由见 §7）。

## 3. 设计（怎么做）

### 3.1 `POST /beacon/v2/agent/schedule/decide`（服务端）

请求（新增键 `admissionScope`，其余键不变）：

```json
{
  "zone": "string",
  "purpose": "string?",
  "plugin": "string?",
  "admissionScope": [ { "k": "v" }, { "k2": "v2" } ]
}
```

- `admissionScope` 可缺省 / 为 `null` / 为空数组 → 不收窄（逐位等同旧行为）。
- 每个备选是"必须**同时**满足"的键值集合；备选之间取**并集**（满足任一备选即准入）。
- 比较为**逐项精确相等**（字符串原样比较，不 trim、不折叠大小写）。
- `key` 去空白后须与原文相等，否则 `400 INVALID_PARAM`；备选数 > 8 或单个备选键数 > 20 同样 `400 INVALID_PARAM`。

响应（新增键 `admissionExcludedCount`）：

```json
{
  "traceId": "string",
  "chosen": { "serverId": "string", "score": 0 },
  "candidateCount": 0,
  "excludedCount": 0,
  "admissionExcludedCount": 0,
  "failReason": "no_candidate_in_scope"
}
```

> **逐台排除明细不在本响应里**：`decide` 只回 `excludedCount` 计数。逐台的 `{serverId, reason}`
> 落在**决策日表**，经 `GET /admin/v2/sched-decisions/{traceId}` 查询——该端点与本响应共用同一
> traceId。此处曾误画 `excluded` 数组，已按实现改正。

- `admissionExcludedCount` 带 `omitempty`：未被收窄时**不下发该键**（旧调用方逐位不变）。
- `failReason` 取值新增 `no_candidate_in_scope`，语义与触发条件：
  - **仅当本次全部候选都因作用域被排除**时给出（稳定事实：换服 / 调服务范围才能改口）；
  - "部分作用域排除 + 部分健康排除" 仍为 `no_candidate`（健康属**可恢复的当前状态**，不能印成稳定事实）。
- 新增 `503 admission_unavailable`：作用域非空但本进程没有自声明标签的读取真源——**判不了**，可重试；不得忽略作用域照旧决策，也不得报 `no_candidate*`。
- 判定顺序：**先准入作用域、后可调度性**；同一台同时命中两类原因时，**决策明细**（经 `sched-decisions/{traceId}` 查，非本响应）的 `excluded` 记 `admission_scope_mismatch`（作用域原因拼接在前）。

### 3.2 `GET /beacon/v2/agent/schedule/candidates`（服务端）

每台候选新增可选键 `labels`（对象，节点**自己声明**的键值标签），三态是契约的一部分：

| 状态 | 线上表现 | 调用方应读成 |
|---|---|---|
| 读真源已装配 | **总是下发** `labels`；节点没声明过即 `{}` | 稳定事实：这台就是"没有声明过标签" |
| 读真源未装配 | **整个键不发** | "看不到声明"，**不得**读成"没有声明" |

`labels` 是**版本信号**：缺键 = 对端（控制面 / 快照）不支持该字段。agent 门面据此把"判不了"与"确实没声明"分开（§3.3）。

### 3.3 agent 门面（`agent-api`，纯 Java 8）

- `BeaconScheduling` 新增两条 `default` 重载：
  - `acquireCandidate(zone, purpose, scope)`；
  - `candidatesInZone(zone, scope)`。
  - `scope == null` 或空作用域 → **委派既有重载**（旧行为逐位不变）；非空 → 缺省实现抛 `UnsupportedOperationException`（**绝不静默忽略作用域**——静默忽略会让调用方拿到"看起来成功但不符合作用域"的结果）。
  - `candidatesInZone(zone, scope)` 在"判据看不到"（候选快照缺 `labels` 字段）时抛 `IllegalStateException`（**当前状态，可重试**）。
  - 既有三个方法与两个既有构造器**签名一字未改**；本次以 `javap` **人工逐条比对**确认，仓内**暂无**自动化二进制兼容门禁。
- `AdmissionScope`（新值对象）：`empty()` / `of(key,value)` / `of(map)` / `anyOf(List)` / `anyOf(vararg)`；不可变（防御性拷贝 + 不可修改视图）；`isEmpty()` 的语义 = **无约束力**（空列表，或只含空备选）；`anyOf` 的退化形态一律归约为 `empty()`。
- `ScheduleState`（新枚举，三态）：`CHOSEN` / `NO_CANDIDATE`（稳定事实）/ `UNAVAILABLE`（当前状态、可重试）。
- `ScheduleResult` 新增 `state()` 与 `admissionExcludedCount()`；带 `state` 的构造器做一致性校验（`CHOSEN` ⟺ `chosen != null`）。
- **用户可见取值变更**：占位实现 `UnavailableScheduling` 与降级路径在"看不到"时由 `no_candidate` **更正为** `unavailable` + `UNAVAILABLE`——此前把"看不到"印成了"确实没有"。
- `CandidateView.labels()`：节点自声明的只读标签（不可变副本；空 map = 本帧没有可展示的标签）。
- `ServiceInstance.metadata()` / `capacity()`：本进程直接读回声明值（FR-243 读回补齐）。

### 3.4 真源接入（只读，不新增第二真源）

- 判定读的是 FR-243 声明端点写入运行期注册表的**那一份**（`registry` 的实例 `Metadata`），控制面不再存第二份。
- 「真源未装配」的判据是**进程内是否有该读取面**（如装配期只有降级实现 / 快照缺字段），不是"标签为空"；两者结论不同（503 vs 收窄为 0 台）。
- 与 `server_tag`（FR-227）**不做合并、不做同步**；`?tag.*=` 过滤仍只读 `server_tag`。

### 3.5 降级（fail-static）路径

- 无快照 → `unavailable`（`UNAVAILABLE`），**不**报"没有候选"。
- 快照缺 `labels` 字段且作用域非空 → `admission_scope_unavailable`（判不了，可重试）。
- **候选快照落盘保留 `labels`**：新格式快照恢复后仍可判作用域；**原本不带该字段的旧快照**恢复后仍是"看不到"（这正是版本信号要表达的语义，不得用空 map 冒充）。

### 3.6 判定顺序与原因码

一次候选生成期内，先判准入作用域、再判可调度性；作用域未通过者**不再**进入健康原因判定，其 `excluded.reason` 为 `admission_scope_mismatch` 并拼接在健康原因之前。原因码是**排除原因**（"为什么没选它"），与 `failReason`（"这一次为什么没成"）是两个字段，不混用。

## 4. UX / 交互

本次**不新增页面、不改导航、不引入新交互模式**：原因是既有服务分析页 / 健康面板 / 资产面板的原因码词表（`cluster.servers.schedReason`）需要新增一个词条 `admission_scope_mismatch`（中文「不满足准入作用域」），与新原因码并列。无空态 / 加载 / 大数据量的新设计。

## 5. 任务拆分

- [x] 服务端：`AdmissionScope` 解析与校验（语义四条 / 上限 / 键规整）+ 准入判定接入 `sched_decision_service` + 单测
- [x] 服务端：`admissionExcludedCount` / `no_candidate_in_scope` / `503 admission_unavailable` / `excluded.reason=admission_scope_mismatch` + handler 与集成测试
- [x] 服务端：`candidates` 的 `labels` 三态 + 快照保留 `labels`
- [x] `agent-api`：`AdmissionScope` / `ScheduleState` / `ScheduleResult.state()·admissionExcludedCount()` / `CandidateView.labels()` / `ServiceInstance.metadata()·capacity()` / `BeaconScheduling` 两条 `default` 重载
- [x] `agent-core`：三态口径更正（`UnavailableScheduling` 与降级路径）、`labels` 解析与"缺键 = 看不到"的区分 + 单测
- [x] 兼容性人工核验：`javap` 逐条比对既有方法 / 构造器签名未变
- [ ] 文档同步：PRD（FR-244 登记 + §6.2 验收 + FR-243 旧口径改写）、本规格、ADR-0087 与 ADR-0086 状态行、ADR 索引、`.claude/rules/architecture-invariants.md` §1、API、`v2-metrics-health-scheduling.md`（契约真源）、`agent-self-declaration-runtime-refresh.md` 状态行、i18n、CHANGELOG

## 6. 验收标准

1. **按作用域收窄生效**：给出作用域后，候选**只**含满足者；备选之间 OR、备选之内 AND、逐项精确相等（含"大小写 / 首尾空白不同即不相等"的反向用例）。
2. **全被滤掉 → `no_candidate_in_scope`**：全部候选都因作用域被排除时 `failReason` 为该值；**部分作用域 + 部分健康**仍为 `no_candidate`（两类分别断言）。
3. **退化形态等同缺键**：`null` / `[]` / `[{}]` 三种形态与"缺键"**逐位相同**——不收窄、不读真源、不报 503，且与旧行为逐字段一致。
4. **上限与键规整校验**：备选 > 8、单备选键 > 20、`key` 去空白后与原文不等、`key` / `value` 超 FR-227 长度 → `400 INVALID_PARAM`（逐项用例）。
5. **真源未装配 → 503 `admission_unavailable`**：作用域非空而进程内无读取真源时返回 503；**不得**忽略作用域继续决策，也**不得**回落成 `no_candidate`。
6. **标签随候选下发且缺键语义可区分**：真源已装配时 `labels` **总是**下发（没声明过 = `{}`）；未装配时**整个键不下发**；门面侧 `CandidateView.labels()` 与 `candidatesInZone(zone, scope)` 的 `IllegalStateException` 分别覆盖两态。
7. **排除原因与顺序**：`excluded.reason = admission_scope_mismatch` 出现在健康原因**之前**；`admissionExcludedCount` 只计作用域排除台数（健康排除不计）、为 0 时**不下发该键**。
8. **降级路径三态**：无快照 → `unavailable`；快照缺 `labels` 且作用域非空 → `admission_scope_unavailable`；带 `labels` 的新快照可正常判作用域；**旧格式快照恢复后仍是"看不到"**（不被空 map 冒充）。
9. **既有签名一字未改**：`javap` 逐条比对既有三个方法与两个构造器签名不变；`null` / 空作用域的两条 `default` 重载**委派既有重载**（旧 agent 行为逐字不变）；非空作用域的缺省实现抛 `UnsupportedOperationException`（不静默忽略）。
10. **不进健康打分与 `?tag.*=` 过滤**：同一批实例在加 / 不加作用域时健康分、等级与 `?tag.*=` 过滤结果**逐项不变**（负向断言）。
11. **不新增第二真源**：作用域判定读的就是声明端点写入的那一份；无新表、无新写入面、无 `server_tag` 双写。
12. **构建门**：`go build ./...` + `go test ./...` + `make lint` 全绿；agent 侧 `./gradlew ktlintCheck detekt test` 全绿。

## 7. 风险 / 待定

- **`[{}]` 退化形态的取舍**：把"全部备选都是空对象"判成**无约束力**（等同缺键）而非 `400`——空对象备选本身"恒真"，若按严格 AND 解读会让 `[{}]` 反而成为"全不满足"；且它的判定结果与"不收窄"完全一致，报错只会让调用方在无意义的分支上报错。代价是"空备选"这一形态无法被识别为调用方的笔误，只能靠文档与 §6.3 的等值断言守护。
- **快照落盘与旧格式兼容**：快照保留 `labels` 才能让降级期判作用域；旧格式快照恢复后必然"看不到"，此时**有作用域就报 `admission_scope_unavailable`**（宁可说判不了，也不放宽作用域）。升级后首次刷新快照前会一直处于该状态，需在运维文档与接入方预期里讲清。
- **reflection 接入方按 `failReason` 字符串分支的风险**：本仓有接入方以反射读结果、按 `failReason` 字面量分支的先例（`no_candidate` 曾承载"看不到"）。本次新增取值（`no_candidate_in_scope` / `admission_unavailable` / `admission_scope_unavailable`）与 `unavailable` 更正是**有意的取值变更**：既有分支若未同步，会把"判不了"当成"真的没有"。对策是门面同时给出 `state()` 三态，接入方按枚举分支即可，不依赖字符串；`javap` 比对只保证签名不变，保证不了调用方的分支逻辑——需在接入方文档点名。
- **待定：多帧 / 跨刷新的一致读**。作用域判定读的是**当下**注册表快照；标签可能在同一次决策的多次读之间变化（FR-243 允许运行期刷新）。本次不做快照固定（与既有健康视图同为"当下事实"），若接入方要求"一次决策内标签冻结"，另立 FR 评估。
