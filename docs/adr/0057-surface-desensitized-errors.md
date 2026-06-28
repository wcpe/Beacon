# ADR-0057：操作错误脱敏后展示前端（反转「一律藏内部错误」）

- 状态：已接受
- 日期：2026-06-28
- 关联：FR-122；[.claude/rules/error-surfacing.md](../../.claude/rules/error-surfacing.md)；[SECURITY.md](../../SECURITY.md)

## 背景

控制面统一错误出口 `internal/render.WriteError` 此前的行为：

- 若错误是 `*apperr.Error`（领域错误，带业务码与中文说明）→ 原样返回 `{code, message, traceId}`。
- 否则（任何非预期错误）→ **只记服务端日志**，对外**一律返回通用 `{code:"INTERNAL", message:"内部错误"}`**。

这是一种「不泄露内部细节」的保守姿态，但在单节点、单运维、内网可信的部署形态下产生了实际危害：**真实失败原因被静默吞掉**。例如在线更新下载失败（`下载资产失败: ... context canceled`）只进日志，前端只看到「内部错误」，运维不看服务器日志就无从知道「为什么更新没成功」，问题被隐藏、拖大。

## 决策

**反转姿态：操作出错时，把脱敏后的真实原因展示到前端，让运维看得见；不静默隐藏。**

1. 新增叶子包 `internal/redact`，提供 `Desensitize(s string) string`：把文本中的**凭据类敏感片段**打码（best-effort）——URL 里的 `user:pass@` 密码段、`token=`/`password=`/`secret=`/`pwd=`/`api-key=`/`authorization` 等键值、`Bearer`/`Basic` 令牌。
2. `render.WriteError` 对非 `apperr.Error` 的内部错误：**仍记完整日志**（含未脱敏全文 + traceId 供排查），但对外返回 `{code:"INTERNAL", message: Desensitize(err), traceId}`——即脱敏后的真实原因，而非笼统「内部错误」。`apperr.Error` 分支不变（其 message 本就是面向调用方的安全文案）。
3. 前端 `ApiClientError.message` 已从响应体取 `message`，既有 `showError(e.message)` 即展示真因；另在 react-query `MutationCache` 加**全局 onError 兜底**：未自带 `onError` 的写操作失败也 toast 出该（脱敏）message，杜绝静默失败。

## 理由

- **可观测优先**：本项目部署形态（约 50 服、单节点、单运维、内网）下，运维能看到真实错误的价值，远大于「内网攻击者可能从错误文案推断内部细节」的风险。
- **脱敏为安全闸**：真正的机密是**凭据**（DB 口令、proxy 账密、token）。`Desensitize` 专打码凭据，使「展示真因」与「不泄密」可兼得。
- **内网地址 / 主机名 / 文件路径不打码**：它们是**运维定位问题的关键上下文**（哪台、哪个文件失败），且非凭据；打码反而违背本 ADR 的初衷。机密边界守在凭据上，不外扩到运维上下文。
- **traceId 保留**：脱敏文案 + traceId 仍可与服务端完整日志对账，深度排查不丢信息。

## 后果

- `INTERNAL` 错误响应的 `message` 从固定「内部错误」变为脱敏真因；`docs/API.md` 错误响应描述同步。属对外可观测增强，非破坏性（结构不变、仍 `{code,message,traceId}`）。
- `Desensitize` 是 best-effort 正则打码，无法覆盖所有可能的机密形态；新增凭据形态需扩充规则并补单测。**编码纪律**：构造错误信息时不要把明文机密直接拼进去（治本仍在源头），`Desensitize` 是最后一道兜底闸。
- 该决策落为长期规则 [.claude/rules/error-surfacing.md](../../.claude/rules/error-surfacing.md)：所有操作错误必须脱敏后展示前端，不静默隐藏。

## 备选方案

- **维持「一律内部错误」**：最不泄露，但运维盲区大、问题被隐藏——本次正是为修此弊，否决。
- **全量打码（含 IP / 主机 / 路径）**：更保守，但抹掉运维定位关键上下文、违背可观测初衷，且内网地址非凭据，否决。
- **不脱敏直接透传真错**：最可观测，但会泄露 DB 口令 / proxy 账密 / token 等凭据，否决——故引入 `Desensitize` 作安全闸。
