// /changes/history 交付历史页测试：常规渲染历史单、空态引导、进入详情看单服状态、整单回滚写闭环。
// 命名空间范围自本页移除选择器后取自页眉观测范围选择器（真源 features/env/observation-scope），
// 因此用例统一在 beforeEach 里把范围固定到 mock 的 prod 命名空间（id=1）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import ChangesHistoryPage from '../../pages/changes-history'
import { getObservationScopeSelection, setObservationScope } from '../../features/env/observation-scope'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
beforeEach(() => {
  // mock 数据仓的历史单都在 prod 命名空间（id=1）；env 1（生产）映射到该 namespace
  setObservationScope({ kind: 'env', envId: 1, namespaceId: 1 })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

describe('/changes/history 交付历史页', () => {
  it('常规态渲染历史变更单列表', async () => {
    useScenario('normal')
    renderPage(<ChangesHistoryPage />)

    // 页头：大标题 + 一行解释
    expect(await screen.findByRole('heading', { level: 1, name: '交付历史' })).toBeInTheDocument()
    expect(
      screen.getByText(/归档已结束的变更单/),
    ).toBeInTheDocument()
    // 已完成单出现
    expect(await screen.findByText('全网核心配置基线对齐')).toBeInTheDocument()
    // 已回滚单出现
    expect(await screen.findByText('坏更新整单回滚示例')).toBeInTheDocument()
  })

  // 空态引导只在「命名空间存在但一单都没有」时出现（empty 场景连命名空间都没有，
  // 那时历史页无主体可查，所以用常规场景把列表接口打成空来验证引导本身）
  it('空态给出引导文案', async () => {
    useScenario('normal')
    server.use(http.get('*/admin/v2/change-orders', () => HttpResponse.json({ items: [], total: 0 })))
    renderPage(<ChangesHistoryPage />)

    // 空态引导：说明历史从哪来 + 「去变更单」动作
    expect(await screen.findByText('还没有历史记录')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /去变更单/ })).toHaveAttribute('href', '/changes')
  })

  it('点行打开右侧非模态详情面板并展示执行回放（批次状态机 + 单服状态）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesHistoryPage />)

    const row = (await screen.findByText('全网核心配置基线对齐')).closest('tr')
    expect(row).not.toBeNull()
    await user.click(row as HTMLElement)

    // 默认「执行回放」Tab：批次状态机只读回放 + 单服状态表，且未产生模态遮罩
    expect(await screen.findByText('批次状态')).toBeInTheDocument()
    expect(await screen.findByText('第 1 批')).toBeInTheDocument()
    expect(await screen.findByText('单服状态')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // 只读回放：不出现放行按钮
    expect(screen.queryByRole('button', { name: '确认放行下一批' })).not.toBeInTheDocument()
    expect(document.querySelector('[data-slot="detail-actions-footer"]')).toHaveClass('sticky', 'bottom-0', 'mt-auto')
  })

  it('历史详情复用共享控件：变更内容 / 交付编排 / 进度时间线可完整追溯', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesHistoryPage />)

    const row = (await screen.findByText('全网核心配置基线对齐')).closest('tr')
    await user.click(row as HTMLElement)
    await screen.findByText('批次状态')

    // 变更内容 Tab：共享变更内容预览（当时改了什么）
    await user.click(screen.getByRole('tab', { name: '变更内容' }))
    expect(await screen.findByText('配置变更清单（1 项）')).toBeInTheDocument()

    // 交付编排 Tab：共享编排预览（发给谁 / 怎么编排）
    await user.click(screen.getByRole('tab', { name: '交付编排' }))
    expect(await screen.findByText('目标范围')).toBeInTheDocument()
    expect(screen.getByText('批次规划')).toBeInTheDocument()
    expect(screen.getByText('生效方式')).toBeInTheDocument()

    // 进度时间线 Tab：共享双模式时间线（出过什么事）
    await user.click(screen.getByRole('tab', { name: '进度时间线' }))
    const timelinePanel = within(await screen.findByRole('tabpanel'))
    expect(await timelinePanel.findByText('变更单 · 已完成')).toBeInTheDocument()
    await user.click(timelinePanel.getByRole('button', { name: '详细' }))
    expect(await timelinePanel.findByRole('columnheader', { name: '序号' })).toBeInTheDocument()
  }, 20_000)

  it('行内前置基础字段可见 + 吸顶筛选存在', async () => {
    useScenario('normal')
    renderPage(<ChangesHistoryPage />)

    await screen.findByText('全网核心配置基线对齐')
    expect(screen.getByRole('columnheader', { name: '单号' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '结束时间' })).toBeInTheDocument()
    expect(screen.getByRole('combobox', { name: '按状态过滤' })).toHaveValue('全部状态')
  })

  it('历史状态筛选支持键入过滤并映射查询参数', async () => {
    useScenario('normal')
    const urls: string[] = []
    server.use(
      http.get('*/admin/v2/change-orders', ({ request }) => {
        urls.push(request.url)
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<ChangesHistoryPage />)
    const status = await screen.findByRole('combobox', { name: '按状态过滤' })

    expect(status).toHaveValue('全部状态')
    await user.click(status)
    expect(await screen.findByRole('option', { name: '全部状态' })).toHaveAttribute('aria-selected', 'true')
    await user.type(status, '已回滚')
    await user.click(await screen.findByRole('option', { name: '已回滚' }))

    await waitFor(() => {
      expect(status).toHaveValue('已回滚')
      expect(urls.some((url) => new URL(url).searchParams.get('status') === 'rolled_back')).toBe(true)
    })
  })

  it('整单回滚走审批申请：历史详情提交后只出票据反馈，单据状态不变', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesHistoryPage />)

    // 进入已完成单详情
    const row = (await screen.findByText('全网核心配置基线对齐')).closest('tr')
    await user.click(row as HTMLElement)
    await screen.findByText('单服状态')

    // 触发整单回滚申请（共享控件 order-rollback，/changes 与历史页同源）
    await user.click(await screen.findByRole('button', { name: '整单回滚' }))
    const dialog = await screen.findByRole('alertdialog')
    // 高摩擦：手输复述「回滚」 + 原因（两个 textbox：phrase / reason）
    const textboxes = within(dialog).getAllByRole('textbox')
    await user.type(textboxes[0], '回滚')
    await user.type(textboxes[1], '新版本异常，回滚')
    await user.click(within(dialog).getByRole('button', { name: '确认回滚' }))

    // 真机回滚是申请动作（202 票据）：只有票据反馈，批准前单据不变、也不出现「人工结束回滚」
    expect(await screen.findByText(/已提交审批（申请号/)).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    })
    expect(screen.queryByRole('button', { name: '人工结束回滚' })).not.toBeInTheDocument()
    // 详情头部仍为已完成（不是回滚中 / 已回滚）
    expect(screen.getAllByText('已完成').length).toBeGreaterThan(0)
  }, 20_000)

  it('页眉未选具体命名空间时保留全局范围并隐藏历史列表与回滚入口', async () => {
    useScenario('normal')
    setObservationScope({ kind: 'all' })
    renderPage(<ChangesHistoryPage />)

    expect(await screen.findByText('请先在页眉左侧选择命名空间')).toBeInTheDocument()
    expect(screen.queryByText('全网核心配置基线对齐')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '整单回滚' })).not.toBeInTheDocument()
    expect(getObservationScopeSelection()).toEqual({ kind: 'all' })
  })

  // 兜底：确实没有任何可用命名空间（env 列表为空）时不给列表 / 详情，只引导去页眉选择
  it('无可用命名空间时给出引导且不渲染可回滚的详情入口', async () => {
    useScenario('normal')
    setObservationScope({ kind: 'all' })
    // 没有任何 env / namespace 映射：默认值无从补齐，页面必须回到引导而非硬猜
    server.use(http.get('*/admin/v2/envs', () => HttpResponse.json({ items: [], total: 0 })))
    renderPage(<ChangesHistoryPage />)

    expect(await screen.findByText('请先在页眉左侧选择命名空间')).toBeInTheDocument()
    // 列表与详情都不渲染：既看不到历史单，也没有可点开的「整单回滚」
    expect(screen.queryByText('全网核心配置基线对齐')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '整单回滚' })).not.toBeInTheDocument()
  })
})
