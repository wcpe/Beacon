# ADR-0066：热冷数据归档采用独立 database 双连接 + 应用层搬运

**状态**：已接受（随 P6 FR-151/152/153 落地；登记 `docs/specs/v2-hot-cold-archive.md` 的架构级决策——引入第二个数据库连接）

## 背景

P4/P5 引入大流量时序日表（`metric_sample` / `health_snapshot` / `sched_decision` / `conn_detail` / `msg_trace` / `msg_payload`，均日期后缀分表）与 `audit` 单表，长期累积会拖垮热库容量与查询。P6 需把到期数据归档到独立库、支持显式冷查询、并提供「先归档校验后清理」的可控删除。

`docs/specs/v2-hot-cold-archive.md` 已详述完整机制（双连接搬运、幂等、cursor 断点、sha256 抽样校验门、应用层冷查询归并、进程内工作器、任务表状态机）。本 ADR 登记其中的**架构级决策**——控制面首次引入「第二个数据库连接」，并锁定其可移植与禁重型件约束。受 [architecture-invariants](../../.claude/rules/architecture-invariants.md) §2（禁 MQ/调度中间件/分布式一致性组件）与 §4（DB 可移植、禁方言专有）约束。

## 决策

1. **归档库 = 独立的第二个 `*gorm.DB` 连接**（非 ATTACH、非跨库 SQL）：`store.OpenArchive` 复用现有 `newDialector` 传入归档 DSN 建第二连接。**sqlite 归档 = 第二个 .db 文件路径；MySQL 归档 = 同实例第二个 database（默认 `beacon_archive`，部署方预建，Beacon 不执行 `CREATE DATABASE`）**。留空 `archive.dsn` = 同实例模式（复用主库连接参数仅替换库名 `archive.database`）；非空 = 独立库模式。`EnsureDailyTable` 已按 `db` 参数化，可直接对归档连接复用建表（同一套 model）。
2. **搬运在应用层完成**：热连接读、归档连接写，**禁跨库 JOIN / 跨库 `UNION` / `INSERT INTO ... SELECT` 跨库搬运 / `db.Table("beacon_archive.xxx")` 跨库表名限定**。幂等用 `clause.OnConflict{DoNothing: true}`（禁 `INSERT IGNORE`/`REPLACE INTO`）；批量删用「SELECT 主键批 + `DELETE WHERE id IN (...)`」（**禁 `DELETE ... LIMIT` MySQL 专有**）；整日表删用 Migrator `DropTable`。
3. **删除前置校验门**：每 item 走 `copying → verifying → deleting → done`；`verifying` 做行数校验 + sha256 抽样校验（确定性取样、列名字典序+行按主键升序规范序列化、小写 hex），**任一不一致则 item `failed`、绝不删除热库**（`verify_passed=true` 才进 deleting）。两库无分布式事务，靠幂等补偿（copy OnConflict、delete 按主键、cursor 单调）任意时刻崩溃重跑收敛一致终态。
4. **归档器 = 进程内单例后台工作器**（goroutine + ticker，**无外部调度组件**）：每日 `archive.schedule-hour-utc` 触发，单飞（同时至多一个 running 任务，撞车 409 / 自动任务跳过）。任务表 `archive_job` / `archive_job_item` 落**热库**（控制面事实，不随数据归档）。装配挂 main.go 后台 goroutine 区（随关停 ctx 优雅退出）。
5. **冷查询 = 应用层归并**：路由层对热连接与归档连接执行同构查询（同过滤/同 `ORDER BY 时间 DESC, id DESC`/同 limit），应用层有序归并取前 N，**主键去重保留热侧**（归档进行中两侧同存）；`includeArchived=true` query 参数挂各查询域端点、强制时间范围 ≤ `archive.cold-query-max-days`（默认 31，违反 400），归档库不可达时整体脱敏报错不静默返回仅热库部分。
6. **归档策略键落 v1 运维设置 KV store**（复用 [ADR-0038](0038-ops-settings-store-hot-reload.md)）：`archive.retention-days.*` 等策略键加进 `/admin/v1/settings` 白名单（≥7 天守卫、热更下一次任务/冷查询生效）；归档 **DSN 属启动配置**（`config.yml` + env 覆盖凭据、不入库明文，改需重启，不热更）。

## 理由

- **双连接绕开跨库可移植问题**：归档库就是「另一个 gorm.DB」，sqlite（第二文件）与 MySQL（第二 database）用同一层抽象统一，无需 ATTACH 或方言分支；搬运/查询全应用层，天然满足 §4 可移植。
- **进程内工作器守禁重型件**：无 Redis/MQ/外部调度器，与健康计算轮 / MetricSampler 清理 / FileSyncTask 任务台同款进程内范式。
- **幂等补偿替代分布式事务**：两库无 XA，靠 OnConflict + cursor + 校验门保证崩溃重跑收敛，简单可靠。
- **归档库不可达降级不阻断**：启动做连通性检查，不可达 → WARN + 归档能力标不可用，控制面照常启动。

## 后果

- `config.DatabaseConfig` 之外新增归档库配置（`archive.dsn` / `archive.database`）；控制面新增第二 DB 连接（生命周期随进程）。
- 冷查询有慢查询边界（≤ cold-query-max-days）；归档进行中冷查询需主键去重。
- 归档域端点 `/admin/v2/archive/*`、任务表、后台工作器全新增；冷查询 `includeArchived` 分散挂 P4/P5 各查询域端点。
- 本 ADR 不改各数据域表结构（属主规格权威），只按日期枚举表名 + 同 model 建归档表。

## 备选方案

- **单库 ATTACH（sqlite ATTACH / MySQL 无对应）**：方言不统一、破可移植。**否决**。
- **跨库 `UNION` / `INSERT ... SELECT` SQL 搬运**：需跨库表名限定、破 §4 可移植、sqlite 不支持。**否决**。
- **外部任务调度器 / MQ 驱动归档**：撞 §2 禁重型件。**否决**。
- **MySQL 分区表按时间分区**：撞架构不变量禁 `PARTITION`、且 sqlite 无对应。**否决**。
- **归档策略键另立 v2 设置端点**：设置页混 v1/v2 两套读写、多一套端点；加 v1 白名单与现有运行参数同页同端点最简。**否决**（采纳 v1 白名单）。
