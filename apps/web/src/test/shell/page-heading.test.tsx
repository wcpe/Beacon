// 全站页面标题地标（h1）覆盖性用例：
// 页眉不再承载面包屑后，页面身份改由内容区表达，因此每个路由页都必须「存在且仅存在一个 h1」——
// 否则读屏用户失去「跳到页面标题」锚点、文档大纲也没有页面节点。
//
// 覆盖方式：对 routes.tsx 的 ALL_PAGES 逐页渲染页面组件本体并断言 h1 数量与文案，
// 路由表增删页面时用例自动跟随，一次挡住两类回归：新页面忘记接标题（0 个 h1）、
// 一页接了两套标题（2 个 h1）。标题文案统一取 NavPage.titleKey（nav.* 域），
// 与侧栏标签、document.title 同源。
//
// 这里只渲染页面本体、不套整个 Shell：地标由页面自己产出，这样用例跑得快且失败信息更聚焦；
// 「Shell 层不得再塞一个 h1」由文件末尾的整壳用例单独锁住。
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ComponentType } from 'react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it } from 'vitest'

import { i18next } from '../../i18n'
import LoginPage from '../../pages/login'
import LicensePage from '../../pages/license'
import { ALL_PAGES } from '../../routes'
import AppShell from '../../shell/app-shell'
import '../../i18n'

// 页面组件渲染所需的最小 Provider 树：数据查询 + 路由上下文（页面普遍用 useSearchParams）
function renderWithProviders(ui: React.ReactElement, path = '/'): void {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

// 当前文档里的 h1 列表
function h1s(): HTMLElement[] {
  return [...document.querySelectorAll<HTMLElement>('h1')]
}

describe('页面标题地标（h1）', () => {
  it.each(ALL_PAGES.map((page) => [page.path, page.titleKey, page.Component] as const))(
    '%s 页面恰有一个 h1，且文案为 nav 标题 %s',
    (path, titleKey, Component: ComponentType) => {
      renderWithProviders(<Component />, path)
      const headings = h1s()
      expect(headings).toHaveLength(1)
      expect(headings[0]?.textContent?.trim()).toBe(i18next.t(titleKey))
    },
  )

  it('/license 页面恰有一个 h1（不进主导航的站内页同样需要地标）', () => {
    renderWithProviders(<LicensePage />, '/license')
    const headings = h1s()
    expect(headings).toHaveLength(1)
    expect(headings[0]?.textContent?.trim()).toBe(i18next.t('common.license.pageTitle'))
  })

  it('/login 页面恰有一个 h1（独立于 Shell 的公开页）', () => {
    renderWithProviders(<LoginPage />, '/login')
    const headings = h1s()
    expect(headings).toHaveLength(1)
    expect(headings[0]?.textContent?.trim()).toBe(i18next.t('auth.login.title'))
  })

  it('整壳渲染时不额外产生 h1（页眉 / 侧栏不得再塞地标）', () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/dashboard']}>
          <AppShell />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    // 完整 Shell（页眉 + 侧栏 + 路由出口）里仍然只有一个 h1，且由页面本体提供
    const headings = h1s()
    expect(headings).toHaveLength(1)
    expect(headings[0]?.textContent?.trim()).toBe(i18next.t('nav.dashboard'))
  })

  it('隐藏标题不参与视觉呈现（sr-only，不使用 display:none，读屏仍可达）', () => {
    const dashboard = ALL_PAGES[0]
    const DashboardComponent = dashboard.Component
    renderWithProviders(<DashboardComponent />, dashboard.path)
    const heading = h1s()[0]
    expect(heading).toHaveClass('sr-only')
    // display:none / visibility:hidden 会把标题从无障碍树里摘掉，等于没补
    expect(heading).not.toHaveStyle({ display: 'none' })
    expect(heading.getAttribute('aria-hidden')).toBeNull()
  })
})
