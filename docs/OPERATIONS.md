# Beacon 运维手册

> 面向部署与运维 Beacon 控制面。前置：docker-compose（beacon + mysql）。架构见 [ARCHITECTURE.md](ARCHITECTURE.md)。

## 1. 部署
- 复制 `.env.example` → `.env`，填 MySQL 密码与 `BEACON_BOOTSTRAP_TOKEN`。
- `docker compose up -d`；待 mysql healthcheck 通过后 beacon 自动建表（GORM AutoMigrate）+ 预置 namespace（prod/test）。
- 管理台与 API 同端口（默认 8080）。

## 2. 升级
- **升级前先备份 MySQL**（见 §4）。
- 控制面：拉新镜像 → `docker compose up -d beacon`（mysql 数据卷不动）。AutoMigrate 只增不删；删列 / 改类型等复杂变更它不处理，需要时再引入迁移工具并另立 ADR。
- agent：替换 jar 重启子服。控制面与 agent 应同次发布、版本号一致（[ADR-0007](adr/0007-versioning-and-release-channels.md)）。

## 3. 健康与观测
- 健康探针：`GET /admin/v1/namespaces`（只读、无副作用）。
- 日志：beacon 容器内中文分级日志（ERROR/WARN/INFO/DEBUG）。
- 重点关注：实例失联告警、重复 serverId 告警、配置漂移告警。

## 4. MySQL 备份与恢复（关键）
> MySQL 是**配置权威库**——丢了等于全集群配置全没。务必定期备份。
- 备份：`docker exec beacon-mysql mysqldump -u root -p<密码> beacon > beacon-$(date +%F).sql`
- 恢复：`docker exec -i beacon-mysql mysql -u root -p<密码> beacon < beacon-backup.sql`
- 数据卷 `beacon-mysql-data` 持久化；迁移机器时连卷一起搬。
- **常态化**：建议 cron 每日 dump + 保留近 N 天 + 异机各存一份（别与 MySQL 同机）。
- **恢复演练**：上线前至少完整演练一次恢复（导出 → 空库导入 → 起 beacon 校验配置仍在），确认备份真能用。

## 5. 回滚
- **控制面版本回滚**：部署上一个稳定镜像 tag（见 GitHub Releases）。
- **业务配置回滚**：用管理台的配置版本回滚——这是 Beacon 自带能力，**不需重新部署**。
- **代码层回滚**：见 `rollback-change` 技能。

## 6. 排障
- beacon 起不来：看日志是否连不上 MySQL（DSN / 网络 / healthcheck 未过）。
- agent 连不上：核对控制面地址、`X-Beacon-Token`、网络连通。
- 配置不热更：看 agent 长轮询是否在连、控制面是否唤醒了受影响集合、有效配置 md5 是否真变。
- **控制面短暂不可用时不要重启子服**：agent 会按本地快照 fail-static 继续，控制面恢复后自动重连。
