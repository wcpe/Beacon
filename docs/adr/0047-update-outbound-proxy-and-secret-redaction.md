# ADR-0047：控制面更新出站代理（单一 httpx 工厂）+ 含凭据设置项脱敏（扩展 ADR-0038）

**状态**：已接受

## 背景

控制面在线自更新（FR-97/FR-99）需从公网（`wcpe/Beacon` Releases / 资产下载）做**出站 HTTP**。部署环境常只有代理出网（企业内网 / 受限网络），故需让「更新相关出站」可经 HTTP/HTTPS 正向代理。两个新问题：

1. **出站客户端如何构造**：是否要像 agent 那样把 HTTP 客户端抽象成接口（[ADR-0005](0005-agent-transport-codec-abstraction.md)）？控制面与 agent 处境不同——agent 是插件、怕与 TabooLib / 其它插件的 OkHttp/kotlinx 版本冲突，故 core 对传输层做端口抽象；控制面是独立二进制、无此冲突，标准库 `net/http` 直接可用。这里唯一的真实变化点只是「要不要走代理」，不是「要不要换 HTTP 库」。

2. **代理地址可能含凭据**：代理形如 `http://user:pass@host:port`，是 FR-61 设置 store（[ADR-0038](0038-ops-settings-store-hot-reload.md)）里**第一个可能含凭据的热改项**。ADR-0038 决策 6 与 settings 审计实现都依赖一个前提：「白名单不含任何密钥 / 口令，故审计 detail 与日志可明文记 value」。`update.proxy-url` 打破该前提——userinfo 段是凭据，明文记审计 / 打日志 / 回前端都构成凭据泄露。

## 决策

1. **新增单一出站客户端小工厂 `internal/httpx`**，把「构造带代理与超时的 `*http.Client`」收口到一处：

   ```go
   func NewClient(proxyURL string, timeout time.Duration) (*http.Client, error)
   ```

   - `proxyURL` 非空 → 解析校验后 `&http.Transport{Proxy: http.ProxyURL(parsed)}`；空 → 直连（`Proxy` 为 nil）。
   - **只支持 http/https 正向代理**（标准库 `http.ProxyURL` 原生支持），**不引 socks5、不引任何新依赖、不读 `*_PROXY` 环境变量**（保持最小，YAGNI）。
   - 更新检查 / 下载（FR-97/FR-99）经此工厂出站。本工厂是**控制面侧唯一**构造「可配代理出站客户端」的入口。

2. **明确不照搬 [ADR-0005](0005-agent-transport-codec-abstraction.md) 的 transport / codec 接口抽象。** 那是 agent 侧约束（防插件依赖冲突 + 可替换具体库）；控制面只面对「代理」这一个真实变化点，标准库直接够用，加端口接口属过度工程（违反「简单优先」）。两套约束各自独立，不互相套用。

3. **代理作用域硬约束：仅作用于「更新相关出站」。** **不改 `internal/runtime/alert/webhook.go` 的既有行为**——webhook 告警维持现状裸连（`&http.Client{Timeout}`、无代理），向后兼容。本工厂只服务更新出站，不是全局出站收口。

4. **新增 Go URL 凭据脱敏纯函数 `httpx.RedactURLCredentials`**，精确掩 URL 的 userinfo 段：`http://user:pass@h` → `http://***:***@h`、`http://user@h` → `http://***@h`；无 userinfo 不改、空串返空串；URL 解析失败按「宁严勿松」整体返回 `***`（绝不回传可能含凭据的原串）。**三处全脱敏、落库存原值仅供运行：**
   - **审计**：`settings_service.go` 的 `settingAuditDetail` 对 `update.proxy-url` 走脱敏后再记。
   - **日志**：任何 slog 不打原值、只打脱敏值。
   - **前端回显**：`GET /settings` 对该项 value 回脱敏值；落库存原值供出站工厂运行。
   - **「未改密码」语义**：前端提交的 value 若仍是脱敏占位（等于当前值的脱敏形态），后端**保留原值不覆盖**（避免把脱敏占位当真值写库）；在 service 落实并注释。

5. **本 ADR 扩展 [ADR-0038](0038-ops-settings-store-hot-reload.md) 决策 6「store value 可明文记审计 / 日志」的前提。** ADR-0038 当时白名单确无凭据项故结论成立；`update.proxy-url` 引入后该前提不再普适——含凭据项须走脱敏。本扩展不改 ADR-0038 其余决策（分层真源 / 按需读 + 缓存 / 启动安全项留文件等照旧）。

## 理由

- **不引接口抽象**：控制面无 agent 的库冲突顾虑，单一小工厂应对「代理」这一变化点足矣，多一层端口接口是为不存在的需求镀金。
- **只 http/https 代理**：覆盖绝大多数企业出网场景；socks5 / 环境变量代理无明确需求，按 YAGNI 不做。
- **脱敏宁严勿松**：解析失败也不回传原串（可能恰好含凭据），整体掩成 `***`；只掩 userinfo 段、不动 host/port/path，便于运维核对代理指向。
- **落库存原值**：脱敏只用于「展示 / 审计 / 日志」三个对外面；运行期出站工厂需要真实凭据连代理，故 store 落原值。
- **作用域不含 webhook**：webhook 是既有行为，扩大其出站语义属范围漂移且破坏向后兼容；代理只服务新引入的更新出站。

## 后果

- 多一个极薄包 `internal/httpx`（`NewClient` + `RedactURLCredentials` 两个纯/近纯函数 + 穷举单测）。
- settings 白名单新增 `update.proxy-url`（string，归「网络代理」组，应用层校验 scheme∈{http,https} + host:port）；`config.yml` 增首启 seed 默认空（=直连）。
- settings 审计 detail / GET 回显 / Update 「未改占位保留原值」三处对该 key 特判，注释说明脱敏与保留语义。
- 本 FR 无更新消费者（FR-97 在后续批）：只交付工厂 + store 项 + 脱敏 + 单测，**不 wire 不存在的 updater、不动 webhook**。「经代理真连 GitHub」的真机维度待 FR-97 接入后验。

## 备选方案

- **照搬 ADR-0005 给控制面出站做 transport 接口抽象**：控制面无库冲突顾虑，加接口是过度工程。被否（单一小工厂够用）。
- **支持 socks5 / 读 `*_PROXY` 环境变量**：无明确需求，徒增复杂度与依赖。被否（YAGNI）。
- **把代理做成全局出站收口（含 webhook）**：改 webhook 既有裸连行为破坏向后兼容、扩大范围。被否（代理仅更新出站）。
- **脱敏失败时回传原串或报错**：回原串泄露凭据、报错使设置页不可读。被否（宁严勿松整体掩 `***`）。
- **proxy-url 不落原值、只存脱敏值**：运行期无法用真实凭据连代理。被否（落原值、仅对外脱敏）。
