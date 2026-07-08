// /alert-events 告警事件页测试：KPI + 列表渲染、空态、确认写闭环（状态变化可见）。
import { screen, waitFor } from '@testing-library/react'
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

  it('点行开右侧非模态详情面板并确认待处理告警（写闭环）', async () => {
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

    // 点行 → 右侧非模态详情面板出现（不产生模态遮罩层）
    await user.click(row as unknown as HTMLElement)
    await waitFor(() => {
      expect(screen.getByText('告警详情')).toBeInTheDocument()
    })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    // 面板内点「确认」完成写闭环
    await user.click(screen.getByRole('button', { name: '确认' }))

    // 详情面板内状态徽标更新为「已确认」（选中行从最新数据派生；排除筛选下拉的同名 option）
    await waitFor(() => {
      const acknowledged = screen
        .getAllByText('已确认')
        .filter((el) => el.tagName !== 'OPTION')
      expect(acknowledged.length).toBeGreaterThan(0)
    })
  })
})
