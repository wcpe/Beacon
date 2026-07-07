// /alert-events 告警事件页测试：KPI + 列表渲染、空态、确认写闭环（状态变化可见）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AlertEventsPage from '../../pages/alert-events'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

describe('/alert-events 告警事件页', () => {
  it('常规态渲染 KPI 与告警列表', async () => {
    useScenario('normal')
    renderPage(<AlertEventsPage />)

    expect(await screen.findByText('告警总数')).toBeInTheDocument()
    // 出现健康流转告警摘要
    expect((await screen.findAllByText(/状态 (lost → offline|online → degraded)/)).length).toBeGreaterThan(0)
  })

  it('空态给出无记录提示', async () => {
    useScenario('empty')
    renderPage(<AlertEventsPage />)

    expect(await screen.findByText('当前筛选条件下无告警事件')).toBeInTheDocument()
  })

  it('确认待处理告警后状态徽标更新为已确认（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)

    // 等待列表首屏，找到第一条「待处理」告警行
    let row: HTMLTableRowElement | null = null
    await waitFor(() => {
      const badges = screen.getAllByText('待处理')
      const tr = badges.map((el) => el.closest('tr')).find((r): r is HTMLTableRowElement => r !== null) ?? null
      expect(tr).not.toBeNull()
      row = tr
    })
    const scoped = within(row as unknown as HTMLElement)

    // 点确认 → 打开处理弹窗 → 确认
    await user.click(scoped.getByRole('button', { name: '确认' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '确认' }))

    // 该行状态徽标变为「已确认」
    await waitFor(() => {
      expect(within(row as unknown as HTMLElement).getByText('已确认')).toBeInTheDocument()
    })
  })
})
