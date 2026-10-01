import { afterEach, describe, expect, it, vi } from 'vitest'

import { randomId } from '../lib/random-id'

// RFC 4122 v4 UUID
const UUID_V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/

describe('randomId', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('安全上下文（randomUUID 可用）返回 v4 UUID', () => {
    expect(randomId()).toMatch(UUID_V4)
  })

  it('非安全上下文（randomUUID 缺失）降级到 getRandomValues 仍返回 v4 UUID', () => {
    // 复现真实故障场景：运维以 http://本机IP:8848 登录，此时 window.crypto 存在
    // 但 randomUUID 未定义——旧的直接调用会抛 "crypto.randomUUID is not a function"。
    const original = globalThis.crypto
    vi.stubGlobal('crypto', { getRandomValues: original.getRandomValues.bind(original) })
    expect(randomId()).toMatch(UUID_V4)
  })

  it('每次调用取值不同', () => {
    const ids = new Set(Array.from({ length: 64 }, () => randomId()))
    expect(ids.size).toBe(64)
  })

  it('crypto 整体缺失时仍有可用兜底（不需密码学强度，只求唯一）', () => {
    vi.stubGlobal('crypto', undefined)
    const id = randomId()
    expect(id).not.toBe('')
    expect(randomId()).not.toBe(id)
  })
})
