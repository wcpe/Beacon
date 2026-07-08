// /servers 页测试（主从布局）：主体资产列表常规渲染、空态引导、待确认抽屉 approve 写闭环、
// keyword 筛选。待确认收敛到吸顶入口 → 抽屉里处理，故 approve 用例先开抽屉。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ServersPage from '../../pages/servers'
import { createTestServer, renderPage, useScenario } from './harness'

// 本文件独享 mock 服务端实例
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

describe('/servers 服务器页', () => {
  it('常规态渲染服务器资产列表，待确认入口带计数', async () => {
    useScenario('normal')
    renderPage(<ServersPage />)

    // 资产列表出现已知子服
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()
    // 吸顶入口按钮存在（待确认收敛为入口，不再默认铺开）
    const pendingBtn = await screen.findByRole('button', { name: /注册待确认/ })
    expect(pendingBtn).toBeInTheDocument()
  })

  it('空态给出资产列表接入引导', async () => {
    useScenario('empty')
    renderPage(<ServersPage />)

    expect(await screen.findByText('当前筛选条件下无服务器')).toBeInTheDocument()
  })

  it('打开待确认抽屉后 approve 该行从抽屉消失（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    // 等主体加载完，再从吸顶入口打开待确认抽屉
    await screen.findByText('lobby-1')
    await user.click(await screen.findByRole('button', { name: /注册待确认/ }))

    // 抽屉内定位 game-new-1 所在待确认行的确认按钮
    const pendingRow = (await screen.findByText('game-new-1')).closest('tr')
    expect(pendingRow).not.toBeNull()
    const approveBtn = within(pendingRow as HTMLElement).getByRole('button', { name: '确认接入' })
    await user.click(approveBtn)

    // 弹窗确认（确认按钮文案为「确认接入」）
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '确认接入' }))

    // game-new-1 从待确认抽屉消失
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
