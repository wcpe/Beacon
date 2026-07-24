// FR-159 mock 数据层共用支撑：确定性随机、伪哈希、时间基准。
// 数据集必须"固定种子/静态生成、同一会话内稳定"，因此这里不出现任何非确定性来源
//（时间基准在模块加载时取整一次，会话内恒定）。

/** 会话内固定的时间基准（加载时按 5 分钟取整），所有 mock 时间戳相对它偏移 */
export const BASE_MS = Math.floor(Date.now() / 300_000) * 300_000

/** 基准时间偏移出 ISO-8601（UTC）时间串 */
export function isoOffset(offsetMs: number): string {
  return new Date(BASE_MS + offsetMs).toISOString()
}

/** mulberry32 确定性伪随机数生成器（固定种子 → 固定序列） */
export function createRng(seed: number): () => number {
  let state = seed >>> 0
  return () => {
    state = (state + 0x6d2b79f5) >>> 0
    let t = Math.imul(state ^ (state >>> 15), 1 | state)
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296
  }
}

/** FNV-1a 字符串散列（32 位无符号） */
export function hashString(input: string): number {
  let h = 2166136261
  for (let i = 0; i < input.length; i++) {
    h ^= input.charCodeAt(i)
    h = Math.imul(h, 16777619)
  }
  return h >>> 0
}

/** 由输入串确定性生成 64 位 hex 的"伪 sha256"（仅供 mock 展示，非真实摘要） */
export function pseudoSha256(input: string): string {
  let out = ''
  let h = hashString(input)
  for (let i = 0; i < 8; i++) {
    h = Math.imul(h ^ (h >>> 13), 2654435761) >>> 0
    out += h.toString(16).padStart(8, '0')
  }
  return out.slice(0, 64)
}

/** 由输入串确定性生成 UUIDv4 形态的标识 */
export function uuidFrom(input: string): string {
  const hex = pseudoSha256(input)
  return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-4${hex.slice(13, 16)}-8${hex.slice(17, 20)}-${hex.slice(20, 32)}`
}

/** 从数组中按确定性随机取一个元素 */
export function pickOne<T>(rng: () => number, list: readonly T[]): T {
  return list[Math.floor(rng() * list.length)]
}
