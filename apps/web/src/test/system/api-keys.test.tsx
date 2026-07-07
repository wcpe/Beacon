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

  it('吊销生效密钥后状态变为已吊销（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ApiKeysPage />)

    const row = (await screen.findByText('业务管理后端')).closest('tr')
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: '吊销' }))

    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '确认吊销' }))

    await waitFor(() => {
      const updated = screen.getByText('业务管理后端').closest('tr')
      expect(within(updated as HTMLElement).getByText('已吊销')).toBeInTheDocument()
    })
  })
})
