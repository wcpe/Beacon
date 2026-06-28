// PageHeader 单测（FR-105）：
// - 页设 title 时显之；未设时回退当前路由 navModel 叶子 labelKey；
// - count / actions 槽渲染；
// - envScoped 时渲染 EnvSelector，非 envScoped 不渲染。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'

// mock 环境列表（EnvSelector 内部依赖），避免真实请求
vi.mock('@/api/client', () => ({
  listNamespaces: vi.fn(),
}))

import PageHeader, { PageHeaderProvider, usePageHeader, type PageHeaderConfig } from './PageHeader'
import { listNamespaces } from '@/api/client'

// 测试用「页」：在渲染期把给定配置注入 context（模拟各页 usePageHeader 调用）。
function TestPage({ config }: { config: PageHeaderConfig }) {
  usePageHeader(config)
  return <div data-testid="page-body">页面正文</div>
}

// 在指定路由下渲染「PageHeader + 测试页」，二者共享同一 Provider。
function renderAt(path: string, config: PageHeaderConfig, body: ReactNode = null) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <PageHeaderProvider>
          <PageHeader />
          <Routes>
            <Route path={path} element={body ?? <TestPage config={config} />} />
          </Routes>
        </PageHeaderProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  vi.mocked(listNamespaces).mockReset()
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('PageHeader 标题', () => {
  it('页设 title 时显示该标题', () => {
    renderAt('/servers', { title: '我的自定义标题', envScoped: false })
    expect(screen.getByRole('heading', { name: '我的自定义标题' })).toBeInTheDocument()
  })

  it('页未设 title 时回退当前路由叶子 labelKey（servers → 服务器）', () => {
    renderAt('/servers', { envScoped: false })
    expect(screen.getByRole('heading', { name: '服务器' })).toBeInTheDocument()
  })

  // fix-B 回归护栏：标题不收缩、不换行，防窄屏被挤成竖排字符（jsdom 无布局引擎，仅锁结构类名）
  it('标题不收缩不换行（shrink-0 + whitespace-nowrap）', () => {
    renderAt('/servers', { title: '配置中心', envScoped: false })
    const heading = screen.getByRole('heading', { name: '配置中心' })
    expect(heading.className).toMatch(/shrink-0/)
    expect(heading.className).toMatch(/whitespace-nowrap/)
  })
})

describe('PageHeader 计数 / 副标题 / 主操作槽', () => {
  it('渲染 count 槽', () => {
    renderAt('/servers', { title: '服务器', count: '共 3 台', envScoped: false })
    expect(screen.getByText('共 3 台')).toBeInTheDocument()
  })

  it('渲染 actions 槽（主操作）', () => {
    renderAt('/servers', {
      title: '服务器',
      envScoped: false,
      actions: <button type="button">新建</button>,
    })
    expect(screen.getByRole('button', { name: '新建' })).toBeInTheDocument()
  })
})

describe('PageHeader 环境槽', () => {
  it('envScoped 为真时渲染环境选择器', () => {
    renderAt('/servers', { title: '服务器', envScoped: true })
    // EnvSelector 的输入框 aria-label＝「全局环境」
    expect(screen.getByLabelText('全局环境')).toBeInTheDocument()
  })

  it('envScoped 为假时不渲染环境选择器', () => {
    renderAt('/settings', { title: '运维设置', envScoped: false })
    expect(screen.queryByLabelText('全局环境')).not.toBeInTheDocument()
  })

  it('页未指定 envScoped 时取路由叶子默认标记（servers＝true 渲染选择器）', () => {
    renderAt('/servers', { title: '服务器' })
    expect(screen.getByLabelText('全局环境')).toBeInTheDocument()
  })

  it('页未指定 envScoped 且路由叶子未标记（settings）时不渲染选择器', () => {
    renderAt('/settings', { title: '运维设置' })
    expect(screen.queryByLabelText('全局环境')).not.toBeInTheDocument()
  })
})
