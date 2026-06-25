// useUpdateCheck 单测（FR-100）：
// ① 纯派生：自动检查开关 / 周期由 store 设置派生（缺项 / 非法值兜默认，下界保护）；
// ② 自动检查关闭时禁用周期轮询（refetchInterval=false 不自动重拉）；
// ③ refresh 走 force=true 强制刷新并回填同一缓存。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderHook, waitFor, act } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import type { SettingView, UpdateCheckView } from '@/api/types'

vi.mock('@/api/client', () => ({
  checkUpdate: vi.fn(),
  listSettings: vi.fn(),
}))

import { useUpdateCheck, deriveAutoCheckEnabled, deriveIntervalMs } from './useUpdateCheck'
import { checkUpdate, listSettings } from '@/api/client'

const CHECK_OK: UpdateCheckView = {
  status: 'ok',
  currentVersion: 'v0.10.0',
  channel: 'stable',
  hasUpdate: true,
  isDevBuild: false,
  latestVersion: 'v0.11.0',
  releaseNotes: '## 变更\n- 新增 X',
  releaseUrl: 'https://github.com/wcpe/Beacon/releases/tag/v0.11.0',
  publishedAt: '2026-06-20T08:00:00Z',
  checkedAt: '2026-06-25T10:00:00Z',
  cacheExpiresAt: '2026-06-25T16:00:00Z',
}

function setting(key: string, value: string, valueType: SettingView['valueType']): SettingView {
  return { key, value, valueType, default: value, desc: '', isStartup: false }
}

function wrapper({ children }: { children: ReactNode }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(checkUpdate).mockResolvedValue(CHECK_OK)
  vi.mocked(listSettings).mockResolvedValue([])
})

describe('deriveAutoCheckEnabled', () => {
  it('缺项默认开', () => {
    expect(deriveAutoCheckEnabled(undefined)).toBe(true)
    expect(deriveAutoCheckEnabled([])).toBe(true)
  })
  it('显式 false 关闭', () => {
    expect(deriveAutoCheckEnabled([setting('update.auto-check-enabled', 'false', 'bool')])).toBe(false)
  })
  it('true 开启', () => {
    expect(deriveAutoCheckEnabled([setting('update.auto-check-enabled', 'true', 'bool')])).toBe(true)
  })
})

describe('deriveIntervalMs', () => {
  it('缺项 / 非法回退默认 6h', () => {
    expect(deriveIntervalMs(undefined)).toBe(6 * 3600 * 1000)
    expect(deriveIntervalMs([setting('update.check-interval-hours', 'abc', 'int')])).toBe(6 * 3600 * 1000)
  })
  it('合法值按小时换算毫秒', () => {
    expect(deriveIntervalMs([setting('update.check-interval-hours', '12', 'int')])).toBe(12 * 3600 * 1000)
  })
  it('越界小值（0）回退默认（下界保护）', () => {
    expect(deriveIntervalMs([setting('update.check-interval-hours', '0', 'int')])).toBe(6 * 3600 * 1000)
  })
})

describe('useUpdateCheck', () => {
  it('首屏走非强制检查并暴露结果', async () => {
    const { result } = renderHook(() => useUpdateCheck(), { wrapper })
    await waitFor(() => expect(result.current.data).toEqual(CHECK_OK))
    expect(vi.mocked(checkUpdate)).toHaveBeenCalledWith(false)
  })

  it('refresh 走 force=true 强制刷新', async () => {
    const { result } = renderHook(() => useUpdateCheck(), { wrapper })
    await waitFor(() => expect(result.current.data).toEqual(CHECK_OK))
    await act(async () => {
      await result.current.refresh()
    })
    expect(vi.mocked(checkUpdate)).toHaveBeenCalledWith(true)
  })
})
