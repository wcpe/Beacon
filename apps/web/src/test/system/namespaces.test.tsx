// /namespaces 页测试：常规渲染、空态引导、创建出一次性 token、收回信任后状态变化、
// 接入 token 轮换（FR-238）、展示名 / 描述编辑（FR-239）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
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
    expect(screen.getByRole('heading', { name: '命名空间生命周期评审（Mock）' })).toBeInTheDocument()
    // 至少渲染一行 namespace
    await waitFor(() => {
      expect(screen.getAllByRole('row').length).toBeGreaterThan(1)
    })
  })

  it('空态给出 命名空间 创建引导', async () => {
    useScenario('empty')
    renderPage(<NamespacesPage />)

    expect(
      await screen.findByText('暂无命名空间，点击「创建命名空间」新增第一个'),
    ).toBeInTheDocument()
  })

  it('创建 命名空间 后弹出一次性接入 token（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    await user.click(await screen.findByRole('button', { name: '创建命名空间' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('业务标识'), 'game-new')
    await user.type(within(dialog).getByLabelText('显示名称'), '新游戏环境')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    expect(await screen.findByText('命名空间已创建')).toBeInTheDocument()
    // 一次性明文接入 token 以 nstk_ 前缀
    const tokenDialog = screen.getByRole('dialog')
    expect(within(tokenDialog).getByText(/^nstk_/)).toBeInTheDocument()
  })

  it('点击 命名空间 行展开非模态详情面板（不产生遮罩）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    // 初始无详情、无 dialog
    expect(screen.queryByText('命名空间详情')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    // 选中 test 域（种子含出向生效信任 test → prod）
    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)

    // 固定层详情出现（非 role=dialog）
    expect(await screen.findByText('命名空间详情')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // 面板内出现互通信任关系区与授予入口
    expect(screen.getByText('互通信任关系')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '授予信任' })).toBeInTheDocument()
  })

  it('授予信任只创建审批申请，等待审批中心执行', async () => {
    useScenario('normal')
    server.use(
      http.post('/admin/v2/namespace-trusts', () =>
        HttpResponse.json(
          { approvalRequestId: 'apr_trust_test', status: 'pending', operationKey: 'namespace_trust.grant' },
          { status: 202 },
        ),
      ),
    )
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const prodRow = (await screen.findAllByText('prod'))[0].closest('tr')
    expect(prodRow).not.toBeNull()
    await user.click(prodRow as HTMLElement)
    await user.click(await screen.findByRole('button', { name: '授予信任' }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('combobox', { name: '来源命名空间' }))
    await user.click(await screen.findByRole('option', { name: 'prod' }))
    await user.click(within(dialog).getByRole('combobox', { name: '目标命名空间' }))
    await user.click(await screen.findByRole('option', { name: 'test' }))
    await user.type(within(dialog).getByLabelText('建立原因'), '联调跨域调度')
    await user.click(within(dialog).getByRole('button', { name: '授予' }))

    const status = await screen.findByRole('status')
    expect(within(status).getByText('信任授予审批申请已创建，等待审批中心执行。')).toBeInTheDocument()
    expect(within(status).getByRole('link', { name: '查看统一审批' })).toHaveAttribute('href', '/approvals/apr_trust_test')
  })

  it('详情面板收回生效信任后该关系变为已收回（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    // 选中 test 域后在详情面板内发起收回
    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)
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

  it('详情面板提供接入 token 轮换入口', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)

    expect(await screen.findByRole('button', { name: '轮换接入 token' })).toBeInTheDocument()
    // 轮换前不出现任何破坏性确认弹窗
    expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
  })

  it('轮换接入 token 须手输 code 二次确认，成功后一次性展示新明文（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)
    await user.click(await screen.findByRole('button', { name: '轮换接入 token' }))

    // 破坏性确认：影响摘要显式说明旧 token 立即失效、新明文仅一次
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/旧 token 在写入新哈希的那一刻起立即失效/)).toBeInTheDocument()
    expect(within(dialog).getByText(/全部 agent 在换用新 token 前，请求一律 401/)).toBeInTheDocument()

    // 手输复述闸：未输入 / 输错均禁用确认
    const phraseInput = () => within(dialog).getByLabelText('输入命名空间 code 以确认轮换')
    expect(within(dialog).getByRole('button', { name: '确认轮换' })).toBeDisabled()
    await user.type(phraseInput(), 'wrong')
    expect(within(dialog).getByRole('button', { name: '确认轮换' })).toBeDisabled()

    // 输对 code 放行 → 轮换结果复用一次性 token 弹窗展示新明文
    await user.clear(phraseInput())
    await user.type(phraseInput(), 'test')
    expect(within(dialog).getByRole('button', { name: '确认轮换' })).toBeEnabled()
    await user.click(within(dialog).getByRole('button', { name: '确认轮换' }))

    const tokenDialog = await screen.findByRole('dialog')
    expect(within(tokenDialog).getByText(/^nstk_/)).toBeInTheDocument()
  })

  it('轮换确认框取消则不发请求（二次确认可撤销）', async () => {
    useScenario('normal')
    let rotateCalls = 0
    server.use(
      http.post('/admin/v2/namespaces/:id/token/rotate', () => {
        rotateCalls += 1
        return HttpResponse.json({}, { status: 200 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)
    await user.click(await screen.findByRole('button', { name: '轮换接入 token' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '取消' }))

    await waitFor(() => {
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    })
    expect(rotateCalls).toBe(0)
  })

  it('详情面板提供展示名编辑入口，且入口本身不弹窗（FR-239）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)

    expect(await screen.findByRole('button', { name: '编辑' })).toBeInTheDocument()
    // 未点开前不产生任何模态
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('编辑弹窗中业务标识只读，并显式说明其不可变', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)
    await user.click(await screen.findByRole('button', { name: '编辑' }))

    const dialog = await screen.findByRole('dialog')
    // 稳定业务标识：只读，值即当前 code
    const codeInput = within(dialog).getByLabelText('业务标识')
    expect(codeInput).toHaveAttribute('readonly')
    expect(codeInput).toHaveValue('test')
    // UI 上说明不可变（改动会被服务端 IMMUTABLE_IDENTIFIER 拒绝）
    expect(within(dialog).getByText(/创建后不可修改/)).toBeInTheDocument()
    // 对照组：展示名可编辑
    expect(within(dialog).getByLabelText('显示名称')).not.toHaveAttribute('readonly')
  })

  it('编辑保存后列表与详情面板同步显示新显示名与描述（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)
    await user.click(await screen.findByRole('button', { name: '编辑' }))

    const dialog = await screen.findByRole('dialog')
    const displayName = within(dialog).getByLabelText('显示名称')
    await user.clear(displayName)
    await user.type(displayName, '测试主域')
    const description = within(dialog).getByLabelText('描述（可选）')
    await user.clear(description)
    await user.type(description, '预发验证与回归专用')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    // 保存成功后弹窗关闭，重新拉取的列表与详情面板都取到新值
    await waitFor(() => {
      expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    })
    expect((await screen.findAllByText('测试主域')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('预发验证与回归专用').length).toBeGreaterThan(0)
    // 稳定业务标识保持原值不变
    expect(screen.getAllByText('test').length).toBeGreaterThan(0)
  })

  it('编辑保存只提交展示层字段，稳定标识 code 不进请求体', async () => {
    useScenario('normal')
    const bodies: Record<string, unknown>[] = []
    server.use(
      http.patch('/admin/v2/namespaces/:id', async ({ request }) => {
        bodies.push((await request.json()) as Record<string, unknown>)
        return HttpResponse.json(
          {
            id: 2,
            name: 'test',
            code: 'test',
            displayName: '测试主域',
            description: '预发验证与回归专用',
            serverCount: 0,
            bcClusterCount: 0,
            activeTrustCount: 0,
            createdAt: new Date(0).toISOString(),
          },
          { status: 200 },
        )
      }),
    )
    const user = userEvent.setup()
    renderPage(<NamespacesPage />)

    const testRow = (await screen.findAllByText('test'))[0].closest('tr')
    expect(testRow).not.toBeNull()
    await user.click(testRow as HTMLElement)
    await user.click(await screen.findByRole('button', { name: '编辑' }))

    const dialog = await screen.findByRole('dialog')
    const displayName = within(dialog).getByLabelText('显示名称')
    await user.clear(displayName)
    await user.type(displayName, '测试主域')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    await waitFor(() => {
      expect(bodies.length).toBe(1)
    })
    // 请求体仅含可变字段：code 从不参与提交，故不可能触发 IMMUTABLE_IDENTIFIER
    expect(bodies[0]).toEqual({ displayName: '测试主域', description: '测试环境（预发验证）' })
  })
})
