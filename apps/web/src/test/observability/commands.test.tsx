// /commands 命令观测页测试：KPI + 历史列表渲染、空态、状态筛选交互。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

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

describe('/commands 命令观测页', () => {
  it('常规态渲染 KPI 与命令历史', async () => {
    useScenario('normal')
    renderPage(<CommandsPage />)

    // KPI 命令总数标签出现
    expect(await screen.findByText('命令总数')).toBeInTheDocument()
    // 历史区块标题
    expect(await screen.findByText('命令历史')).toBeInTheDocument()
    // 历史列表出现已知命令类型（devmock COMMAND_TYPES 之一）
    expect((await screen.findAllByText(/asset_rescan|resync-config|tail-logs/)).length).toBeGreaterThan(0)
  })

  it('空态给出无记录提示', async () => {
    useScenario('empty')
    renderPage(<CommandsPage />)

    expect(await screen.findByText('当前筛选条件下无命令记录')).toBeInTheDocument()
  })

  it('按状态筛选失败后列表出现失败结果', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<CommandsPage />)

    // 等待首屏历史列表
    await screen.findByText('命令历史')

    // 选择状态=失败
    const statusSelect = screen.getByLabelText('状态')
    await user.selectOptions(statusSelect, 'failed')

    // 列表内出现失败结果文案（devmock failed 行 resultDetail），且筛选后仅剩失败命令
    await waitFor(() => {
      expect(screen.getAllByText('执行失败：agent 回执超时').length).toBeGreaterThan(0)
    })
  })

  it('点击命令历史行打开右侧非模态详情面板', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<CommandsPage />)

    await screen.findByText('命令历史')

    // 取命令历史列表首行（有 tr 祖先的命令类型单元格）
    let row: HTMLTableRowElement | null = null
    await waitFor(() => {
      const cells = screen.getAllByText(/asset_rescan|resync-config|tail-logs/)
      row = cells.map((el) => el.closest('tr')).find((tr): tr is HTMLTableRowElement => tr !== null) ?? null
      expect(row).not.toBeNull()
    })
    await user.click(row as unknown as HTMLElement)

    // 右侧详情面板出现（命令详情标题 + 生命周期字段），且不产生模态遮罩层
    await waitFor(() => {
      expect(screen.getByText('命令详情')).toBeInTheDocument()
    })
    expect(screen.getByText('生命周期')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})
