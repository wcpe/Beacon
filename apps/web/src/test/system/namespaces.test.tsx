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
  it('常规态渲染 命名空间 列表与信任关系', async () => {
    useScenario('normal')
    renderPage(<NamespacesPage />)

    // 命名空间 列表出现已知 namespace（种子数据含 default）
    expect(await screen.findByText(/强隔离/)).toBeInTheDocument()
    // 至少渲染一行 namespace
    await waitFor(() => {
      expect(screen.getAllByRole('row').length).toBeGreaterThan(1)
    })
  })

  it('空态给出 命名空间 创建引导', async () => {
    useScenario('empty')
    renderPage(<NamespacesPage />)

    expect(
      await screen.findByText('暂无 命名空间，点击「创建 namespace」新增第一个'),
    ).toBeInTheDocument()
  })

  it('创建 命名空间 后弹出一次性接入 token（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    await user.click(await screen.findByRole('button', { name: '创建 命名空间' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('名称'), 'game-new')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    expect(await screen.findByText('命名空间 已创建')).toBeInTheDocument()
    // 一次性明文接入 token 以 nstk_ 前缀
    const tokenDialog = screen.getByRole('dialog')
    expect(within(tokenDialog).getByText(/^nstk_/)).toBeInTheDocument()
  })

  it('点击 命名空间 行展开非模态详情面板（不产生遮罩）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    // 初始无详情、无 dialog
    expect(screen.queryByText('命名空间 详情')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    // 选中 test 域（种子含出向生效信任 test → prod）
    await user.click(await screen.findByText('test'))

    // 固定层详情出现（非 role=dialog）
    expect(await screen.findByText('命名空间 详情')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // 面板内出现互通信任关系区与授予入口
    expect(screen.getByText('互通信任关系')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '授予信任' })).toBeInTheDocument()
  })

  it('详情面板收回生效信任后该关系变为已收回（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    // 选中 test 域后在详情面板内发起收回
    await user.click(await screen.findByText('test'))
    const revokeBtn = (await screen.findAllByRole('button', { name: '收回' }))[0]
    await user.click(revokeBtn)

    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByLabelText('收回原因'), '业务下线不再需要跨域')
    await user.click(within(dialog).getByRole('button', { name: '确认收回' }))

    // 收回后 test 域再无生效信任，详情面板不再出现收回按钮
    await waitFor(() => {
      expect(screen.queryByRole('button', { name: '收回' })).not.toBeInTheDocument()
    })
    expect(screen.getAllByText('已收回').length).toBeGreaterThan(0)
  })
})
