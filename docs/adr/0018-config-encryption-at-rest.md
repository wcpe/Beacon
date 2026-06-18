# ADR-0018：敏感配置 at-rest 加密——AES-256-GCM、密钥走 env、密文落 TEXT 可移植

**状态**：已接受

## 背景

[ADR-0009](0009-control-plane-auth-pulled-forward.md) 拆分原 FR-11，把管理面鉴权前移、配置加密留作 FR-20（P3）。FR-26 要"经 Beacon 下发 Redis 密码"，下发链路必须先有"敏感凭据不明文入库"的闭环——否则 DB 文件 / 备份 / 误导出一泄露，凭据即外泄。

要拍板四件事，否则极易跑偏（引入重型件、破坏可移植、密钥入库）：

1. 用什么算法、谁来加？
2. 密钥从哪来、会不会进库 / 进仓 / 进日志？
3. 加密哪些数据、在哪一层加解密才不破坏 md5 / 有效配置解析？
4. 密文怎么落库才不绑死 MySQL（须能切 Postgres）？

## 决策

1. **算法 AES-256-GCM（Go 标准库 `crypto/aes` + `crypto/cipher`），不引第三方。** 认证加密（AEAD）天然防篡改：错误密钥或密文被改动时 `Open` 认证失败、返回错误而非脏明文。每次加密用随机 nonce，相同明文得不同密文。密文格式：`enc:v1:` 自描述前缀 + base64(nonce‖密文‖tag)，前缀便于识别"已加密"与未来算法演进。

2. **密钥只从环境变量 `BEACON_CONFIG_ENCRYPTION_KEY`（base64 的 32 字节）读取，绝不入库 / 不入仓 / 不打日志。** 与 `BEACON_AUTH_SECRET` 等敏感项一致走 env 注入；密钥不进 `Config` 结构、不落 yaml、不写审计 / 日志。**fail-fast**：库中已存在敏感配置项却未配置密钥 → 控制面拒绝启动，绝不以密文 / 乱码继续。

3. **加密范围 = 被标记 `sensitive` 的配置项整条 `content`（item 级），at-rest 边界落在 repository 层。** "标记敏感"机制最小可用：新建配置项时传 `sensitive: true`（不做按 key 加密——Redis 密码是整条配置项即可，避免镀金）。加解密只在 `ConfigItemRepository` / `ConfigRevisionRepository` 的写（加密）/ 读（解密）发生，**service 层始终只见明文**——md5、scope 覆盖链合并、发布前 schema 校验全部基于解密后明文计算，与非敏感项行为完全一致。

4. **数据面内网可信不变：解密后下发明文到 agent。** 有效配置解析（`FindEffectiveCandidates`）读出即解密，下发给 agent 的是明文；agent 不持密钥、零改动。这守住"数据面 trusted 内网"的既有前提（[ADR-0009](0009-control-plane-auth-pulled-forward.md) 后果段），加密只解决"落库不明文"。

5. **密文落既有 `content`（TEXT，base64 文本），新增 `sensitive` 落 `BOOL`，均经 GORM 抽象、零方言绑定。** 不用 MySQL 专有列 / SQL，满足"必须能切 Postgres"（[架构不变量 §4](../../.claude/rules/architecture-invariants.md)）。

## 理由

- **支撑 FR-26 的最小闭环**：Redis 密码作为敏感配置项加密入库、解密下发即可，不需要 KMS / 信封加密 / 轮换编排。
- **repository 层加解密**：是唯一既能"密文 at-rest"又"不污染 md5 / 合并 / 校验"的切点；放 service 层会让每个读写点都要记得加解密，放 model 钩子会把 model 耦合到 crypto。
- **AEAD 而非裸 AES-CBC**：免去单独的完整性校验，错密钥 / 篡改一律失败，安全默认值更稳。

## 后果

- 新增 `internal/secret` 叶子包（纯加解密原语，无外部依赖、不读 env、不打日志）。
- `ConfigItem` / `ConfigRevision` 各加 `Sensitive bool` 字段（AutoMigrate 自动补列）。
- 两个配置仓库构造签名加 `*secret.Cipher` 参数；`cmd/beacon` 从 env 建 cipher 并做 fail-fast 探测。
- `POST /admin/v1/configs` 增可选 `sensitive`；配置项视图增 `sensitive` 布尔标记（永不回吐密钥 / 密文）。
- **密钥轮换不在本期**（范围外，避免镀金）：换密钥需旧密钥批量解密 + 新密钥重写，留待按需新增，当前不预留空壳。
- **不取代任何既有 ADR**：本 ADR 落实 [ADR-0009](0009-control-plane-auth-pulled-forward.md) 后果段预留的 FR-20，二者互补。

## 备选方案

- **明文入库 + 仅靠 DB 文件系统加密**：依赖部署环境，备份 / 导出仍明文，且不可移植。否决。
- **信封加密（数据密钥 + 主密钥）/ KMS**：本期单密钥足够支撑 FR-26，信封 / KMS 是过度设计。否决（按需再起新 ADR）。
- **按 key 加密（只加密 YAML 里某个字段）**：复杂度高、要解析—改写—重序列化，Redis 密码场景整条加密即可。否决（如后续确有需求再扩展）。
- **密钥随配置文件入库**：违反"密钥不入库 / 不入仓"，等于没加密。否决。
