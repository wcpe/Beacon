// useConnectionStatus 单测（FR-78）：
// 覆盖「轮询成功 → online → 轮询失败 → offline → 恢复成功 → 回 online 且触发一次全量失效（自动刷新）」
// 以及「首屏未返回 → connecting，不误判断开」。
// 连通态从既有 system-status 轮询查询的成功/失败派生（管理台无浏览器 SSE，见规格 §3.1）。
import { describe, it, expect, vi } from 'vitest'
import { renderHook, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider, useQuery } from '@tanstack/react-query'
import type { ReactNode } from 'react'

// mock 后端调用：useConnectionStatus 依赖的心跳查询走 systemStatus
vi.mock('@/api/client', () => ({
  systemStatus: vi.fn(),
}))

import { useConnectionStatus } from './useConnectionStatus'
import { systemStatus } from '@/api/client'

// 健康样例：连通态判断不依赖具体数值，仅看查询成功/失败
const OK = { version: 'v0', db: { connected: true } }

// 构造一个带 QueryClient 的 wrapper；同时挂一个驱动 system-status 查询的探针组件，
// 使 hook 读到的查询状态由 systemStatus mock 决定（hook 自身只读状态，不发请求）。
function makeWrapper(qc: QueryClient) {
  function HeartbeatProbe() {
    // 复刻 SystemHeader 的查询键与轮询，作为全局心跳来源
    useQuery({ queryKey: ['system-status'], queryFn: systemStatus, refetchInterval: 50 })
    return null
  }
  return function Wrapper({ children }: { children: ReactNode }) {
    return (
      <QueryClientProvider client={qc}>
        <HeartbeatProbe />
        {children}
      </QueryClientProvider>
    )
  }
}

function newClient() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}

describe('useConnectionStatus', () => {
  it('心跳查询成功时判定为 online', async () => {
    vi.mocked(systemStatus).mockResolvedValue(OK as never)
    const qc = newClient()
    const { result } = renderHook(() => useConnectionStatus(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.status).toBe('online'))
  })

  it('心跳查询尚未返回时判定为 connecting（首屏不误弹断开）', () => {
    // 永不 resolve：模拟首屏请求在途
    vi.mocked(systemStatus).mockImplementation(() => new Promise(() => {}))
    const qc = newClient()
    const { result } = renderHook(() => useConnectionStatus(), { wrapper: makeWrapper(qc) })
    expect(result.current.status).toBe('connecting')
  })

  it('心跳查询失败时判定为 offline', async () => {
    vi.mocked(systemStatus).mockRejectedValue(new Error('网络错误'))
    const qc = newClient()
    const { result } = renderHook(() => useConnectionStatus(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.status).toBe('offline'))
  })

  it('从 offline 恢复到 online 时触发一次全量查询失效（自动刷新）', async () => {
    // 先失败 → offline；恢复成功 → online，且边沿触发 invalidateQueries
    vi.mocked(systemStatus).mockRejectedValue(new Error('断开'))
    const qc = newClient()
    const invalidateSpy = vi.spyOn(qc, 'invalidateQueries')
    const { result } = renderHook(() => useConnectionStatus(), { wrapper: makeWrapper(qc) })
    await waitFor(() => expect(result.current.status).toBe('offline'))

    // 控制面恢复：后续轮询成功
    vi.mocked(systemStatus).mockResolvedValue(OK as never)
    await qc.refetchQueries({ queryKey: ['system-status'] })
    await waitFor(() => expect(result.current.status).toBe('online'))
    // 恢复边沿应触发至少一次全量失效（无 key），驱动各页面重取数据
    await waitFor(() => expect(invalidateSpy).toHaveBeenCalled())
  })
})
