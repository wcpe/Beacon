# 安全说明

> 本文说明 Beacon 的安全边界与约定。**建议不要把管理端口暴露到公网**：本期数据面按内网部署模型设计。

## 信任模型

- **admin / 管理台**：需登录鉴权——操作者凭据登录换取无状态签名令牌，`/admin/v1/*`（登录端点除外）须带 `Authorization: Bearer <token>`，缺 / 错 / 过期返回 `401`；写操作的 operator 以认证身份为准并入审计。
- **脚本化 admin API 密钥**：供外部服务 / 脚本调用 `/admin/v1` 的运行时密钥，认证头 `X-Beacon-Api-Key: <bk_...>` 或 `Authorization: Bearer <bk_...>`。**库内只存 SHA-256 哈希、绝不存明文**，明文仅在创建 / 重置时一次性返回、不可二次读取（丢失只能重置轮换）。可**吊销**（软删即时失效）、可**到期**（过期即 `401`）、`full` / `readonly` 两级角色（readonly 对写端点一律 `403`，最小权限）。创建 / 吊销 / 重置均写审计（明文 / 哈希绝不入 detail）。管理台「复制为 curl」辅助仅在浏览器内拼接命令，token 不落库、不入日志。
- **agent ↔ 控制面**：共享 `X-Beacon-Token`（请求头 `X-Beacon-Token`），仅用于**防误连**，**不是安全边界**；缺失返回 `401`。生产部署应通过网络层（内网隔离 / 防火墙 / 反向代理鉴权）保护管理面。
- **敏感配置值加密**：规划中，尚未实现。当下的缓解手段是把管理端口限制在内网。

## 密钥与敏感数据

- DB 密码、`BEACON_BOOTSTRAP_TOKEN`、管理台口令 `BEACON_ADMIN_PASSWORD` 与令牌签名密钥 `BEACON_AUTH_SECRET` 等**走环境变量**；仓库只放 `.env.example` 占位，`.env` 已被 `.gitignore` 排除。
- 禁止在代码 / 注释 / 日志 / 提交信息中硬编码任何凭据。
- 日志不输出密码 / token / 完整凭据。

## 外部输入

- 配置内容由 Beacon 文本透传（不解析业务语义），但发布时做结构化 parse 校验，拒绝坏 yaml / json，避免坏配置推爆全网。
- agent 上报字段在使用前做基本校验（身份非空、序列化安全）。

## 漏洞报告

发现安全漏洞请**不要公开开 Issue**，改为通过以下方式私下报告：

- 使用 GitHub 的 [私密漏洞报告](https://github.com/wcpe/Beacon/security/advisories/new)（Security → Report a vulnerability）

请尽量附上：影响版本、复现步骤、影响面评估，以及（如有）建议的修复方向。我们会在确认后尽快回复并协调修复与披露时间。
