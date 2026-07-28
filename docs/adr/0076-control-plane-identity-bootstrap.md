# ADR-0076：控制面身份分配、Bootstrap/Active Runtime 分层与绑定快照 fail-static

**状态**：已接受（部分取代 [ADR-0004](0004-zone-authority-control-plane.md) 决策中“agent 本地 bootstrap 声明 serverId”的结论，并部分取代 [ADR-0014](0014-downstream-identity-source-direction.md) 中“首次启动必须在线取得身份才放行”的结论；其余决定保持有效）

## 背景

现有身份模型把 `serverId` 作为 agent 本地 bootstrap 配置并上报。虽然 `zone` 已由控制面 DB 权威指派，但 namespace、serverId 与地址等业务字段仍会在每台 agent 本地维护，复制安装目录或误改配置会造成身份绑定漂移。

同时，ADR-0014 规定 agent 在场时必须等待控制面返回首份确定身份才放行启动。这能避免首次接入使用不完整身份，却把“没有任何已确认身份”的冷启动与“已有控制面确认身份、控制面暂时不可用”混为一谈，后者无法满足架构的 fail-static 边界。

FR-203 要求把本地人工配置收敛为 Beacon 控制面地址与 namespace 接入 token；新身份须由管理员分配 serverId，既有身份则在 identityId、平台角色和 token 解析出的 namespace 一致时自动沿用绑定。该变化必须明确身份真源、启动分层、旧配置兼容与断网行为，避免把 pending 身份误当成可运行身份。

## 决策

### 1. 身份业务字段由控制面权威分配

- 本地人工必填只保留 Beacon 控制面地址与 namespace 接入 token；token 解析出的 namespace 是注册的唯一 namespace 来源。
- agent 首启仍在本地生成并持久化不可人工配置的 identityId；平台壳层决定 `backend` 或 `proxy` 角色。
- 新 identityId 只会进入 pending，`serverId` 可为空；管理员确认时在同一控制面事务内分配 namespace 内唯一的 serverId、建立或复用 server 事实、转为 active 并写审计。
- `active`、`disabled`、`conflict` 绑定必须持有非空 serverId；`pending`、`rejected`、`expired`、`unbound` 可为空。数据面收到不满足该不变量的身份一律 fail-closed。
- agent 不得自行生成、选择或以本地旧值覆盖 serverId、namespace、区服归属、调度业务字段。旧本地 `serverId` 仅是兼容注册报文中的迁移提示，未知 identityId 携带它也仍须 pending 和人工确认。

这部分取代 ADR-0004 对“serverId 由本地 bootstrap 声明”的规定；ADR-0004 关于 zone 归属由控制面 DB 指派的决定继续有效且不受影响。

### 2. 运行时明确分为 Bootstrap 与 Active 两层

agent 启动先进入 Bootstrap Runtime，仅允许读取最小配置与 identityId、生成 bootId、识别平台角色、向控制面登记/观察身份状态、采集平台事实，以及读取或落盘绑定快照。

Bootstrap Runtime 不启动任何依赖权威 serverId 的指标、目录、调度、消息、文件、命令或交付循环。只有控制面返回带非空 namespace 与 serverId 的 active 绑定，或按决策 3 合法使用既有 active 绑定快照后，才构造并启动唯一一套 Active Runtime。状态转为 unbound、rejected、conflict 或绑定不匹配时，必须停止该数据面循环，禁止继续以旧 serverId 工作。

已有 active / disabled 身份在 identityId、平台角色与 token 解析出的 namespace 均匹配时，控制面直接沿用既有 serverId，不重新审批；本地旧 serverId 与控制面绑定不一致时拒绝该次接入，不以本地值修正控制面。

### 3. 绑定快照仅为已确认绑定提供 fail-static

agent 可在本机维护只读的绑定快照，保存最后一次控制面确认的 identityId、namespace、serverId、角色、确认时间、确认摘要与格式版本；快照不得包含 token，也不得由用户手工编辑。

