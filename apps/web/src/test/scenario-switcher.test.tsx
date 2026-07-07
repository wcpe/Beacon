import { getMockScenario, setMockScenario } from '@beacon/devmock'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import AppShell from '../shell/app-shell'
import '../i18n'

// 渲染完整 Shell 并返回可被侦测的 queryClient
function renderShell() {
  const queryClient = new QueryClient()
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={['/dashboard']}>
        <AppShell />
      </MemoryRouter>
    </QueryClientProvider>,
  )
  return queryClient
}

describe('演示模式与场景切换器（FR-159 前端侧）', () => {
  afterEach(() => {
    setMockScenario('normal')
    window.localStorage.clear()
  })

  it('页眉常驻演示模式徽标', () => {
    renderShell()
    expect(screen.getByText('演示模式')).toBeInTheDocument()
  })

  it('切换到异常态会调用场景 API 并触发全量查询失效', async () => {
    const user = userEvent.setup()
    const queryClient = renderShell()
    const invalidateSpy = vi.spyOn(queryClient, 'invalidateQueries')

    await user.click(screen.getByRole('combobox', { name: '数据场景' }))
    await user.click(await screen.findByRole('option', { name: '异常' }))

    expect(getMockScenario()).toBe('error')
    expect(invalidateSpy).toHaveBeenCalled()
  })

  it('切换器提供空态 / 常规 / 超大量 / 异常四态选项', async () => {
    const user = userEvent.setup()
    renderShell()

    await user.click(screen.getByRole('combobox', { name: '数据场景' }))
    for (const label of ['空态', '常规', '超大量', '异常']) {
      expect(await screen.findByRole('option', { name: label })).toBeInTheDocument()
    }
  })
})
