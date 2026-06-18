/**
 * Mock 模式入口
 *
 * 通过环境变量 VITE_USE_MOCK=true 启用。
 * 启用后拦截 /admin/v1/* 的 fetch 请求，返回 mock 数据。
 */

import { handleMockRequest } from './handlers'

const MOCK_BASE = '/admin/v1'

let enabled = false

/** 检查是否启用 mock 模式 */
export function isMockEnabled(): boolean {
  return enabled
}

/** 启用 mock 模式（拦截 fetch） */
export function enableMock(): void {
  if (enabled) return
  enabled = true

  const originalFetch = window.fetch

  window.fetch = async (input: string | URL | Request, init?: RequestInit): Promise<Response> => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url

    // 只拦截 admin API
    if (url.startsWith(MOCK_BASE) || url.includes(MOCK_BASE)) {
      // 提取 path + query（去掉 base URL）
      const urlObj = new URL(url, 'http://localhost')
      const result = await handleMockRequest(urlObj.pathname + urlObj.search, init)
      // 模拟网络延迟（50-150ms）
      await new Promise((r) => setTimeout(r, 50 + Math.random() * 100))
      return result
    }

    return originalFetch.call(window, input, init)
  }

  console.log('[Mock] Mock API 模式已启用，拦截 /admin/v1/* 请求')
}

/** 如果环境变量设置了，自动启用 */
if (typeof window !== 'undefined') {
  // Vite 环境变量通过 import.meta.env 访问，但这里用运行时检查
  const useMock = localStorage.getItem('VITE_USE_MOCK')
  if (useMock === 'true') {
    enableMock()
  }
}
