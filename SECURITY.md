# 安全说明

> 本文说明 Beacon 的安全边界与约定。管理面鉴权已前移本批（见 [ADR-0009](docs/adr/0009-control-plane-auth-pulled-forward.md)）；配置加密仍属后期（FR-20）。本期数据面为内网部署模型。

## 信任模型
- **admin / 管理台**：需登录鉴权——单操作者凭据登录换无状态签名令牌，`/admin/v1/*`（登录端点除外）须带 `Authorization: Bearer <token>`，缺/错/过期返回 401；写操作 operator 以认证身份为准入审计。仍建议**不要把 admin 端口暴露到公网**。
- **agent ↔ 控制面**：共享 `X-Beacon-Token`（请求头 `X-Beacon-Token`），仅用于**防误连**，**不是安全边界**；缺失返回 401。
- 配置加密（敏感配置值落库加密）在 FR-20 引入。

## 密钥与敏感数据
- DB 密码、`BEACON_BOOTSTRAP_TOKEN`、管理台口令 `BEACON_ADMIN_PASSWORD` 与令牌签名密钥 `BEACON_AUTH_SECRET` 等**走环境变量**；仓库只放 `.env.example` 占位，`.env` 已被 `.gitignore` 排除。
- 禁止在代码 / 注释 / 日志 / 提交信息中硬编码任何凭据（见 `.claude/rules/` 与全局安全准则）。
- 日志不输出密码 / token / 完整凭据。

## 外部输入
- 配置内容由 Beacon 文本透传（不解析业务语义），但发布时做结构化 parse 校验，拒绝坏 yaml/json，避免坏配置推爆全网。
- agent 上报字段在使用前做基本校验（身份非空、序列化安全）。

## 漏洞报告

本项目为**内部项目**，不对外接收漏洞报告。发现安全问题请在内部 issue 跟踪，或直接联系项目负责人处理。
