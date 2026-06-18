# 功能规格：敏感配置 at-rest 加密

> 状态：开发中　·　关联 PRD：FR-20　·　分支：feature/fr-20-config-encrypt

## 1. 背景与目标

敏感配置项（如 Redis 密码、第三方 token）此前以**明文**落库（`config_item.content` / `config_revision.content`）。一旦数据库文件 / 备份 / 误导出泄露，明文凭据即外泄。

FR-20 给"被标记为敏感"的配置项做 **at-rest（落库）加密**：DB 里只存密文，解密只在控制面读取 / 下发时进行。本能力是 FR-26（经 Beacon 下发 Redis 密码）的前置——下发链路里必须先有"敏感配置不明文入库"的闭环。属 P3。

## 2. 需求（要什么）

- 范围内：
  - 标记为敏感的配置项，其 `content` **加密入库**；DB 列存 base64 文本密文。
  - 加密算法 **AES-256-GCM**（标准库），密钥从 **env** `BEACON_CONFIG_ENCRYPTION_KEY`（base64 的 32 字节）读取。
  - 密钥**绝不入库 / 不入仓 / 不打日志**。
  - 解密在控制面读取 / 下发（有效配置解析）时进行；下发到 agent 的是**明文**（数据面内网可信不变，agent 不需要密钥）。
  - "如何标记某项为敏感"：新建配置项时传 `sensitive: true`（item 级，最小可用）。
  - 有加密项却缺密钥 → **fail-fast**（启动报错，绝不静默以明文 / 乱码继续）。
  - md5 / 有效配置解析始终基于**明文**计算（解密后再算），与非敏感项行为一致。
- 不做（范围外，避免镀金）：
  - 不做 KMS / 外部密钥服务、不做自动密钥轮换编排、不做信封加密（envelope）。
  - 不做"按 key 加密"（只整 item 加密；Redis 密码是整条配置项即可）。
  - 不弱化 ADR-0009 的管理面鉴权；不改数据面 agent 共享 token 语义。

## 3. 设计（怎么做）

涉及架构决策，单列 **[ADR-0018](../adr/0018-config-encryption-at-rest.md)**（算法 / 密钥来源 / 加密范围 / 密钥不入库 / 与 FR-26 关系 / 可移植性）。此处只述模块改动，不重复决策正文。

- **新增 `internal/secret` 包**：纯加解密原语，无外部依赖。
  - `Cipher`：封装 AES-256-GCM。`NewCipher(keyB64)` 解析 base64 32 字节密钥；空串 → 返回"未配置"哨兵 cipher（不持密钥）。
  - `Encrypt(plaintext) (string, error)`：输出 `enc:v1:` 前缀 + base64(nonce‖密文‖tag)，自描述、便于识别密文。
  - `Decrypt(token) (string, error)`：校验前缀，GCM 认证失败（错密钥 / 篡改）→ 返回错误，绝不返回脏明文。
  - 未配置 cipher 被要求加 / 解密 → 返回 `ErrKeyMissing`。
- **数据模型**：`ConfigItem` / `ConfigRevision` 各加 `Sensitive bool`（落 `BOOL`，GORM 抽象，不绑方言）。密文落既有 `content`（TEXT，base64 文本，可移植）。
- **at-rest 边界落在 repository 层**（service 仍只见明文，md5 / merge / schema 校验零改）：
  - `ConfigItemRepository` / `ConfigRevisionRepository` 注入 `*secret.Cipher`。
  - 写（Create/Save）：`Sensitive` 为真则 `content` 加密后再落库。
  - 读（FindByID/FindByIdentity/List/FindEffectiveCandidates/版本查询）：`Sensitive` 为真则解密回明文再交给上层。
- **装配 / fail-fast**（`cmd/beacon`）：启动从 env 建 cipher；探测库中是否已存在 sensitive 项，存在却无密钥 → fail-fast 中文报错退出。
- **API**：`POST /admin/v1/configs` 增可选 `sensitive`（默认 false）；配置项视图增 `sensitive` 字段（只暴露布尔标记，永不回吐密钥 / 密文）。

## 4. 任务拆分

- [ ] `internal/secret`：Cipher + 往返 / 错密钥 / 篡改 / 无密钥 单测（红→绿）
- [ ] 模型加 `Sensitive` 字段；repository 注入 cipher，写加密 / 读解密
- [ ] service.Create 透传 sensitive；revision 镜像 item.Sensitive
- [ ] handler / API：create 入参 + 视图字段
- [ ] cmd/beacon 装配 cipher + fail-fast 探测
- [ ] 文档同步：ADR-0018、PRD（状态已预置开发中，不改）、ARCHITECTURE、API、CHANGELOG

## 5. 验收标准

- 加解密往返一致：明文 → Encrypt → Decrypt 得回原文。
- 错误密钥 / 篡改密文：Decrypt GCM 校验失败返回错误，不返回脏明文。
- 无密钥 fail-fast：有 sensitive 项却无 `BEACON_CONFIG_ENCRYPTION_KEY` 时启动 / 加解密报错。
- 非敏感项不加密：`Sensitive=false` 的 content 落库即明文（与现状一致）。
- 加密项 md5 / 有效配置解析正确：敏感项入库再读出，md5 与明文一致，有效配置合并结果与明文等价（解密后再算）。
- DB 里 sensitive 项的 `content` 为 `enc:v1:` 前缀密文（非明文）。

## 6. 风险 / 待定

- 密钥轮换不在本期：换密钥需用旧密钥批量解密 + 新密钥重写，留待 FR-26 之后按需新增，不预留空壳。
- 仅 item 级加密：若后续需"同一文件里只加密某 key"，再按需扩展，当前不做。
