// /servers 页测试：常规渲染、空态引导、approve 写闭环、keyword 筛选。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ServersPage from '../../pages/servers'
import { renderPage, server, useScenario } from './harness'

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

describe('/servers 服务器页', () => {
  it('常规态渲染服务器资产列表与待确认区', async () => {
    useScenario('normal')
    renderPage(<ServersPage />)

    // 资产列表出现已知子服
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()
    // 待确认区出现待确认身份
    expect(await screen.findByText('game-new-1')).toBeInTheDocument()
  })

  it('空态给出接入引导文案', async () => {
    useScenario('empty')
    renderPage(<ServersPage />)

    expect(await screen.findByText('当前筛选条件下无服务器')).toBeInTheDocument()
    expect(
      await screen.findByText('暂无待确认的注册请求，新 agent 首次接入后会出现在此'),
    ).toBeInTheDocument()
  })

  it('approve 待确认身份后该行从待确认区消失（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    // 定位 game-new-1 所在待确认行的确认按钮
    const pendingRow = (await screen.findByText('game-new-1')).closest('tr')
    expect(pendingRow).not.toBeNull()
    const approveBtn = within(pendingRow as HTMLElement).getByRole('button', { name: '确认接入' })
    await user.click(approveBtn)

    // 弹窗确认（确认按钮文案为「确认接入」）
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '确认接入' }))

    // game-new-1 从待确认区消失
    await waitFor(() => {
      expect(screen.queryByText('game-new-1')).not.toBeInTheDocument()
    })
  })

  it('keyword 搜索按 serverId 过滤资产列表', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    // 初始能看到 lobby-1 与 mall-1
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()

    const searchBox = screen.getByLabelText('搜索 serverId')
    await user.type(searchBox, 'mall')

    await waitFor(() => {
      expect(screen.getByText('mall-1')).toBeInTheDocument()
      expect(screen.queryByText('lobby-1')).not.toBeInTheDocument()
    })
  })
})
