// /api-keys 密钥页测试：常规渲染、空态引导、创建出一次性明文、吊销闭环。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ApiKeysPage from '../../pages/api-keys'
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

describe('/api-keys 密钥页', () => {
  it('常规态渲染密钥列表', async () => {
    useScenario('normal')
    renderPage(<ApiKeysPage />)

    expect(await screen.findByText('运维脚本（巡检）')).toBeInTheDocument()
    expect(await screen.findByText('业务管理后端')).toBeInTheDocument()
  })

  it('空态给出创建引导', async () => {
    useScenario('empty')
    renderPage(<ApiKeysPage />)

    expect(
      await screen.findByText('暂无 API 密钥，点击「创建密钥」新增第一把'),
    ).toBeInTheDocument()
  })

  it('创建密钥后弹出一次性明文（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ApiKeysPage />)

    await screen.findByText('运维脚本（巡检）')
    await user.click(screen.getByRole('button', { name: '创建密钥' }))

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('名称'), '新巡检脚本')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    // 一次性明文弹窗出现，展示完整 40+ 位明文（区别于列表里的短前缀）
    const plaintextDialog = await screen.findByRole('dialog')
    expect(within(plaintextDialog).getByText('密钥已创建')).toBeInTheDocument()
    expect(within(plaintextDialog).getByText(/^bk_\w{20,}/)).toBeInTheDocument()
  })

  it('点击密钥行展开非模态详情面板（不产生遮罩）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ApiKeysPage />)

    // 初始无详情、无 dialog
    expect(screen.queryByText('密钥详情')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    await user.click(await screen.findByText('业务管理后端'))

    // 固定层详情出现（非 role=dialog）
    expect(await screen.findByText('密钥详情')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // 详情面板内出现吊销 / 重置操作
    expect(screen.getByRole('button', { name: '吊销' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重置' })).toBeInTheDocument()
  })

  it('详情面板吊销生效密钥后状态变为已吊销（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ApiKeysPage />)

    // 点行展开详情面板，在面板内发起吊销
    await user.click(await screen.findByText('业务管理后端'))
    await user.click(await screen.findByRole('button', { name: '吊销' }))

    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '确认吊销' }))

    // 名称在行与详情面板各出现一次，定位到表格行断言其状态列变已吊销
    await waitFor(() => {
      const row = screen
        .getAllByText('业务管理后端')
        .map((el) => el.closest('tr'))
        .find((tr): tr is HTMLTableRowElement => tr !== null)
      expect(row).toBeDefined()
      expect(within(row as HTMLElement).getByText('已吊销')).toBeInTheDocument()
    })
  })
})
