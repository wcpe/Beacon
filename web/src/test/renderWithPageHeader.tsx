// 测试辅助（FR-105）：把被测页面置于两层页眉上下文中渲染。
// 各页改用 usePageHeader 把标题 / 主操作 / 计数注入第二层页眉 PageHeader 后，
// 页面自身不再渲染顶部标题行与主操作按钮；单测若仍需断言这些元素，需把 PageHeader 一并渲染、
// 并把 MemoryRouter 落到该页路由（PageHeader 据路由做标题回退与 envScoped 判定）。
//
// 用法：renderWithPageHeader(<XxxPage />, { path: '/xxx' })，其余 provider（QueryClient）由调用方包裹或本辅助内置。

import { render } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import PageHeader, { PageHeaderProvider } from '@/components/PageHeader'

// 在指定路由下渲染「第二层页眉 PageHeader + 被测页」，二者共享同一 PageHeaderProvider。
export function renderWithPageHeader(ui: ReactElement, opts: { path: string }) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[opts.path]}>
        <PageHeaderProvider>
          <PageHeader />
          <Routes>
            <Route path={opts.path} element={ui} />
          </Routes>
        </PageHeaderProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}
