import { afterEach, describe, expect, it, vi } from 'vitest'

import { isDemoMode } from '../demo-mode'

// 演示模式门控（FR-159）：发布产物不得启动 mock，否则 Service Worker 会拦截 /admin/*
// 使真实控制面后端不可达。这里锁死判据，防止误改回无条件启动。
describe('演示模式判定', () => {
  afterEach(() => {
    vi.unstubAllEnvs()
  })

  it('dev server 恒为演示模式', () => {
    vi.stubEnv('DEV', true)
    vi.stubEnv('MODE', 'development')
    expect(isDemoMode()).toBe(true)
  })

  it('生产构建且 mode=demo 时为演示模式', () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('MODE', 'demo')
    expect(isDemoMode()).toBe(true)
  })

  it('常规生产构建不得进入演示模式', () => {
    vi.stubEnv('DEV', false)
    vi.stubEnv('MODE', 'production')
    expect(isDemoMode()).toBe(false)
  })
})
