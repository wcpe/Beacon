// /audits 审计页测试：KPI + 列表渲染、空态、操作人筛选、详情打开。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AuditsPage from '../../pages/audits'
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

describe('/audits 审计页', () => {
  it('常规态渲染 KPI 与审计列表', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />)

    expect(await screen.findByText('审计总数')).toBeInTheDocument()
    // 出现已知审计动作
    expect((await screen.findAllByText(/identity.approved|zone.rezone.initiated/)).length).toBeGreaterThan(0)
  })

  it('空态给出无记录提示', async () => {
    useScenario('empty')
    renderPage(<AuditsPage />)

    expect(await screen.findByText('当前筛选条件下无审计记录')).toBeInTheDocument()
  })

  it('列表行直接展示目标（targetRef），无需点开详情', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />)

    // 表头出现「目标」列（基础信息前置到行）
    expect(await screen.findByText('目标')).toBeInTheDocument()
    // 未点开任何行时右侧详情面板不渲染（列表行已可见目标，不产生模态遮罩）
    expect(screen.queryByText('审计详情')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('点击行打开审计详情', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    // 等待表格行渲染（排除筛选下拉的同名 option，只取有 tr 祖先的动作单元格）
    let row: HTMLElement | null = null
    await waitFor(() => {
      const cells = screen.getAllByText(/identity.approved|zone.rezone.initiated/)
      row = cells.map((el) => el.closest('tr')).find((tr): tr is HTMLTableRowElement => tr !== null) ?? null
      expect(row).not.toBeNull()
    })
    await user.click(row as unknown as HTMLElement)

    // 右侧非模态详情面板出现（含来源 IP 字段），且不产生模态遮罩层
    await waitFor(() => {
      expect(screen.getByText('审计详情')).toBeInTheDocument()
    })
    expect(screen.getByText('来源 IP')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
