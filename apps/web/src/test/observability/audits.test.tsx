// /audits 审计页测试：KPI + 列表渲染、空态、操作人筛选、详情打开、互跳 URL query 初始化（FR-157）。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Route, Routes } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AuditsPage from '../../pages/audits'
import CommandsPage from '../../pages/commands'
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

  it('URL 查询参数初始化筛选：targetRef 落进输入框并驱动服务端过滤（互跳承接，FR-157）', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />, ['/audits?targetRef=srv-none'])

    // 目标输入框以 URL 参数为初值
    expect(await screen.findByLabelText('目标（targetRef）')).toHaveValue('srv-none')
    // 不存在的 targetRef → 服务端过滤后列表为空
    expect(await screen.findByText('当前筛选条件下无审计记录')).toBeInTheDocument()
  })

  it('URL action=message.payload.view 免交互定位 payload 查看审计（FR-157）', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />, ['/audits?action=message.payload.view'])

    // 列表出现该动作的行（服务端过滤生效），其余动作不再出现
    await waitFor(() => {
      const cells = screen
        .getAllByText('message.payload.view')
        .filter((el) => el.closest('tr') !== null)
      expect(cells.length).toBeGreaterThan(0)
    })
    expect(screen.queryByText('identity.approved')).not.toBeInTheDocument()
  })

  it('审计详情互跳命令观测：URL 带 serverId 且落位页筛选初始化生效（FR-157 贯通）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(
      <Routes>
        <Route path="/audits" element={<AuditsPage />} />
        <Route path="/commands" element={<CommandsPage />} />
      </Routes>,
      ['/audits'],
    )

    // 点开首条审计详情
    let row: HTMLElement | null = null
    await waitFor(() => {
      const cells = screen.getAllByText(/identity.approved|zone.rezone.initiated/)
      row = cells.map((el) => el.closest('tr')).find((tr): tr is HTMLTableRowElement => tr !== null) ?? null
      expect(row).not.toBeNull()
    })
    await user.click(row as unknown as HTMLElement)

    // 详情面板互跳链接带 targetRef 作 serverId 查询参数
    const link = await screen.findByRole('link', { name: /在命令观测中查看/ })
    const href = link.getAttribute('href') ?? ''
    const serverId = new URLSearchParams(href.split('?')[1] ?? '').get('serverId') ?? ''
    expect(serverId).not.toBe('')

    // 点击互跳 → 命令观测页挂载，serverId 搜索框以链接参数为初值（链路真正生效）
    await user.click(link)
    expect(await screen.findByText('命令历史')).toBeInTheDocument()
    expect(await screen.findByLabelText('搜索 serverId')).toHaveValue(serverId)
  })
})
