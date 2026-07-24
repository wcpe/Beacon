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
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url

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

// localStorage 标志键：登录页「演示模式」开关写入此键，刷新后据此自动启用 mock。
const MOCK_FLAG_KEY = 'VITE_USE_MOCK'

/** 运行时是否开启了演示模式（localStorage 标志） */
export function isDemoModeOn(): boolean {
  if (typeof window === 'undefined') return false
  return localStorage.getItem(MOCK_FLAG_KEY) === 'true'
}

/**
 * 开启演示模式（登录页「演示模式」开关触发）：写 localStorage 标志并立即启用 mock。
 * 写标志使刷新后仍保持启用（fetch 一旦被某轮覆写无法干净还原，故启用后建议刷新以从干净态加载）。
 */
export function enableDemoMode(): void {
  if (typeof window !== 'undefined') localStorage.setItem(MOCK_FLAG_KEY, 'true')
  enableMock()
}

/** 关闭演示模式：清 localStorage 标志（已覆写的 fetch 需刷新页面才能还原为真后端） */
export function disableDemoMode(): void {
  if (typeof window !== 'undefined') localStorage.removeItem(MOCK_FLAG_KEY)
}

/** 如果 localStorage 标志开启了演示模式，自动启用（运行时开关，与构建期 env 开关并存） */
if (typeof window !== 'undefined' && isDemoModeOn()) {
  enableMock()
}
