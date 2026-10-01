/**
 * 运行期可靠的随机 ID 生成器。
 *
 * 为什么不直接用 `crypto.randomUUID()`：它**只在安全上下文**（HTTPS 或 localhost）可用。
 * 运维常以 `http://本机IP:8848` 登录管理台（见 docs/OPERATIONS.md），此时 `window.crypto`
 * 存在但 `randomUUID` 未定义，直接调用会抛 `crypto.randomUUID is not a function`——
 * API 密钥创建、MCP 客户端创建/轮换/启用、生命周期提审等处都会因此失败。
 *
 * 降级路径用的是 `crypto.getRandomValues`：它在**非安全上下文仍可用**（只有 `randomUUID`
 * 与 `crypto.subtle` 会被裁掉），因此足以生成 RFC 4122 v4 UUID。
 */
export function randomId(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  if (typeof crypto !== 'undefined' && typeof crypto.getRandomValues === 'function') {
    return uuidV4From(crypto.getRandomValues(new Uint8Array(16)))
  }
  // 极端兜底：宿主既无 randomUUID 也无 getRandomValues 时，用时间戳 + 随机数拼一个。
  // 调用方只要求「每次调用取值不同」（幂等键 / 临时 id），不需要密码学强度。
  return `r${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`
}

/** 把 16 字节随机数按 RFC 4122 v4 规范格式化。 */
function uuidV4From(bytes: Uint8Array): string {
  bytes[6] = (bytes[6] & 0x0f) | 0x40 // version 4
  bytes[8] = (bytes[8] & 0x3f) | 0x80 // variant 10
  const hex = Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('')
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`
}
