// /namespaces 页测试：常规渲染、空态引导、创建出一次性 token、收回信任后状态变化。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import NamespacesPage from '../../pages/namespaces'
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

describe('/namespaces 页', () => {
  it('常规态渲染 namespace 列表与信任关系', async () => {
    useScenario('normal')
    renderPage(<NamespacesPage />)

    // namespace 列表出现已知 namespace（种子数据含 default）
    expect(await screen.findByText(/强隔离/)).toBeInTheDocument()
    // 至少渲染一行 namespace
    await waitFor(() => {
      expect(screen.getAllByRole('row').length).toBeGreaterThan(1)
    })
  })

  it('空态给出 namespace 创建引导', async () => {
    useScenario('empty')
    renderPage(<NamespacesPage />)

    expect(
      await screen.findByText('暂无 namespace，点击「创建 namespace」新增第一个'),
    ).toBeInTheDocument()
  })

  it('创建 namespace 后弹出一次性接入 token（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    await user.click(await screen.findByRole('button', { name: '创建 namespace' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('名称'), 'game-new')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    expect(await screen.findByText('namespace 已创建')).toBeInTheDocument()
    // 一次性明文接入 token 以 nstk_ 前缀
    const tokenDialog = screen.getByRole('dialog')
    expect(within(tokenDialog).getByText(/^nstk_/)).toBeInTheDocument()
  })

  it('收回生效信任后该行状态变为已收回（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    // 种子含 test → prod 的生效信任，定位其行的收回按钮
    const revokeBtn = (await screen.findAllByRole('button', { name: '收回' }))[0]
    await user.click(revokeBtn)

    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByLabelText('收回原因'), '业务下线不再需要跨域')
    await user.click(within(dialog).getByRole('button', { name: '确认收回' }))

    // 收回后该三元组不再出现生效状态的收回按钮（本用例断言列表出现「已收回」）
    await waitFor(() => {
      expect(screen.getAllByText('已收回').length).toBeGreaterThan(0)
    })
  })
})
