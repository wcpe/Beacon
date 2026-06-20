# 功能规格：agent 配置环境变量覆盖

> 状态：开发中　·　关联 PRD：FR-33　·　分支：master（小型增强，未单开分支）

## 1. 背景与目标

agent（数据面，TabooLib 插件）当前只能从 `config.yml` 读配置；控制面（Go）已支持 env 覆盖（`BEACON_*`，env 优先）。为支持容器化部署的 agent（用环境变量注入接入信息、不必改文件）并消除 E2E 里手写 `config.yml` 大字符串，给 agent 的配置读取加一层环境变量覆盖。属 P2 增强（增强 agent 配置加载，不改契约、不动控制面）。

## 2. 需求（要什么）

- agent 的每个**标量**与**列表**配置项都能被环境变量覆盖，env 优先于 `config.yml`。
- 环境变量名约定：`BEACON_AGENT_` + 配置点分路径转大写、`.` 与 `-` 均转 `_`。例：`identity.server-id` → `BEACON_AGENT_IDENTITY_SERVER_ID`。
- 范围内：`beacon.*` / `identity.*`（除 metadata）/ `timing.*` / `backoff.*` / `snapshot.*` / `file-tree.*` / `override.*` / `messaging.*` 下的标量与列表。
- 不做（范围外）：`identity.metadata` 动态键 map 的 env 覆盖（动态键 env 枚举本版不做，仍只读文件）；控制面侧不变；不引入新依赖。

## 3. 设计（怎么做）

- `agent-core` 新增 `EnvOverridingConfigReader(delegate: ConfigReader, env: (String) -> String?) : ConfigReader` 装饰器：
  - scalar（string/int/long/double/boolean）：env 有非空值即解析覆盖，解析失败回落 delegate（不崩）。
  - stringList：env 值按 `,` 分隔、trim、去空。
  - keys（动态键 map）：直接委托 delegate（metadata 不支持 env 覆盖）。
  - env 查找以函数注入，core 不依赖具体环境读取、便于单测。
- 两端壳 `BeaconAgentBukkit` / `BeaconAgentBungee` 构造 reader 时包一层：`EnvOverridingConfigReader(TabooLibConfigReader(config), System::getenv)`。
- E2E（`agent-e2e` / `agent-e2e-bungee`）：删除 `agentConfigYaml` 手写 YAML，改在 run-task 的 run 任务上 `environment(...)` 注入 E2E 专属字段（endpoints / namespace / server-id / address / bootstrap-token / command-whitelist），其余走出厂 `config.yml` 默认。
- 无新 ADR：配置加载增强，不推翻任何已接受决策；core 仍 TabooLib-free（env 用注入 lambda）。

## 4. 任务拆分

- [x] PRD 加 FR-33（状态：开发中）
- [x] `EnvOverridingConfigReader` 单测（覆盖 / 优先级 / 命名 / 列表 / 缺失回落 / 解析失败回落 / keys 委托）
- [x] `EnvOverridingConfigReader` 实现
- [x] 两端壳接线
- [x] E2E 改用 env 注入、删 `agentConfigYaml`
- [x] 验证：agent-core 单测绿 + E2E 三套件复跑绿
- [x] 文档同步：ARCHITECTURE（agent 配置机制）、`config.yml` 注释、CHANGELOG

## 5. 验收标准

- 单测：env 覆盖 string/int/long/double/boolean/stringList；env 缺失或空 → 文件值；env 解析失败 → 文件值；命名映射正确；`keys()` 始终委托文件。
- E2E：agent 经 env 注入接入控制面（不写 `config.yml`），`go test -tags=e2e ./test/e2e/{directory,override,metrics}` 三套件全绿（注册 / 热更 / 目录注入 / override / metrics 均成立）。
- 受影响组件测试绿：`./gradlew -p agent test`。

## 6. 风险 / 待定

- env 空串语义：本版「空串 = 未设置、回落文件」（无法用 env 把非空文件值改成空）；如需「显式置空」后续再议。
- `identity.metadata` map 的 env 覆盖：本版不做（见 §2 范围外）。
