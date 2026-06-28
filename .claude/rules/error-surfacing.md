# 错误展示规范（防错误被静默隐藏）

> 依据 [ADR-0057](../../docs/adr/0057-surface-desensitized-errors.md)。**操作出错必须让运维看得见——脱敏后展示到前端，绝不静默隐藏。**

## 1. 核心原则（强制）

- **不隐藏**：任何操作（尤其写操作）失败，真实原因必须能传达到前端，让运维知道「为什么没成功」。**禁止**把错误只塞进日志、对外回笼统「内部错误」之类无信息文案。
- **先脱敏再展示**：展示给前端的错误文案必须经脱敏，去除**凭据类机密**（DB 口令、proxy 账密、token / secret / api-key、Bearer/Basic 令牌、URL 里的 `user:pass@`）。
- **凭据才脱敏，上下文要留**：内网地址 / 主机名 / 文件路径 / 业务标识等**不是凭据**，是运维定位问题的关键上下文，**不打码**。机密边界只守在凭据上。

## 2. 后端（Go）

- 统一错误出口 `internal/render.WriteError`：领域错误（`*apperr.Error`）原样返回其安全 message；非预期内部错误**记完整日志（含 traceId）+ 对外返回 `redact.Desensitize(err)` 脱敏真因**（非固定「内部错误」）。
- 脱敏工具：`internal/redact.Desensitize`。**新增凭据形态时**扩规则并补单测。
- **治本在源头**：构造错误时不要把明文机密拼进错误信息；`Desensitize` 是最后兜底闸，不是免责符。
- 异步 / 后台操作（不经 HTTP 响应回错）：把失败原因（同样脱敏）落到可被前端轮询 / 查询的状态里（如进度态 `error` 字段、命令生命周期、审计），不让它无声消失。

## 3. 前端（React）

- API 错误（`ApiClientError.message`，已是后端脱敏文案）必须 toast 给用户：写操作 mutation 自带 `onError` 的照旧；未自带的由全局 `MutationCache.onError` 兜底 toast，**杜绝静默失败**。
- 禁止空 `catch` 吞掉操作错误而不给任何前端反馈（与全局反模式禁令一致）。

## 4. 与现有规则的关系

- 本规则是 [comments.md](comments.md) / [testing-and-quality.md](testing-and-quality.md) 中「异常处理不当（吞异常）」禁令的正向细化，并与 [SECURITY.md](../../SECURITY.md) 的「不泄露凭据」兼容——通过脱敏使「可观测」与「不泄密」并存。
