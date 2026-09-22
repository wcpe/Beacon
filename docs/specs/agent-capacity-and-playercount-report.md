# 功能规格：Agent 上报容量与在线人数

> 状态：已实现（`capacity` 已接通；`conn` 语义已拍板：**保持 FR-32 母规格的 proxy 口径**，见 §7）　·　关联 PRD：FR-228（增强 FR-32 健康打分）　·　分支：feature/agent-capacity-and-playercount-report

## 1. 背景与目标

健康详情页对 `r1-z1-g1` 实际返回：

```
capacity  raw 0  normalized 0  weight 20  applicable:false
conn      raw 0  normalized 0  weight 10  applicable:false
```

即 **30% 的权重（20+10）永久不参与打分**——因为 agent 没上报 `capacity`（最大人数）与 `playerCount`（在线人数），因子被判「不适用」。这让健康分只反映 tps / cpu / latency，无法体现「这台是否快满员」，也使 FR-10 落位排序缺少真实的容量维度。

本 FR 让 **agent 上报 capacity / playerCount**，使这两个因子真正参与打分。属 P3。

## 2. 需求（要什么）

- agent 上报 `capacity`（服务器的最大玩家数，如 Paper `max-players`）与 `playerCount`（当前在线）。
- 控制面把二者喂给健康打分，使 `capacity` / `conn` 因子 `applicable:true` 并计入总分。
- **兼容降级**：旧 agent 未上报时，两因子维持 `applicable:false`（现状），不计分、不报错。

范围内：协议字段、agent 采集、控制面接收、健康因子接入。
不做（范围外）：按容量自动调度 / 自动扩缩容；历史容量趋势图（可归 FR-32 metric 既有采样）。

## 3. 设计（怎么做）

- **协议**：心跳（或指标上报）新增 `capacity`（int）与 `playerCount`（int）。**只增不改**，向后兼容。
- **agent 侧**：
  - `capacity`：优先取服务端配置（Paper `max-players`），取不到则留空（→ 不适用）。
  - `playerCount`：取当前在线玩家数（Bukkit `getOnlinePlayers().size()`）。
  - 二者需**同批上报**，避免只报一个导致口径不一。
- **控制面**：
  - 存储最近值（内存注册表，与既有健康真源一致）；**不额外落 metric**（复用既有采样，见 §7）。
  - 健康打分：`capacity` 因子按 FR-32 既有公式接入（占用率 `online/maxOnline`，`applicable = backend && 上限已上报`）。
  - `conn` 因子**维持 FR-32 母规格口径不变**（仅 proxy、`r=conn/connSoftLimit`），不改为 backend 饱和度——理由见 §7。
- **边界**：`capacity<=0`（未设上限）视为不适用；占用率 >1（过载，在线超过上限）归一化自然钳到 0。

## 4. UX / 交互

- 用户任务：在健康详情看到真实的容量 / 在线，并理解分数为何变化。
- 进入路径：`/servers` → 健康详情抽屉（factor 列表）。
- 操作闭环：打开详情 → `capacity` / `conn` 显示实际数值与权重 → 总分体现容量压力。
- 状态设计：未上报 → 明确标「未上报 / 不参与打分」（**替换当前误导性的"不适用 20"**）；过载 → 红色高亮 + 告警。
- IA 挂载：集群域 `/servers` 健康详情；不新增页面。

## 5. 任务拆分

- [x] 协议：心跳新增 `capacity` / `playerCount`
- [x] agent 采集（Paper max-players + online count）
- [x] 控制面接收 + 注册表存储
- [x] 健康打分接入 `capacity` 因子（占用率 `online/maxOnline`，`applicable = backend && 上限已上报`）
- [x] `conn` 语义**已拍板（2026-09-22）：保持 FR-32 母规格的 proxy 口径，不改公式**。原稿要求 backend 的 `conn` = 饱和度 `playerCount/capacity`，与母规格冲突且会导致重复计分，故不采纳；详见 §7。
- [x] 前端：健康详情区分「角色不适用」与「未上报 / 数据不可得」。实现方式：**纯前端按 `kind` + 因子名推导**（`health-factor-reason.ts`），不改契约、不改后端——控制面只下发 `applicable: bool` 不区分成因，而前端已持有 `kind`，足以反推（依据 FR-32 §4.4 适用矩阵 + 后端 `health_calc.go` 判定，后者已由 `TestFactorApplicabilityByKind` 固化）：tps/conn 仅单角色适用 → 另一角色为 `role`；capacity 在 backend 不适用只可能是 `missing`（maxOnline≤0 未上报）；latency/cpu 两者皆适用 → 不适用即 `missing`（数据不可得）。
- [x] 文档同步：PRD 状态、CHANGELOG

## 6. 验收标准

- 新版 agent 上报后，`GET /admin/v2/health/{serverId}` 的 `capacity` 为 `applicable:true` 且 `normalized` 随实际在线数变化。
- 旧 agent 未上报时 `capacity` 仍 `applicable:false`、总分口径不变。
- `capacity=0`（未设上限）判不适用；在线超过上限时归一化自然钳 0。
- `conn` 因子保持 FR-32 口径：proxy 适用、backend 不适用（`applicable:false`），改动前后不变。
- 总分权重和仍为 100；同角色内**同一输入**总分单调可预测。

## 7. 已确认默认（评审拍板）

- **`conn` 因子语义（2026-09-22 拍板：采纳「保持现状」）**：`conn` **维持 FR-32 母规格口径**——仅 proxy 适用，`r = conn / connSoftLimit`（复用 capGood/capBad 阈值，母规格原文即写「同 capacity 式」）。**原稿「`conn` = backend 饱和度 `playerCount / capacity`」不采纳**，理由：
  1. **与母规格冲突**：FR-32 §4.4 明确 `conn | proxy`、综合分口径写「backend 无 conn」。子规格不应反向推翻母规格；此处以母规格为准并回写本 spec。
  2. **会导致重复计分**：现行 `capacity` 因子公式 `(capBad − online/maxOnline)/(capBad − capGood)` **已经就是**「饱和度」。若再让 backend 的 `conn` 算 `playerCount/capacity`，两因子对同一实例会得出同一个数——正是原稿「一静一动、不重复计分」要避免的。
  3. **现状无重复计分且角色互斥**：`capacity` 判 `backend && …`、`conn` 判 `proxy && …`，两者**永不同时适用于同一实例**；「一静一动」的分工实际由 `capacity`（后端满度）与 `conn`（代理连接压力）承担。
  4. **改语义会波及调度排序**：给 backend 新增一个权重 10 的适用因子会把分母由 90 抬到 100，**同一输入的 backend 总分必变** → 改变 FR-10 落位排序；且需连带改 `normalizeSampleByKind`（现对 backend 的 conn 强制清 0）、新增配置项（会让历史权重 rev 反序列化出 0 值）。属超出本 FR 范围的破坏性改动。
- **是否落 metric**：**不额外落库**，复用既有采样。
- **跨端口径**：做 **adapter 归一**（不同服务端容量语义不同）。
- **与 FR-10 联动**：本 FR 只把 `capacity` 因子由「不适用」转为参与打分（改的是**同一实例的分数精度**，非改变因子定义），落位排序随之更真实；`conn` 定义不变，proxy 侧排序不受影响。
