// FR-190：站内开源协议页（项目 MIT + 第三方依赖清单）
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'

import licenseData from '../data/third-party-licenses.json'
import { MIT_LICENSE_TEXT } from '../pages/license'
import AppShell from '../shell/app-shell'
import DocumentTitle from '../shell/document-title'
import { useShellStore } from '../store'
import '../i18n'

function renderAt(path: string) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[path]}>
        <DocumentTitle />
        <AppShell />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('开源协议页（FR-190）', () => {
  beforeEach(() => {
    useShellStore.setState({ sidebarCollapsed: false, mobileNavOpen: false })
  })

  it('展示项目 MIT 全文与第三方依赖清单', () => {
    renderAt('/license')
    expect(screen.getByRole('heading', { name: '开源协议' })).toBeInTheDocument()
    const full = document.querySelector('[data-slot="license-full-text"]')
    expect(full).not.toBeNull()
    const body = full?.textContent ?? ''
    expect(body).toContain('MIT License')
    expect(body).toContain('Copyright (c) 2026 wcpe')
    expect(body).toContain('Permission is hereby granted')
    expect(body.replace(/\r\n/g, '\n').trim()).toBe(MIT_LICENSE_TEXT.replace(/\r\n/g, '\n').trim())

    // 依赖清单（单一运行时表）
    expect(document.querySelector('[data-slot="license-deps"]')).not.toBeNull()
    expect(
      screen.getByText(new RegExp(`运行时依赖（${String(licenseData.counts.total)}）`)),
    ).toBeInTheDocument()
    expect(screen.getByLabelText('搜索软件包…')).toBeInTheDocument()
    const first = licenseData.groups[0]?.items[0]?.name
    expect(first).toBeTruthy()
    expect(screen.getByText(first!)).toBeInTheDocument()
    // 不应出现纯类型包
    expect(screen.queryByText('@types/react')).not.toBeInTheDocument()
    expect(document.title).toBe('Beacon - 开源协议')
  })

  it('搜索可过滤依赖表', async () => {
    const user = userEvent.setup()
    renderAt('/license')
    const first = licenseData.groups[0]?.items[0]
    expect(first).toBeTruthy()
    const search = screen.getByLabelText('搜索软件包…')
    await user.clear(search)
    await user.type(search, first!.name)
    expect(screen.getByText(first!.name)).toBeInTheDocument()
    // 搜一个不存在的串
    await user.clear(search)
    await user.type(search, '___no_such_package_xyz___')
    expect(screen.getByText('没有匹配的软件包')).toBeInTheDocument()
  })

  it('从侧栏底栏链进入 /license', async () => {
    const user = userEvent.setup()
    renderAt('/dashboard')
    const desktop = document.querySelector('[data-slot="desktop-sidebar"]')
    expect(desktop).not.toBeNull()
    const footer = desktop?.querySelector('[data-slot="sidebar-footer"]')
    expect(footer).not.toBeNull()
    await user.click(within(footer as HTMLElement).getByRole('link', { name: '开源协议' }))
    expect(await screen.findByRole('heading', { name: '开源协议' })).toBeInTheDocument()
    expect(document.title).toBe('Beacon - 开源协议')
  })
})