- 没有有效快照的新 agent 在控制面不可用时，只能保持 Bootstrap/degraded，绝不启动 Active Runtime。
- 已有有效快照的 agent，在本机 identityId 与平台角色一致且 token 能解析到相同 namespace 时，可按快照启动受限的 fail-static Active Runtime，并持续后台重连和对账。
- 控制面确认 unbound、rejected、conflict、绑定不匹配，或 token namespace 与快照 namespace 不一致时，快照立即失效，数据面停止；快照不能复活已被控制面否决的绑定。
- 快照以同目录临时写入后原子替换；损坏或格式不兼容的快照视为无效，不自动修补、不作为身份依据。

因此，ADR-0014 的“agent 在场后首次必须在线取得身份才放行”改为：**无已确认绑定快照时必须在线确认；有有效 active 绑定快照时允许 fail-static**。ADR-0014 其余下游身份来源方向、agent 不在场才可由 CoreLib 本地降级、zone 未指派不得伪造等结论继续有效。

### 4. 保持 agent-core 与平台壳层边界

身份协议、状态机、快照校验与 Bootstrap/Active 编排属于 `agent-core` 的平台无关逻辑；identityId 存储位置、平台角色识别、监听事实采集与运行循环装配由 Bukkit/Bungee 平台壳层提供。`agent-core` 继续只依赖传输与序列化抽象，不直接依赖具体 HTTP 或 JSON 库；具体实现仍限于适配器，保持 [ADR-0005](0005-agent-transport-codec-abstraction.md) 的边界。

本决策不为身份流程另造并行传输通道，也不改变下游业务插件只能经本机 agent-api 消费已就绪身份的边界。

## 理由

- namespace 已可由 token 唯一推导，serverId 又需要人工确认；把二者作为本地持续配置会形成双真源，集中到控制面更可审计且避免漂移。
- pending 身份没有可安全运行的数据面身份。显式分层可确保无 serverId 不会以空字符串扩散到消息、调度或目录请求。
- 已确认身份的短时控制面故障是 fail-static 场景；没有确认事实的新安装不是。绑定快照区分两者，既不误放行新节点，也不无端阻断已接入服务器。
- 保持 core 的抽象端口与平台壳层职责，避免为了身份迁移把 HTTP 库或 Bukkit/Bungee 细节渗入平台无关核心。

## 后果

- 控制面身份数据需要允许 pending 等非活跃状态的 serverId 为空，并对活跃数据面入口强制非空不变量。
- 注册、审批、状态观察和管理台待确认流程需支持“待分配 serverId”；审批必须以控制面事务完成绑定、状态与审计。
- agent 的完整运行时不再在进程启动即装配；注册重试与状态变更必须保证不会并发启动两套 Active Runtime。
- 已部署 agent 需要保留旧配置键与旧注册报文的兼容读取窗口；只有控制面已有、且身份匹配的绑定才可自动迁移，未知身份不能凭旧 serverId 自动激活。
- 下游在 agent 存在时获得的是已确认 active 身份或合法 fail-static 身份；Bootstrap/pending 状态不应被当成可用身份。
- 快照是短时可用性缓存，不是独立真源；控制面恢复后的对账结果始终覆盖快照。

## 备选方案

- **继续让 agent 本地声明 serverId 与 namespace**：改动较少，但双真源持续存在，复制配置和人工修改会继续制造绑定漂移。否决。
- **新 identityId 携带旧 serverId 即自动 active**：迁移体验更短，但任何复制目录或伪造本地值都可能越过人工确认。否决。
- **所有启动都等待控制面在线确认**：首次接入安全，但已确认服务器在控制面短故障时不能按 fail-static 运行。否决。
- **所有离线启动都信任快照**：可用性最高，却会把无确认的新节点或已解绑节点误当作活跃身份。否决。
- **把 HTTP、JSON 或 Bukkit/Bungee 生命周期直接写入 agent-core**：实现表面直接，但破坏 ADR-0005 的可替换传输与平台边界。否决。
