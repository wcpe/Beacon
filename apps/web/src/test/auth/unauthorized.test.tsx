// 全局 401 处理测试（FR-179）：任意 /admin/* 请求遇 401 → 清令牌 + 触发全局跳登录回调。
// 直接驱动请求层统一封装 request，覆盖鉴权注入与 401 收敛行为。
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiClientError, request } from '../../api/cluster'
import { currentToken, setAuth, setOnUnauthorized } from '../../state/auth'
import { fakeResponse } from './harness'

beforeEach(() => {
  localStorage.clear()
  setAuth('tok-live', 'admin')
})

afterEach(() => {
  vi.unstubAllGlobals()
  setOnUnauthorized(() => {
    // 空操作
  })
})

describe('请求层鉴权注入与 401 处理', () => {
  it('有令牌时注入 Authorization: Bearer', async () => {
    const fetchMock = vi.fn().mockResolvedValue(fakeResponse(200, { ok: true }))
    vi.stubGlobal('fetch', fetchMock)

    await request('GET', '/admin/v2/servers')

    // GET 无请求体，故 headers 恰为单一鉴权头（无 Content-Type），可直接断言完整对象
    expect(fetchMock).toHaveBeenCalledWith(
      '/admin/v2/servers',
      expect.objectContaining({ headers: { Authorization: 'Bearer tok-live' } }),
    )
  })

  it('遇 401：清令牌并触发全局跳登录回调，同时抛脱敏错误', async () => {
    const onUnauthorized = vi.fn()
    setOnUnauthorized(onUnauthorized)
    const fetchMock = vi
      .fn()
      .mockResolvedValue(fakeResponse(401, { code: 'UNAUTHORIZED', message: '缺少或非法的 token' }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(request('GET', '/admin/v2/servers')).rejects.toBeInstanceOf(ApiClientError)
    expect(currentToken()).toBe('')
    expect(onUnauthorized).toHaveBeenCalledTimes(1)
  })
})
