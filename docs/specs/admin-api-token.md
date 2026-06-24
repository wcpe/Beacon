# 功能规格：admin API token 管理（复用 FR-42 + 复制为 curl）

> 状态：开发中　·　关联 PRD：FR-90　·　分支：feature/fr-90-admin-token

## 1. 背景与目标

运维常走 API / curl 做自动化，需要一种**可管理的脚本化 admin token**（签发 / 吊销 / 到期、区别 agent 凭据）调 `/admin/v1`，并希望对关键操作有「复制为 curl」上手辅助。属 P2。

盘既有鉴权面发现：FR-42（[ADR-0026](../adr/0026-runtime-api-keys-and-readonly-role.md)）的运行时 API 密钥（`bk_` 前缀）**已是**这种脚本化 admin token——库存哈希、可吊销、可到期、full/readonly 角色、可调 `/admin/v1`、创建 / 吊销 / 重置入审计、管理台已有「密钥管理」页。故 FR-90 真正尚缺的只有**「复制为 curl」自动化辅助**。决策见 [ADR-0042](../adr/0042-admin-api-token.md)。

## 2. 需求（要什么）

- 范围内：
  - 在密钥一次性明文展示弹窗内，新增**「复制为 curl」**按钮：把刚签发 / 重置的明文 token 拼成一条可直接粘贴运行的 curl 命令（带认证头、指向一只读 admin 端点），一键复制到剪贴板。
  - curl 命令构造为**纯函数**，可穷举单测（含特殊字符安全引用）。
  - 文档明确：脚本化 admin token = FR-42 `bk_` 密钥（签发 / 吊销 / 到期 / 角色 / 调 `/admin/v1` 全在 FR-42）。
- 不做（范围外，守 scope-discipline / YAGNI）：
  - 不新增独立 admin-token 类型 / 表 / 中间件（复用 FR-42）。
  - 不做 OAuth/SSO、细粒度 / 按端点 scope ACL、自动轮换、速率限制、多租户（ADR-0026 已列不做项）。
  - 不逐端点全量生成 curl，仅给一条只读样例模板。

## 3. 设计（怎么做）

- **后端**：零改动。鉴权面（`api_key` 表 / `apikey` 哈希 / `adminAuthMiddleware` / `readonlyWriteGuard` / `APIKeyService`）维持 FR-42 现状。
- **前端**：
  - 新增 `web/src/lib/curlCommand.ts`：纯函数 `buildApiKeyCurl(key, opts?)`，按 token 拼 `curl -H 'X-Beacon-Api-Key: <key>' '<base>/system/status'`，对单引号做 shell 安全转义。base 默认取当前站点（`window.location.origin` + `/admin/v1`），样例端点 `GET /system/status`（只读、任何角色可调）。
  - `ApiKeysPage.tsx` 一次性明文弹窗：在「复制明文」旁加「复制为 curl」按钮，调 `buildApiKeyCurl` 后复制。
  - i18n：`apikeys.copyCurlBtn` 等中文文案。

## 4. 任务拆分

- [x] 写 ADR-0042 + 索引；写本规格
- [x] PRD §4 FR-90 行「计划」→「开发中」
- [ ] 前端纯函数 `buildApiKeyCurl` + 单测（红→绿）
- [ ] ApiKeysPage 接入「复制为 curl」按钮 + i18n
- [ ] 文档同步：API.md（FR-90 指向 FR-42 + curl 辅助说明）、SECURITY.md、CHANGELOG

## 5. 验收标准

- `buildApiKeyCurl` 单测覆盖：普通 token 生成正确命令；含特殊字符 token 被安全引用；命令含认证头与只读端点。
- `cd web && pnpm test` + `pnpm build` 绿。
- 后端无改动；`go build ./... && go test ./...` 维持绿。
- 真机浏览器：弹窗内「复制为 curl」可复制出可运行命令（待真机浏览器验）。

## 6. 风险 / 待定

- curl 命令的 base host：浏览器内用 `window.location.origin`，与用户实际反代域名一致；样例仅作模板，运维可改。
- 无（鉴权面零改动，回归风险低）。
