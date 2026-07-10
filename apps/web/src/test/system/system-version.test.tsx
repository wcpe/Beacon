// /system/version 版本与更新页测试：常规渲染、空态（无更新）、触发更新写闭环、代理测试。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import SystemVersionPage from '../../pages/system-version'
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

describe('/system/version 版本与更新页', () => {
  it('常规态渲染当前版本与可用更新', async () => {
    useScenario('normal')
    renderPage(<SystemVersionPage />)

    // 当前版本 v0.21.0
    expect(await screen.findByText('v0.21.0')).toBeInTheDocument()
    // normal 场景有可用更新 v0.22.0（版本徽标 / 更新说明标题多处出现）
    await waitFor(() => {
      expect(screen.getAllByText(/v0\.22\.0/).length).toBeGreaterThan(0)
    })
    // 紧凑分区：版本信息 / 更新与渠道 / 维护操作三段常驻
    expect(screen.getByText('版本信息')).toBeInTheDocument()
    expect(screen.getByText('更新与渠道')).toBeInTheDocument()
    expect(screen.getByText('维护操作')).toBeInTheDocument()
  })

  it('空态（已是最新）给出已最新提示', async () => {
    useScenario('empty')
    renderPage(<SystemVersionPage />)

    expect(await screen.findByText('已是最新版本')).toBeInTheDocument()
  })

  it('触发更新后进度进入下载中（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<SystemVersionPage />)

    await screen.findByText('v0.21.0')
    await user.click(screen.getByRole('button', { name: '应用更新' }))

    // 确认弹窗
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '开始更新' }))

    // 更新进度块进入下载中
    await waitFor(() => {
      expect(screen.getByText('下载中')).toBeInTheDocument()
    })
  })
})
