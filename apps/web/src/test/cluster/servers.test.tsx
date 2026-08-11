// /servers 页测试（主从布局）：主体资产列表常规渲染、空态引导、待确认抽屉 approve 写闭环、
// keyword 筛选。待确认收敛到吸顶入口 → 抽屉里处理，故 approve 用例先开抽屉。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ServersPage from '../../pages/servers'
import IdentityDetailSheet from '../../pages/servers/identity-detail-sheet'
import { createTestServer, renderPage, useScenario } from './harness'
import { uuidFrom } from '@beacon/devmock/support'

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

async function findServerRow(serverId: string): Promise<HTMLElement> {
  const matches = await screen.findAllByText(serverId)
  const row = matches.map((element) => element.closest('tr')).find((element): element is HTMLTableRowElement => element !== null)
  if (!row) throw new Error(`未找到服务器 ${serverId} 的资产行`)
  return row
}

describe('/servers 服务器页', () => {
  it('常规态渲染服务器资产列表，待确认入口带计数', async () => {
    useScenario('normal')
    renderPage(<ServersPage />)

    // 资产列表出现已知子服
    expect(await findServerRow('lobby-1')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '服务器生命周期评审（Mock）' })).toBeInTheDocument()
    // 吸顶入口按钮存在（待确认收敛为入口，不再默认铺开）
    const pendingBtn = await screen.findByRole('button', { name: /注册待确认/ })
    expect(pendingBtn).toBeInTheDocument()
  }, 20_000)

  it('空态给出资产列表接入引导', async () => {
    useScenario('empty')
    renderPage(<ServersPage />)

    expect(await screen.findByText('当前筛选条件下无服务器')).toBeInTheDocument()
  })

  it('打开待确认抽屉后创建审批申请并保留待确认行，直到 worker 执行', async () => {
    useScenario('normal')
    server.use(
      http.post('/admin/v2/agent-identities/:identityId/approve', () =>
        HttpResponse.json({ approvalRequestId: 'apr_identity_test', status: 'pending', operationKey: 'identity.agent.approve' }, { status: 202 }),
      ),
    )
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    // 等主体加载完，再从吸顶入口打开待确认抽屉
    await findServerRow('lobby-1')
    await user.click(await screen.findByRole('button', { name: /注册待确认/ }))

    // 抽屉内定位 game-new-1 所在待确认行的确认按钮
    const pendingRow = (await screen.findByText('game-new-1')).closest('tr')
    expect(pendingRow).not.toBeNull()
    const approveBtn = within(pendingRow as HTMLElement).getByRole('button', { name: '确认接入' })
    await user.click(approveBtn)

    // 弹窗确认（确认按钮文案为「确认接入」）
    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByLabelText('原因'), '确认测试接入')
    await user.click(within(dialog).getByRole('button', { name: '确认接入' }))

    const status = await screen.findByRole('status')
    expect(within(status).getByText('确认接入审批申请已创建，等待审批中心执行。')).toBeInTheDocument()
    expect(within(status).getByRole('link', { name: '查看统一审批' })).toHaveAttribute('href', '/approvals/apr_identity_test')
    expect(screen.getByText('game-new-1')).toBeInTheDocument()
  }, 20_000)

  it('待分配身份要求显式填写服务器 ID，并展示绑定详情', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    await user.click(await screen.findByRole('button', { name: /注册待确认/ }))
    const pendingRow = (await screen.findByText('待分配服务器 ID')).closest('tr')
    expect(pendingRow).not.toBeNull()

    await user.click(within(pendingRow as HTMLElement).getByRole('button', { name: '确认接入' }))
    const approveDialog = await screen.findByRole('alertdialog')
    const confirm = within(approveDialog).getByRole('button', { name: '确认接入' })
    expect(confirm).toBeDisabled()

    const serverId = within(approveDialog).getByLabelText('服务器 ID')
    await user.type(serverId, 'bad id')
    expect(await within(approveDialog).findByText('服务器 ID 不能包含空白字符')).toBeInTheDocument()
    expect(confirm).toBeDisabled()

    await user.clear(serverId)
    await user.type(serverId, 'lobby-new-1')
    expect(confirm).toBeDisabled()
    await user.type(within(approveDialog).getByLabelText('原因'), '确认测试接入')
    expect(confirm).toBeEnabled()
    await user.click(within(approveDialog).getByRole('button', { name: '取消' }))

    await user.click(within(pendingRow as HTMLElement).getByRole('button', { name: '查看身份详情' }))
    const detailTitle = await screen.findByText('身份详情')
    const detailSheet = detailTitle.closest('[data-slot="sheet-content"]')
    expect(detailSheet).not.toBeNull()
    expect(within(detailSheet as HTMLElement).getByText('待分配服务器 ID')).toBeInTheDocument()
    expect(within(detailSheet as HTMLElement).getByText('管理端分配')).toBeInTheDocument()
    expect(within(detailSheet as HTMLElement).getAllByText('未提供').length).toBeGreaterThan(0)
  })

  it('行操作切换默认入口：取消后该行徽标消失（FR-48/ADR-0067 写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    // game-1 在 mock 中为小区默认入口：行内带「默认入口」徽标；操作收进「…」菜单
    const row = await findServerRow('game-1')
    expect(within(row).getByText('默认入口')).toBeInTheDocument()
    // 打开行内操作菜单再点「取消默认入口」
    await user.click(within(row).getByRole('button', { name: '操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '取消默认入口' }))

    // 无原因确认框：确认后徽标消失、菜单项翻转
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '取消默认入口' }))
    await waitFor(() => {
      const fresh = screen.getAllByText('game-1').map((element) => element.closest('tr')).find((element): element is HTMLTableRowElement => element !== null)
      expect(fresh).toBeDefined()
      expect(within(fresh as HTMLElement).queryByText('默认入口')).not.toBeInTheDocument()
    })
    const fresh = await findServerRow('game-1')
    await user.click(within(fresh).getByRole('button', { name: '操作' }))
    expect(await screen.findByRole('menuitem', { name: '设为默认入口' })).toBeInTheDocument()
  }, 20_000)

  it('取消排空只创建审批申请，直到审批 worker 执行', async () => {
    useScenario('normal')
    server.use(
      http.put('/admin/v2/servers/:serverId/draining', () =>
        HttpResponse.json(
          { approvalRequestId: 'apr_draining_test', status: 'pending', operationKey: 'topology.draining.disable' },
          { status: 202 },
        ),
      ),
    )
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    const row = await findServerRow('survival-1')
    await user.click(within(row).getByRole('button', { name: '操作' }))
    await user.click(await screen.findByRole('menuitem', { name: '取消排空' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByLabelText('原因'), '维护窗口')
    await user.click(within(dialog).getByRole('button', { name: '取消排空' }))

    const status = await screen.findByRole('status')
    expect(within(status).getByText('排空审批申请已创建，等待审批中心执行。')).toBeInTheDocument()
    expect(within(status).getByRole('link', { name: '查看统一审批' })).toHaveAttribute('href', '/approvals/apr_draining_test')
  })

  it('列表行直显健康分/等级/实时指标与不可调度原因摘要', async () => {
    useScenario('normal')
    renderPage(<ServersPage />)

    // lobby-1 是独立大厅成员：资产页只展示归属，不复用小区默认入口或未分配摘要。
    const lobbyRow = await findServerRow('lobby-1')
    expect(within(lobbyRow).getByText('大厅成员')).toBeInTheDocument()
    expect(within(lobbyRow).queryByText('- / -')).not.toBeInTheDocument()
    expect(within(lobbyRow).queryByText('默认入口')).not.toBeInTheDocument()
    await waitFor(() => {
      expect(within(lobbyRow).getByText('87')).toBeInTheDocument()
    })
    expect(within(lobbyRow).getByText('健康')).toBeInTheDocument()
    await waitFor(() => {
      expect(within(lobbyRow).getByText(/TPS/)).toBeInTheDocument()
    })
    expect(within(lobbyRow).getByText(/CPU/)).toBeInTheDocument()
    expect(within(lobbyRow).getByText(/人在线/)).toBeInTheDocument()
    // 可调度按例外呈现：可调度行不出现不可调度药丸
    expect(within(lobbyRow).queryByText(/不可调度/)).not.toBeInTheDocument()

    // proxy-1（代理，类型不可调度）：直显不可调度原因摘要
    const proxyRow = await findServerRow('proxy-1')
    expect(within(proxyRow).getByText(/类型不可调度/)).toBeInTheDocument()

    // game-4（失联）：失联徽标 + 指标列占位不显示误导性旧值
    const lostRow = await findServerRow('game-4')
    expect(within(lostRow).getByText('失联')).toBeInTheDocument()
    expect(within(lostRow).getByText('—')).toBeInTheDocument()
  })

  it('点行打开健康详情抽屉：面板已加宽且展示因子分解', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    await user.click(await findServerRow('lobby-1'))

    // 抽屉打开并加载因子分解内容
    expect(await screen.findByText('因子分解')).toBeInTheDocument()
    // 加宽类已应用（jsdom 无布局，按既有约定锁类名断言）
    const content = document.querySelector('[data-slot="sheet-content"]')
    expect(content).not.toBeNull()
    expect((content as HTMLElement).className).toContain('max-w-[min(32rem,90vw)]')
  })

  it('keyword 搜索按 serverId 过滤资产列表', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServersPage />)

    // 初始能看到 lobby-1 与 mall-1
    expect(await findServerRow('lobby-1')).toBeInTheDocument()

    const searchBox = screen.getByLabelText('搜索服务器 ID 或显示名称')
    await user.type(searchBox, 'mall')

    await waitFor(() => {
      expect(screen.queryAllByText('mall-1').some((element) => element.closest('tr') !== null)).toBe(true)
      expect(screen.queryAllByText('lobby-1').every((element) => element.closest('tr') === null)).toBe(true)
    })
  })

  it('身份详情按平台代理展示地址列表，并可填写原因设置与清除覆盖', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<IdentityDetailSheet identityId={uuidFrom('identity:1:proxy-1')} onOpenChange={() => undefined} />)

    expect(await screen.findByText('监听地址（2）')).toBeInTheDocument()
    expect(screen.getByText('BC listener 1')).toBeInTheDocument()
    expect(screen.getAllByText('自动探测地址').length).toBeGreaterThan(0)
    await user.click(screen.getAllByRole('button', { name: '设置覆盖' })[0])
    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByLabelText('覆盖地址'), '198.51.100.20:25577')
    await user.type(within(dialog).getByLabelText('原因'), '公网入口地址修正')
    await user.click(within(dialog).getByRole('button', { name: '保存覆盖' }))
    expect((await screen.findAllByText('198.51.100.20:25577')).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: '清除覆盖' }))
    const clearDialog = await screen.findByRole('alertdialog')
    await user.type(within(clearDialog).getByLabelText('原因'), '恢复自动探测地址')
    await user.click(within(clearDialog).getByRole('button', { name: '清除覆盖' }))
    expect((await screen.findAllByText('自动探测地址')).length).toBeGreaterThan(0)
  })

  it('失活 listener 默认折叠，但仍显示最后上报时间并可展开详情', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<IdentityDetailSheet identityId={uuidFrom('identity:1:proxy-2')} onOpenChange={() => undefined} />)

    expect(await screen.findByText('已失活')).toBeInTheDocument()
    expect(screen.getAllByText('最后上报时间')).toHaveLength(2)
    expect(screen.getAllByText('上报监听')).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: '展开已失活监听' }))
    expect(screen.getAllByText('上报监听')).toHaveLength(2)
  })
})
