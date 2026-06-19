# ADR-0021：配置灰度——按显式 serverId 名单（cohort）在"版本选择"层叠加，promote/abort 两态收口

**状态**：已接受

## 背景

第二期（P2）要做配置灰度 / Beta（FR-9，增强 FR-1/FR-3）：某个 `dataId` 想先把新内容**只发给一小撮目标 server** 观察无误，再**晋升（promote）为全量稳定版**，或**中止（abort）丢弃灰度**。

这里有几处必须先拍板的边界，否则极易把"流量调度 / 自动放量"的镀金漏进控制面，或破坏既有 scope 覆盖链与有效配置 md5 的不变量：

1. 灰度名单怎么界定——按**百分比 / 自动放量**还是按**显式 serverId 名单**？
2. 灰度版本与稳定版本**如何共存**、有效配置**如何按灰度名单解析**，会不会破坏 scope 覆盖链与 merge 纯函数？
3. promote / abort 的**精确语义**是什么？灰度内容怎样并入版本历史？
4. 灰度与既有发布前 schema 校验（FR-27）、敏感项 at-rest 加密（FR-20）如何对齐？

## 决策

1. **灰度按显式 serverId 名单（cohort），不做百分比 / 自动晋升 / 自动回滚。** 灰度发布时由管理员明确给出 cohort（一组 serverId）。名单内的 server 解析到灰度版本，名单外解析到当前稳定版本。**不引入比例放量、不引入定时 / 阈值自动 promote、不引入自动 rollback**——按比例引流 / 金丝雀是流量调度域的事（FR-10 已在 ADR-0017 把 canary 划在范围外），灰度只管"某 dataId 的版本选择"，不碰玩家连接、不感知活跃负载。

2. **灰度挂在"配置项（config_item）"上，作用在"版本选择"层，与 scope 覆盖链正交叠加。** 新增 `config_gray` 表：一个未软删灰度唯一对应一个 config_item（`config_item_id` 进唯一键）。它携带灰度 `content` / `content_md5` / `format` / `sensitive` 与 cohort 名单（serverId 列表，序列化落 `TEXT`）。**有效配置解析时**：四层候选照常按 scope 覆盖链（global←group←zone←server）深合并；唯一变化是——若某候选项存在未软删灰度、且当前解析的 serverId 在该灰度 cohort 内，则**把该候选项参与合并的 content 临时替换为灰度 content**（其余层、合并算法、md5 计算全不变）。即灰度作用在"这一格用哪个版本的内容"，不新增覆盖层、不改 merge 纯函数。

3. **promote = 把灰度内容作为新稳定版本发布（version+1）并清空灰度；abort = 直接软删灰度。** 二者均在**单事务**内写表 + 审计原子完成，**提交成功后**按受影响 serverId 唤醒推送（复用既有长轮询 / SSE 唤醒）：
   - **promote**：以灰度 content 追加一条 config_revision（version+1，沿用既有发布路径，过 FR-27 schema 校验、敏感项 FR-20 加密），更新 config_item 的 content/md5/version/current_revision 指针，软删该灰度。语义：灰度内容晋升为全量稳定版，cohort 内外此后都解析到它。
   - **abort**：仅软删该灰度，稳定版本指针纹丝不动。语义：丢弃灰度，cohort 成员回到稳定版本。

4. **灰度内容复用既有发布前校验与敏感加密边界，不另起一套。** 灰度发布前同样过 `validateContent`（格式 / 大小 / 可解析 / FR-27 schema）；灰度项的 `sensitive` 与所属 config_item 镜像，敏感则灰度 content 走 FR-20 的 at-rest 加密落 `config_gray.content`（仓库层加解密，service 只见明文，与 config_item / config_revision 同构）。

5. **唤醒只命中受影响 server，复用既有唤醒集合。** 灰度发布 / abort 影响的是 cohort 名单内的 serverId（它们的版本选择变了），故按 cohort 名单逐 serverId 唤醒（`NotifyServers`）；promote 影响 cohort 内（灰度→稳定，内容不变但灰度撤销）与稳定版变更波及的全 scope，故按该 config_item 的 scope 唤醒 + cohort 名单并集唤醒。绝不全量盲唤醒。

## 理由

- **显式 cohort + 无自动放量**：守住"控制面只存事实 + 给版本选择"的边界，把比例引流 / 自动编排留给后续 ADR，避免范围漂移与镀金（与 ADR-0017 一致地把 canary 划在外）。
- **作用在版本选择层、不新增覆盖层**：scope 覆盖链（ADR-0008）与 merge 纯函数零改，灰度只是"合并前换掉某格的内容来源"，对既有有效配置 md5 幂等不变量无侵入——名单外的 server 解析结果与没有灰度时**逐字节相同**。
- **promote 走既有发布路径**：晋升 = 一次普通发布，自动获得版本历史、可回滚、审计、schema 校验、敏感加密，无需为灰度造第二套发布机制。
- **灰度落 DB（GORM、VARCHAR 枚举 / TEXT、软删哨兵、零方言）**：灰度是跨控制面重启须存活、要审计的"决策事实"，与 config_item / server_drain 同源类别，可移植可切 Postgres。

## 后果

- 有效配置解析路径多一步"灰度叠加"：拉候选后按 config_item_id 一次性取这批项的活跃灰度（按 ns 一次查、Map 命中，**无 N+1**），对在 cohort 内的项替换 content。纯解析（admin 预览）与 agent 热路径共用同一叠加逻辑，保证一致。
- 一个 config_item 同时至多一个活跃灰度（唯一键约束）；要换灰度内容 / cohort 须先 abort 再重发，或后续按需扩展"更新灰度"（当前不预留空壳）。
- 灰度**不进版本历史**（它是临时态）；只有 promote 把内容并入 config_revision。abort 的灰度内容不留痕（审计记录其发生，内容不归档）——若将来要"灰度也留历史"再写新 ADR。
- 灰度 cohort 用 serverId 显式名单，与 zone 指派 / scope 无耦合：一个 cohort 可跨 zone / group（只要同 namespace、同 dataId 的覆盖链里有那一格候选）。

## 备选方案

- **按百分比 / hash 环灰度放量**：引入"比例从哪来、谁执行引流、如何保持稳定分桶"等流量调度问题，越界到 FR-10 / 玩家连接域。被否，留后续 ADR。
- **灰度做成新的 scope 覆盖层（如 server 之上加 gray 层）**：会污染 scope 覆盖链语义、改 merge 纯函数与 provenance 计算，且名单外 server 的解析也要绕过新层。被否——灰度是"版本选择"而非"覆盖维度"。
- **为灰度造独立的发布 / 历史 / 回滚机制**：与 config_revision 重复造轮子。被否，promote 复用既有发布路径即可。
- **灰度自动晋升 / 超时自动回滚**：定时编排属版本发布编排（FR-12，P3）。被否，本期只做手动 promote/abort。
