# ADR-0004：zone 归属由控制面 DB 权威指派

**状态**：已接受

## 背景

旧方案里每台子服自己在配置文件写死 `zone-id`，各写各的，写错（重复 serverId、错 zone）会静默扩散并"爆炸"。需要一个健壮的身份与拓扑模型。

## 决策

采用**方案(b)**：

- **serverId** 是子服固有身份，由 agent 在本地 bootstrap 声明并上报（Beacon 自管身份，不依赖 CoreLib）。
- **zone 归属（serverId → group/zone）由 Beacon 的 DB `zone_assignment` 表权威指派**。agent 只报 serverId，从注册/拉取响应得知自己的 zone。换区 = 管理台改一行，agent 零改动、不重启。
- 守卫：重复 serverId 按 `lastHeartbeat` 新鲜度判（僵尸允许顶替、新鲜冲突拒绝）；身份缺失 fail-fast。

## 理由

- "哪台服属于哪个区"是拓扑分配（低频、要强一致、要审计），DB 权威是正解；逐节点配置文件易错且分散。
- 集中指派 → 改拓扑不必逐台改 config、不重启全网，且全网可见、可审计。
- 这就是"分小区"的正解：发现按 zone 标签过滤即可。

## 后果

- zone 解析依赖 DB（但控制面本就依赖 DB，非新增依赖）。
- 自管身份让 Beacon 彻底独立于 CoreLib/JVM 生态。

## 备选方案

- **方案(a) agent 本地声明 zone + 控制面校验**：简单，但 zone 仍分散在各节点、换区要改 agent。被否。
- **沿用配置文件写死 zone**：即旧方案的脆弱点。被否。
