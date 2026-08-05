import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import ApprovalsPage from '../pages/approvals'
import { allHandlers, resetMockData, setMockScenario, type MockScenario } from '@beacon/devmock'

import '../i18n'

const server = setupServer(...allHandlers)

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})

afterEach(() => {
  server.resetHandlers()
})

afterAll(() => {
  server.close()
})

function renderPage(initialEntries: string[] = ['/approvals'], scenario: MockScenario = 'normal'): void {
  setMockScenario(scenario)
  resetMockData()
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={initialEntries}>
        <Routes>
          <Route path="/approvals" element={<ApprovalsPage />} />
          <Route path="/approvals/:requestId" element={<ApprovalsPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('/approvals 审批中心', () => {
  it('渲染待审批列表并保留机器主体行', async () => {
    renderPage()

    expect(await screen.findByText('启动反作弊组件热更，目标 12 台 backend')).toBeInTheDocument()
    expect(screen.getByText('归档离线 backend-37，退出默认观测范围')).toBeInTheDocument()
    expect(screen.getByText('全局审批不继承页眉环境或 namespace scope；以下 namespace 仅为页面筛选。')).toBeInTheDocument()
  })

  it('通过详情 deep-link 打开指定审批申请', async () => {
    renderPage(['/approvals/apr_change_9101'])

    const selectedRow = await screen.findByRole('button', { name: /apr_change_9101/ })
    expect(selectedRow).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('heading', { name: '风险与影响' })).toBeInTheDocument()
  })

  it('批准动作只调用统一审批批准端点', async () => {
    let called = false
    server.use(
      http.post('*/admin/v2/approval-requests/apr_change_9101/approve', ({ request }) => {
        called = request.method === 'POST'
        return HttpResponse.json({ requestId: 'apr_change_9101', status: 'executing' }, { status: 202 })
      }),
    )
    renderPage(['/approvals/apr_change_9101'])
    const user = userEvent.setup()

    await screen.findByText('风险与影响')
    await user.click(screen.getByRole('button', { name: '批准并执行' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '批准并执行' }))

    expect(called).toBe(true)
  })

  it('机器主体隐藏批准和拒绝动作', async () => {
    renderPage(['/approvals/apr_server_9102'])

    expect(await screen.findByText('当前主体没有可执行的审批操作。')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '批准并执行' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '拒绝' })).not.toBeInTheDocument()
  })

  it('空态区分全局暂无待审批', async () => {
    renderPage(['/approvals'], 'empty')

    expect(await screen.findByText('全局暂无待审批')).toBeInTheDocument()
  })

  it('超大量态保持服务端分页', async () => {
    const user = userEvent.setup()
    renderPage(['/approvals'], 'huge')

    expect((await screen.findAllByText(/共 \d+ 条/)).length).toBeGreaterThan(1)
    expect(screen.getByText('共 255 条')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText(/第 2 \/ \d+ 页/)).toBeInTheDocument()
  })

  it('异常态保留筛选并显示列表重试入口', async () => {
    renderPage(['/approvals'], 'error')

    expect(await screen.findByText('审批列表加载失败')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
  })

  it('拒绝要求原因并调用统一拒绝端点', async () => {
    let reason = ''
    server.use(
      http.post('*/admin/v2/approval-requests/apr_change_9101/reject', async ({ request }) => {
        reason = (await request.json() as { reason: string }).reason
        return HttpResponse.json({ requestId: 'apr_change_9101', status: 'rejected' })
      }),
    )
    renderPage(['/approvals/apr_change_9101'])
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: '拒绝' }))
    const dialog = await screen.findByRole('alertdialog')
    const confirm = within(dialog).getByRole('button', { name: '拒绝' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByLabelText('原因'), '风险窗口冲突')
    await user.click(confirm)

    await waitFor(() => {
      expect(reason).toBe('风险窗口冲突')
    })
  })

  it('申请人可撤回自己的 pending 申请', async () => {
    let called = false
    server.use(
      http.post('*/admin/v2/approval-requests/apr_withdraw_9108/withdraw', () => {
        called = true
        return HttpResponse.json({ requestId: 'apr_withdraw_9108', status: 'withdrawn' })
      }),
    )
    renderPage(['/approvals/apr_withdraw_9108'])
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: '撤回' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '撤回' }))

    expect(called).toBe(true)
  })

  it('并发 409 后刷新服务器详情', async () => {
    let detailReads = 0
    server.use(
      http.get('*/admin/v2/approval-requests/apr_change_9101', () => {
        detailReads += 1
        return HttpResponse.json(approvalDetail(detailReads > 1 ? 'executing' : 'pending'))
      }),
      http.post('*/admin/v2/approval-requests/apr_change_9101/approve', () =>
        HttpResponse.json({ code: 'approval_state_changed', message: '审批状态已变化' }, { status: 409 }),
      ),
    )
    renderPage(['/approvals/apr_change_9101'])
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: '批准并执行' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '批准并执行' }))

    expect((await screen.findAllByText('执行中')).length).toBeGreaterThan(1)
    expect(detailReads).toBeGreaterThan(1)
  })

  it('快照漂移时禁用批准并给出重新申请原因', async () => {
    renderPage(['/approvals/apr_drift_9106'])

    expect(await screen.findByText('当前事实已漂移，需按当前事实重新申请')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '批准并执行' })).toBeDisabled()
  })

  it('执行中申请轮询到最终结果', async () => {
    renderPage(['/approvals/apr_config_9103'])

    expect(await screen.findByText('执行中')).toBeInTheDocument()
    expect(await screen.findByText('已完成')).toBeInTheDocument()
  }, 5_000)
})

function approvalDetail(status: 'pending' | 'executing') {
  return {
    id: 9101,
    requestId: 'apr_change_9101',
    operationKey: 'delivery.change_order.start',
    operationKind: 'delivery.change_order.start',
    resourceType: 'change_order',
    resourceId: '5003',
    riskLevel: 'high',
    status,
    requestReason: '按变更窗口执行',
    safeSummary: '启动反作弊组件热更，目标 12 台 backend',
    frozenPayloadSha256: 'safe-hash',
    requesterType: 'human',
    requesterId: 'ops-chen',
    deciderType: status === 'pending' ? null : 'human',
    deciderId: status === 'pending' ? null : 'admin',
    rejectReason: null,
    decisionReason: null,
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
    version: 1,
    namespaceId: 1,
    canApprove: status === 'pending',
    canReject: status === 'pending',
    canWithdraw: false,
    frozenPayloadSummary: [],
    currentFactsSummary: [],
    currentDiff: [],
    timeline: [],
  }
}
