# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.0.0/) 与[语义化版本](https://semver.org/lang/zh-CN/)。

## 未发布版本

### 新增
- 项目立项与第一期（MVP）设计定稿：确立"控制面（Go + 内嵌 React）/ 数据面（Bukkit/Bungee agent）"架构。
- 架构文档 [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)：控制面/数据面切分、MySQL+GORM 六表数据模型、scope 覆盖链合并、REST 长轮询热更机制、docker-compose 单节点部署。
- REST 契约 [docs/API.md](docs/API.md)：agent 侧与 admin 侧端点。
- 架构决策记录 [docs/adr/](docs/adr/)：自研而非用 Nacos、Go+内嵌 React 栈、MVP 去 Redis、zone 由控制面权威指派、agent 传输/序列化抽象层、REST 长轮询推送。
- 文档治理：PRD 入库为活文档（[docs/PRD.md](docs/PRD.md)）、新增演进与维护指南（[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md)）与文档同步规则（`.claude/rules/doc-sync.md`），确立"文档即代码、ADR 不可变只取代"的防漂移流程。

> 当前处于实现前（第一期 M0 待开工）阶段，尚无可运行产物与正式版本。
