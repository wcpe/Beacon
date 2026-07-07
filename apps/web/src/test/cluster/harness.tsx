// 集群三页测试装配：msw/node 服务端 + QueryClientProvider + MemoryRouter + i18n。
// 参考 shell.test.tsx，额外接入 devmock 全量 handlers 让页面走真实 mock 端点闭环。

import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, type RenderResult } from '@testing-library/react'
import type { ReactElement } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { setupServer } from 'msw/node'

import { allHandlers, resetMockData, setMockScenario, type MockScenario } from '@beacon/devmock'

import '../../i18n'

/** 集群页测试共用的 mock 服务端（全量 handlers） */
export const server = setupServer(...allHandlers)

/** 复位到指定场景并重建数据仓（用例前调用，保证隔离） */
export function useScenario(scenario: MockScenario): void {
  setMockScenario(scenario)
  resetMockData()
}

/** 在 Provider 树内渲染页面（retry:false 让错误态立即可断言） */
export function renderPage(ui: ReactElement): RenderResult {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}
