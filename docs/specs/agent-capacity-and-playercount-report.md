# 功能规格：Agent 上报容量与在线人数

> 状态：部分实现（agent 侧 capacity 采集已补；`conn` 因子语义待拍板，见下）　·　关联 PRD：FR-228（增强 FR-32 健康打分）　·　分支：feature/agent-capacity-and-playercount-report

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
  - 健康打分：`applicable = 值已上报`；两因子**一静一动、不重复计分**（见 §7）。
  - `capacity` 因子 = **静态规模分档**（这台能装多少）；`conn` 因子 = **饱和度** `playerCount / capacity`（越满越差）。
- **边界**：`capacity<=0`（未设上限）视为不适用；`playerCount>capacity`（过载）归一化钳到 0 并可触发告警。

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
- [ ] 健康打分接入两因子（含 applicable 判定与归一化公式）——`capacity` 已接通；**`conn` 语义待拍板**：本 spec §7 要求 backend 的 `conn` = 饱和度 `playerCount/capacity`，但现行 FR-32 实现把 `conn` 定义为**仅 proxy** 的连接数，子服恒不适用。改语义会改变落位排序（spec §7 亦提示需评估现网调度影响），故保持现状待决策。
- [ ] 前端：健康详情显示真实值 / 未上报态
- [ ] 文档同步：PRD 状态、API、ARCHITECTURE、CHANGELOG

## 6. 验收标准

- 新版 agent 上报后，`GET /admin/v2/health/{serverId}` 的 `capacity` / `conn` 为 `applicable:true` 且 `normalized` 随实际在线数变化。
- 旧 agent 未上报时两因子仍 `applicable:false`、总分口径不变。
- `capacity=0`（未设上限）判不适用；`playerCount>capacity` 归一化钳 0。
- 总分权重和仍为 100；改动前后**同一输入**总分单调可预测（穷举单测）。

## 7. 已确认默认（评审拍板）

- **归一化分工（不重复计分）**：`capacity` = **静态规模分档**（这台能装多少）；`conn` = **饱和度** `playerCount / capacity`（越满越差）——一静一动，不重复。
- **是否落 metric**：**不额外落库**，复用既有采样。
- **跨端口径**：做 **adapter 归一**（不同服务端容量语义不同）。
- **与 FR-10 联动**：打分变化会改变落位排序结果，实现时需评估对现网调度的影响。
