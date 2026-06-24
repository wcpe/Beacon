# ADR-0042：脚本化 admin API token 复用 FR-42 运行时 API 密钥，仅补「复制为 curl」自动化辅助（扩展 ADR-0026）

**状态**：已接受

## 背景

FR-90 的盲区描述：运维常走 API / curl 做自动化，但「缺可管理的脚本化 admin token + 缺 UI 自动化辅助」。诉求是「签发 / 吊销 / 到期一种 admin API token（区别 agent apikey），供脚本调 admin API，并对关键操作给『复制为 curl』」。

动手前盘既有鉴权面，发现 FR-90 描述的能力**绝大部分已由 FR-42（[ADR-0026](0026-runtime-api-keys-and-readonly-role.md)）落地**：

- **运行时 API 密钥**（`internal/model/api_key.go`、`internal/apikey/*`、`internal/service/api_key_service.go`、`internal/handler/api_key_handler.go`）：明文带 `bk_` 前缀、全熵随机、**库内只存 SHA-256 哈希**，明文仅创建 / 重置时一次性返回、不可二次读取。
- **签发 / 吊销 / 到期**：创建支持 `name + role + 可选 expiresAt`；吊销走软删（不可逆）；**到期**由 `expiresAt`（UTC）控制，过期即 `Verify` 返 401。
- **调 `/admin/v1`**：中间件 `adminAuthMiddleware` 接受 `X-Beacon-Api-Key: bk_...` 或 `Authorization: Bearer bk_...`，校验通过即注入身份 + 角色；`readonlyWriteGuard` 统一裁决「只读拒写」（readonly 角色访问写方法一律 403）。
- **角色 / 最小权限**：`full`（读写，等同操作者）/ `readonly`（只读）两级，落 `VARCHAR` + 应用层校验（DB 可移植）。
- **审计**：创建 / 吊销 / 重置写既有 `audit_log`（明文 / 哈希绝不入 detail）。
- **管理台**：`web/src/pages/ApiKeysPage.tsx` 已有「密钥管理」页——创建 / 列表（状态 active/expired/revoked）/ 重置 / 吊销（复用 FR-76 `DestructiveConfirmDialog` 二次确认）/ 一次性明文展示 + 复制。

至于「区别 agent apikey」：agent 侧用的是共享 `X-Beacon-Token`（仅防误连、非安全边界，见 [ADR-0009](0009-control-plane-auth-pulled-forward.md)），**本就不是可管理的密钥**；FR-16 是 agent-api SDK 接入包，与 admin token 无关。故 FR-42 的 `bk_` 密钥**结构上已经就是「区别于 agent 的、可管理的脚本化 admin token」**。

因此 FR-90 相对已交付的 FR-42，真正**尚缺的只有一项**：关键操作的**「复制为 curl」自动化辅助**——把刚签发的 token 拼成一条可直接粘贴运行的 curl 示例命令，降低运维上手 API 自动化的门槛。

## 决策

1. **不新增独立的 admin-token 类型**：脚本化 admin API token = **复用 FR-42 既有 `bk_` 运行时 API 密钥**，不另起一套并行凭据。它已满足 FR-90 全部安全要件：哈希存储（不存明文）、可吊销、可到期、角色 / 最小权限（readonly 默认、full 等同操作者）。
2. **FR-90 的实现增量仅为前端「复制为 curl」自动化辅助**：在一次性明文展示弹窗内，新增「复制为 curl」按钮，把明文 token 拼成一条带认证头、指向某只读 admin 端点的示例 curl 命令，一键复制到剪贴板。curl 命令构造为**纯函数**（`web/src/lib/curlCommand.ts`），可穷举单测；token 仅在浏览器内存于展示期内拼接、**不落任何持久化**。
3. **鉴权面边界不变**：不改 `api_key` 表、不改 `apikey` 生成 / 哈希、不改 `adminAuthMiddleware` / `readonlyWriteGuard` / `APIKeyService`。FR-90 是 FR-42 之上的**纯前端易用性增强**，零后端改动、零鉴权语义变化。
4. **与 agent 凭据、登录令牌的区分维持现状**（无新增）：
   - **admin API token（`bk_`，FR-42/FR-90）**：人创建、库存哈希、可吊销 / 到期、角色可选，给**外部服务 / 脚本**调 `/admin/v1`。
   - **登录令牌**（HMAC-SHA256、不落库、TTL，[ADR-0009](0009-control-plane-auth-pulled-forward.md)）：管理台人类操作者会话，恒 full 角色。
   - **agent 共享 `X-Beacon-Token`**：数据面内网防误连，非可管理密钥、非安全边界。

## 理由

- **改动最小、最不破坏既有鉴权边界**：新增并行 token 类型会把鉴权面裂成两套等价凭据系统（两份签发 / 哈希 / 校验 / 吊销 / 到期逻辑、两个管理页），是典型镀金，违反「简单优先」架构不变量与范围纪律（YAGNI）。FR-42 的 `bk_` 密钥已是脚本化 admin token，复用它零重复。
- **鉴权面稳妥**：不碰已审计验证过的哈希存储 / 校验 / 拒写裁决，把增量限制在前端纯展示辅助，避免在金贵的鉴权面引入回归风险。
- **「复制为 curl」放前端纯函数**：命令拼接无副作用、可单测；token 不出浏览器内存、不入库、不入日志，符合安全准则。

## 后果

- FR-90 落地后，PRD §4 FR-90 行标记已交付；其「脚本化 admin token」语义指向 FR-42 的 `bk_` 密钥，文档（PRD / API / SECURITY）明确二者同一物、FR-90 仅补 curl 辅助。
- 将来若真需要「按端点 scope / 细粒度 ACL / 自动轮换 / 速率限制 / 多租户」，仍按 ADR-0026 既定立场——**不在本期做**，到时按域新增并写新 ADR。
- 「复制为 curl」示例固定指向一只读端点（`GET /admin/v1/system/status`）作通用模板；它是上手样例，不穷举每个端点的命令（避免镀金），运维据样例改 URL / 方法即可。

## 被否的备选

- **新增独立 admin-token 实体 / 表 / 中间件**：与 FR-42 能力 100% 重叠，纯重复实现，裂解鉴权面、增加维护与回归面。否。
- **给 token 加按端点 scope / 细粒度权限**：超出 FR-90 范围，且 ADR-0026 已明确列为不做项。否。
- **「复制为 curl」逐端点全量生成**：镀金；运维只需一条可改的模板命令即可推广到所有端点。仅给一条只读样例。否。
