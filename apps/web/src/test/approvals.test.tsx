import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { delay, http, HttpResponse } from 'msw'
import { setupServer } from 'msw/node'
import { MemoryRouter, Route, Routes } from 'react-router-dom'

import ApprovalsPage from '../pages/approvals'
import { setObservationScope } from '../features/env/observation-scope'
import { i18next } from '../i18n'
import { setLocale } from '../state/locale'
import { allHandlers, resetMockData, setMockScenario, type MockScenario } from '@beacon/devmock'

import '../i18n'

const server = setupServer(...allHandlers)

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})

afterEach(async () => {
  server.resetHandlers()
  // 观测范围是模块级单例，测试之间必须复位，否则会串味
  setObservationScope({ kind: 'all' })
  setLocale('zh-CN')
  await i18next.changeLanguage('zh-CN')
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

    expect(await screen.findByText('提审变更单 #5002：经济系统配置调优')).toBeInTheDocument()
    expect(screen.getByText('归档离线 backend-37，退出默认观测范围')).toBeInTheDocument()
    // 页面骨架：大标题 + 一行说明；说明明确本页不继承页眉范围（页内也不重复设置命名空间筛选）。
    expect(screen.getByRole('heading', { level: 1, name: '审批' })).toBeInTheDocument()
    expect(screen.getByText(/统一审批入口/)).toBeInTheDocument()
    expect(screen.getByText(/不继承页眉环境或命名空间筛选/)).toBeInTheDocument()
    // 筛选区为「常显一行 + 高级筛选折叠」：分组标题已移除，默认只露状态与关键字
    expect(screen.getByRole('button', { name: /高级筛选/ })).toBeInTheDocument()
    const statusFilter = screen.getByRole('textbox', { name: '审批状态' })
    expect(statusFilter).toHaveValue('待审批')
    expect(screen.getByRole('textbox', { name: '审批关键字' })).toBeInTheDocument()
    // 高级字段默认收起：时间窗输入不应外露
    expect(screen.queryByLabelText('创建时间起')).not.toBeInTheDocument()
    // 审批页不提供命名空间筛选控件，统一查看全局申请
    expect(screen.queryByRole('combobox', { name: '目标命名空间' })).not.toBeInTheDocument()
  })

  it('审批表头在英文界面显示英文列名', async () => {
    setLocale('en')
    await act(async () => {
      await i18next.changeLanguage('en')
    })
    renderPage()

    for (const label of ['Request ID', 'Summary', 'Domain', 'Requester', 'Status', 'Created at']) {
      expect(await screen.findByText(label)).toBeInTheDocument()
    }
  })

  it('桌面列表使用卡片内滚动并吸顶自定义表头', async () => {
    renderPage()
    expect(await screen.findByText('提审变更单 #5002：经济系统配置调优')).toBeInTheDocument()

    const scrollArea = document.querySelector('[data-slot="list-card-scroll"]')
    expect(scrollArea).toHaveClass('md:min-h-0', 'md:overflow-y-auto')
    expect(scrollArea).not.toHaveClass('max-h-[calc(100vh-22rem)]')
    expect(screen.getByText('申请编号').parentElement).toHaveClass('sticky', 'top-0', 'z-10')
  })

  it('审批状态保留预选标签并支持输入筛选', async () => {
    const urls: string[] = []
    server.use(
      http.get('*/admin/v2/approval-requests', ({ request }) => {
        urls.push(request.url)
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    renderPage()
    const user = userEvent.setup()
    const status = await screen.findByRole('textbox', { name: '审批状态' })

    expect(status).toHaveValue('待审批')
    await user.click(status)
    expect(await screen.findByRole('option', { name: '待审批' })).toHaveAttribute('aria-selected', 'true')
    await user.type(status, '执行中')
    await user.click(await screen.findByRole('option', { name: '执行中' }))

    await waitFor(() => {
      expect(status).toHaveValue('执行中')
      expect(urls.some((url) => new URL(url).searchParams.get('status') === 'executing')).toBe(true)
    })
  })

  it('高级审批枚举支持搜索并保留全部哨兵映射', async () => {
    const urls: string[] = []
    server.use(
      http.get('*/admin/v2/approval-requests', ({ request }) => {
        urls.push(request.url)
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    renderPage()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /高级筛选/ }))

    const risk = await screen.findByRole('textbox', { name: '风险级别' })
    expect(risk).toHaveValue('全部风险')
    await user.click(risk)
    expect(await screen.findByRole('option', { name: '全部风险' })).toHaveAttribute('aria-selected', 'true')
    await user.type(risk, '严重')
    await user.click(await screen.findByRole('option', { name: '严重' }))

    const requesterType = screen.getByRole('textbox', { name: '申请主体类型' })
    expect(requesterType).toHaveValue('全部主体')
    await user.click(requesterType)
    await user.type(requesterType, 'API 密钥')
    await user.click(await screen.findByRole('option', { name: 'API 密钥' }))

    await waitFor(() => {
      expect(risk).toHaveValue('严重')
      expect(requesterType).toHaveValue('API 密钥')
      expect(urls.some((url) => new URL(url).searchParams.get('riskLevel') === 'critical')).toBe(true)
      expect(urls.some((url) => new URL(url).searchParams.get('requesterType') === 'api_key')).toBe(true)
    })
  })

  it('全局审批列表不继承页眉命名空间范围，仍保留自身筛选和分页', async () => {
    const urls: string[] = []
    server.use(
      http.get('*/admin/v2/approval-requests', ({ request }) => {
        urls.push(request.url)
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    setObservationScope({ kind: 'env', envId: 1, namespaceId: 1 })
    renderPage()

    await waitFor(() => {
      expect(urls.length).toBeGreaterThan(0)
    })
    const requestUrl = new URL(urls[urls.length - 1] ?? '')
    expect(requestUrl.searchParams.has('namespaceId')).toBe(false)
    expect(requestUrl.searchParams.get('status')).toBe('pending')
    expect(requestUrl.searchParams.get('page')).toBe('1')
    expect(requestUrl.searchParams.get('pageSize')).toBe('20')
  })

  it('通过详情 deep-link 打开指定审批申请', async () => {
    renderPage(['/approvals/apr_change_9101'])

    const selectedRow = await screen.findByRole('button', { name: /apr_change_9101/ })
    expect(selectedRow).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('heading', { name: '风险与影响' })).toBeInTheDocument()
    expect(document.querySelector('[data-slot="detail-actions-footer"]')).toHaveClass('sticky', 'bottom-0', 'mt-auto')
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

  it('拒绝失败留在弹窗内，关闭后重新打开会清空原因与旧错误', async () => {
    server.use(
      http.post('*/admin/v2/approval-requests/apr_change_9101/reject', async () => {
        await delay(100)
        return HttpResponse.json(
          { code: 'temporary_failure', message: '模拟拒绝失败' },
          { status: 503 },
        )
      }),
    )
    renderPage(['/approvals/apr_change_9101'])
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: '拒绝' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByLabelText('原因'), '第一次拒绝理由')
    await user.click(within(dialog).getByRole('button', { name: '拒绝' }))

    expect(await within(dialog).findByText('模拟拒绝失败')).toBeInTheDocument()
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '取消' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument())

    await user.click(screen.getByRole('button', { name: '拒绝' }))
    const reopened = await screen.findByRole('alertdialog')
    expect(within(reopened).queryByText('模拟拒绝失败')).not.toBeInTheDocument()
    expect(within(reopened).getByLabelText('原因')).toHaveValue('')
    expect(within(reopened).getByRole('button', { name: '拒绝' })).toBeDisabled()
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

    // 状态筛选项已改为组件库 Select（不再渲染原生 option 文本），
    // 因此这条断言改为只认状态徽标：详情确实被刷新成了「执行中」。
    expect((await screen.findAllByText('执行中', { selector: '[data-slot="badge"]' })).length).toBeGreaterThan(0)
    expect(detailReads).toBeGreaterThan(1)
  })

  it('快照漂移时禁用批准并给出重新申请原因', async () => {
    renderPage(['/approvals/apr_drift_9106'])

    expect(await screen.findByText('当前事实已漂移，需按当前事实重新申请')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '批准并执行' })).toBeDisabled()
  })

  it('执行中申请轮询到最终结果', async () => {
    renderPage(['/approvals/apr_config_9103'])

    // 状态文案同时出现在状态徽标与详情时间线里，断言钉在状态徽标上避免多义
    expect(await screen.findByText('执行中', { selector: '[data-slot="badge"]' })).toBeInTheDocument()
    expect(await screen.findByText('已完成', { selector: '[data-slot="badge"]' })).toBeInTheDocument()
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
