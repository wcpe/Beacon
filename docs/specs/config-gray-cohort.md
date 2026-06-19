# 功能规格：配置灰度 / Beta（cohort 名单）

> 状态：开发中　·　关联 PRD：FR-9（增强 FR-1/FR-3）　·　分支：feature/fr-9-config-gray

## 1. 背景与目标

P2 治理增强。某个 `dataId` 想发新内容，但不想一次推全网，先**只发给一小撮目标 server**（beta 队列 / cohort）观察无误，再**晋升（promote）为全量稳定版**，或**中止（abort）丢弃灰度**。落在既有 scope 覆盖链与版本机制之上，不另起一套发布体系。决策见 [ADR-0021](../adr/0021-config-gray-cohort-version-selection.md)。

## 2. 需求（要什么）

- 范围内：
  - 对某 config_item 发布一条灰度（指定灰度 content + cohort 名单），cohort 内 server 解析到灰度内容、名单外解析到稳定内容。
  - promote：灰度内容晋升为全量稳定版（version+1），灰度清空，cohort 内外全量切到新稳定版。
  - abort：丢弃灰度，cohort 成员回到稳定版本，稳定指针不动。
  - 灰度发布前过 FR-27 schema 校验；灰度项 sensitive 与所属配置项镜像，敏感走 FR-20 加密。
  - 操作均事务内写表 + 审计原子完成，提交后按受影响 serverId 唤醒推送（复用既有长轮询 / SSE）。
  - 列出某 namespace 下当前活跃灰度。
- 不做（范围外）：
  - 百分比 / hash 环放量、金丝雀引流（属流量调度 FR-10，ADR-0017 已划外）。
  - 自动晋升 / 超时自动回滚 / 发布编排（属 FR-12，P3）。
  - 灰度内容进版本历史（灰度是临时态，只有 promote 把内容并入 config_revision）。
  - "更新灰度内容 / cohort"原地改（当前不预留；要换先 abort 再重发）。

## 3. 设计（怎么做）

控制面（Go）侧改动，agent / 前端零改（解析结果对 agent 透明，agent 只看 md5）。

- **数据模型**：新增 `config_gray` 表（GORM AutoMigrate）。一个未软删灰度唯一对应一个 config_item（`config_item_id` 进唯一键 + 软删哨兵）。字段：灰度 `content` / `content_md5` / `format` / `sensitive`、cohort（serverId 列表，序列化落 `TEXT`）、operator / comment、软删 `deleted_at`。零方言绑定（VARCHAR / TEXT、软删哨兵、自增由 GORM 抽象）。
- **解析叠加**：`EffectiveService.Resolve` / `ResolveWithProvenance` 在拉四层候选后，按 config_item_id 一次性取这批项的活跃灰度（按 ns 一次查、Map 命中，无 N+1），对"存在灰度且当前 serverId 在 cohort 内"的候选项，把参与合并的 content 替换为灰度 content。其余层、merge 纯函数、md5 计算全不变——名单外 server 解析结果与无灰度时逐字节相同。
- **服务编排**：新增 `ConfigGrayService`：
  - `Publish(itemID, content, cohort, operator, comment)`：校验 cohort 非空、灰度内容过 `validateContent`，事务内 upsert `config_gray` + 写审计，提交后按 cohort 名单逐 serverId 唤醒。
  - `Promote(itemID, operator, comment)`：取活跃灰度，事务内走既有发布路径（appendRevision + 更新 item 指针，version+1）+ 软删灰度 + 写审计，提交后按 item scope + cohort 名单并集唤醒。
  - `Abort(itemID, operator, comment)`：软删活跃灰度 + 写审计，提交后按 cohort 名单唤醒。
  - `List(ns)`：列活跃灰度。
- **唤醒**：`ChangeNotifier` 增加 `NotifyServers(ns, ids)`（按名单逐 serverId 唤醒配置通道，复用既有 Hub）。
- **加密边界**：新增 `ConfigGrayRepository`，持 cipher，敏感灰度 content 落库前加密 / 读出解密，service 只见明文（与 config_item / config_revision 同构，FR-20）。
- **HTTP**：admin 端点挂在 `/admin/v1/configs/{id}/gray`：`POST` 发布灰度、`POST .../promote` 晋升、`DELETE` 中止、`GET /admin/v1/configs/gray?namespace=` 列活跃灰度。operator 由认证态派生。

## 4. 任务拆分

- [x] ADR-0021 + 本 spec
- [x] model `config_gray` + AutoMigrate 注册 + 枚举/审计动作/apperr
- [x] repository（含 cipher 加解密、upsert、按 ns 批量取活跃灰度）
- [x] EffectiveService 灰度叠加（Resolve + Provenance 一致）
- [x] ConfigGrayService（publish / promote / abort / list）+ Notifier.NotifyServers
- [x] handler + 路由 + 装配
- [x] 测试先行：穷举单测（cohort 内/外解析、promote、abort、md5/校验、唤醒命中）
- [x] 文档同步：PRD 状态保持开发中、ARCHITECTURE、API、CHANGELOG

## 5. 验收标准

- 对某 config_item 发布灰度 + cohort=[s1]：s1 解析到灰度内容，s2 解析到稳定内容（逐字节等于无灰度结果）。
- promote 后：cohort 内外都解析到（原灰度）新稳定内容，活跃灰度被清空，version+1，版本历史多一条。
- abort 后：cohort 成员回到稳定内容，稳定 version 不变，活跃灰度被清空。
- 灰度版本的 md5 = 灰度内容 md5；灰度内容非法（schema 不过）被拒。
- 灰度发布 / abort 只唤醒 cohort 内 serverId，名单外不被唤醒。

## 6. 风险 / 待定

- cohort 名单序列化：用 JSON 数组文本落 TEXT（可移植、可读），解析时反序列化为 set。空 cohort 视为非法参数（无意义灰度）。
- 一个 config_item 同时至多一个活跃灰度（唯一键）。重复 publish 同一 item = 覆盖（upsert 软删旧灰度再建新的，保持唯一）。
