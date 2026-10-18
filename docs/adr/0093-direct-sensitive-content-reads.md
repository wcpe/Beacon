# ADR-0093：敏感内容读取直执与强审计

**状态**：已接受（2026-10-18；部分取代 [ADR-0079](0079-principal-capability-and-dangerous-operation-approval.md) 与 FR-209 对指定读取操作必须审批的结论）

## 背景

FR-209 原将实时日志、文件内容和消息 payload 读取统一设为 `approval_required`，以限制持有管理凭据的自动化主体读取敏感明文。实际运维需要自动化读取受限范围的日志、目录与文件资产；本次决策接受安全代价：任何经管理面认证且具备 `management.read` 的主体（明确包括 API key）都可直接读取下列正文。持有相应凭据的自动化因此可能读取服务器上的凭据文件。

## 决策

1. 指定管理路由使用 operation `content.read`、capability `management.read`、分类 `direct`。路由中间件对该 operation 明确校验 capability；只读 API key 可调用这些 POST 读取端点，不因此获得其他写操作能力。
2. 以下内容读取直接执行，不创建审批申请，也不要求批准后签发或消费 grant：V2 文件资产预览与差异、其旧审批申请/授权消费兼容路径、消息 payload 及其旧申请/授权消费路径、V1 agent 日志读取与受限目录浏览及其结果消费路径。
3. 可撤销的文件同步任务启动与恢复使用 `file-sync.task.start` / `file-sync.task.resume`，capability 为 `management.direct`，分类 `direct`；继续由 `FileSyncService` 在状态转换事务内写专项审计。
4. 每次内容成功读取必须先完成 service 层专项审计，审计写失败时拒绝返回正文。审计仅记录主体、serverId/messageId、path、命令状态、哈希、字节数/行数等元数据；不得记录文件内容、日志行、payload 或 diff 片段。原因可作为敏感路径放行的输入，但不写入审计 detail。
5. 其余审批边界不变：配置敏感明文、反向抓取与拓印、重同步、身份、凭据、系统更新、高影响设置、拓扑迁移、归档/删除与交付审批仍按现有 operation 执行。特别是永久删除、凭据、系统更新、不可逆交付不随读取策略调整。

## 理由

- 将只读自动化限制在 `management.read` 能力，不改变主体能力授予、不放宽其他写操作。
- service 层强审计确保直接读取有可追踪证据，且内容不进入审计存储。
- 对跨服消息、文件资产、日志和目录浏览统一标记 `content.read`，避免旧申请/消费兼容路径继续隐式维持审批门。

## 后果

- 持有 `management.read` 的机器凭据可直接读取指定 endpoint 暴露的正文；操作者需将 API key/MCP 凭据视为可读取服务器内容的高敏凭据。
- 旧 `/approval-requests` 与 `/grants/{grantId}/consume` 路径仅作为 REST 兼容入口，不再签发或校验审批 grant；资源仍按请求参数或旧命令/配对记录定位，并重新走 direct 读取与审计。
- 新增 `content.read` 路由必须同时验证 direct 分类、`management.read` 校验和 service 审计覆盖；审计不得记录响应正文。
- 未列入本 ADR 的操作继续由 ADR-0079 与对应领域规格约束。
