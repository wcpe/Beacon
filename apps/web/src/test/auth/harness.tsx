// 鉴权测试装配（FR-179）：QueryClientProvider + i18n + 假 fetch / 假响应工具。
// 用假 fetch 精确控制登录 / 登出 / 401 场景，不依赖 msw（这些端点走真控制面，非 mock）。
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderResult } from '@testing-library/react'
import type { ReactElement } from 'react'

import '../../i18n'

/** 构造一个仅含请求层所需字段（ok / status / text）的假 Response。 */
export function fakeResponse(status: number, body?: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    text: () => Promise.resolve(body === undefined ? '' : JSON.stringify(body)),
  } as unknown as Response
}

/** 在 QueryClientProvider（retry:false 让错误态立即可断言）内渲染。 */
export function renderWithClient(ui: ReactElement): RenderResult {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  return render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>)
}
