import {
  getMockDataVolume,
  getMockLatency,
  getMockScenario,
  setMockDataVolume,
  setMockLatency,
  setMockScenario,
} from '@beacon/devmock'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'

import AppShell from '../shell/app-shell'
import '../i18n'

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

async function openMockTools(user: ReturnType<typeof userEvent.setup>) {
  await user.click(await screen.findByRole('button', { name: '打开 Mock 开发工具' }))
  return screen.getByRole('region', { name: 'Mock 开发工具' })
}

function dispatchPointerEvent(
  target: HTMLElement,
  type: 'pointerdown' | 'pointermove' | 'pointerup',
  values: { pointerId: number; clientX: number; clientY: number; button?: number; isPrimary?: boolean },
) {
  const event = new Event(type, { bubbles: true, cancelable: true })
  for (const [key, value] of Object.entries(values)) {
    Object.defineProperty(event, key, { value })
  }
  fireEvent(target, event)
}

describe('演示模式 Mock 开发工具（FR-159 前端侧）', () => {
  afterEach(() => {
    setMockScenario('normal')
    setMockDataVolume('baseline')
    setMockLatency('none')
    window.localStorage.clear()
  })

  it('页眉移除演示徽标与场景切换，右下角浮球可展开面板', async () => {
    const user = userEvent.setup()
    renderShell()

    expect(screen.queryByText('演示模式')).not.toBeInTheDocument()
    expect(screen.queryByRole('combobox', { name: '数据场景' })).not.toBeInTheDocument()
    const panel = await openMockTools(user)
    expect(panel).toBeInTheDocument()
    expect(within(panel).getByText('当前组合')).toBeInTheDocument()
  })

  it('生命周期评审入口位于工具面板并通过弹窗展示对应模拟界面', async () => {
    const user = userEvent.setup()
    renderShell()
    const panel = await openMockTools(user)

    expect(within(panel).getByRole('heading', { name: '生命周期评审' })).toBeInTheDocument()
    await user.click(within(panel).getByRole('button', { name: '服务器' }))
    const serverDialog = await screen.findByRole('dialog')
    expect(within(serverDialog).getByRole('heading', { name: '服务器生命周期评审' })).toBeInTheDocument()
    expect(within(serverDialog).getByRole('region', { name: '服务器生命周期评审 mock' })).toBeInTheDocument()

    await user.click(within(serverDialog).getByRole('button', { name: 'Close' }))
    await user.click(within(panel).getByRole('button', { name: '命名空间' }))
    const namespaceDialog = await screen.findByRole('dialog')
    expect(within(namespaceDialog).getByRole('heading', { name: '命名空间生命周期评审' })).toBeInTheDocument()
    expect(within(namespaceDialog).getByRole('region', { name: '命名空间生命周期评审 mock' })).toBeInTheDocument()
  })

  it('切换场景调用兼容 API 并触发全量查询失效', async () => {
    const user = userEvent.setup()
    const queryClient = renderShell()
    const panel = await openMockTools(user)
    const invalidateSpy = vi.spyOn(queryClient, 'invalidateQueries')

    await user.click(within(panel).getByRole('button', { name: '异常' }))

    expect(getMockScenario()).toBe('error')
    expect(invalidateSpy).toHaveBeenCalled()
  })

  it('数据量与延时可独立切换并显示笛卡尔积组合', async () => {
    const user = userEvent.setup()
    const queryClient = renderShell()
    const panel = await openMockTools(user)
    const invalidateSpy = vi.spyOn(queryClient, 'invalidateQueries')

    await user.click(within(panel).getByRole('button', { name: '大 · 500 条/域' }))
    await user.click(within(panel).getByRole('button', { name: '慢 · 800 ms' }))

    expect(getMockDataVolume()).toBe('large')
    expect(getMockLatency()).toBe('slow')
    expect(within(panel).getByText('大 · 500 条/域 × 慢 · 800 ms')).toBeInTheDocument()
    expect(invalidateSpy).toHaveBeenCalledTimes(2)
  })

  it('拖动浮球约束在视口内、持久化位置且抑制后续点击', async () => {
    renderShell()
    const trigger = await screen.findByRole('button', { name: '打开 Mock 开发工具' })

    dispatchPointerEvent(trigger, 'pointerdown', {
      pointerId: 1,
      isPrimary: true,
      button: 0,
      clientX: 100,
      clientY: 100,
    })
    dispatchPointerEvent(trigger, 'pointermove', {
      pointerId: 1,
      clientX: 160,
      clientY: 140,
    })
    dispatchPointerEvent(trigger, 'pointerup', {
      pointerId: 1,
      clientX: 160,
      clientY: 140,
    })
    fireEvent.click(trigger)

    expect(document.querySelector('[data-slot="mock-dev-tools-panel"]')).toBeNull()
    const storedPosition = window.localStorage.getItem('beacon-mock-dev-tools-position')
    expect(storedPosition).not.toBeNull()
    const position = JSON.parse(storedPosition ?? '{}') as { left: number; top: number }
    expect(position.left).toBeGreaterThanOrEqual(12)
    expect(position.top).toBeGreaterThanOrEqual(12)
    expect(position.left).toBeLessThanOrEqual(window.innerWidth - 60)
    expect(position.top).toBeLessThanOrEqual(window.innerHeight - 60)
  })
})
