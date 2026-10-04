// /alert-events 告警事件页测试：KPI + 列表渲染、空态、处理写闭环（标记已读 / 标记处理 / 403 错误展示）、
// 跨页一键操作（一键已读 / 一键处理）入口与禁用态。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AlertEventsPage from '../../pages/alert-events'
import { setObservationScope } from '../../features/env/observation-scope'
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

// 详情面板（MasterDetail 的 fixed 侧栏）：aside 隐式 role=complementary，aria-label 取 detailTitle
function detailDrawer(): HTMLElement {
  return screen.getByRole('complementary', { name: '告警详情' })
}

// 打开首条「待处理」告警的详情面板（写闭环用例共用步骤）
async function openFirstOpenAlert(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  let row: HTMLTableRowElement | null = null
  await waitFor(() => {
    const badges = screen.getAllByText('待处理')
    const tr = badges.map((el) => el.closest('tr')).find((r): r is HTMLTableRowElement => r !== null) ?? null
    expect(tr).not.toBeNull()
    row = tr
  })
  await user.click(row as unknown as HTMLElement)
  await waitFor(() => {
    expect(screen.getByText('告警详情')).toBeInTheDocument()
  })
}

describe('/alert-events 告警事件页', () => {
  it('常规态渲染 KPI 与告警列表', async () => {
    useScenario('normal')
    renderPage(<AlertEventsPage />)

    expect(await screen.findByText('告警总数')).toBeInTheDocument()
    // 健康流转摘要已 i18n：亚健康/失联/在线 等中文态 + 箭头
    expect(
      (await screen.findAllByText(/亚健康\s*→\s*失联|在线\s*→\s*亚健康|失联\s*→\s*离线|在线\s*→\s*失联/)).length,
    ).toBeGreaterThan(0)
  })

  it('空态给出无记录提示', async () => {
    useScenario('empty')
    renderPage(<AlertEventsPage />)

    expect(await screen.findByText('当前筛选条件下无告警事件')).toBeInTheDocument()
  })

  it('点行开右侧非模态详情面板并标记已读（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)

    // 等待列表首屏，找到第一条「待处理」告警行
    let row: HTMLTableRowElement | null = null
    await waitFor(() => {
      const badges = screen.getAllByText('待处理')
      const tr = badges.map((el) => el.closest('tr')).find((r): r is HTMLTableRowElement => r !== null) ?? null
      expect(tr).not.toBeNull()
      row = tr
    })

    // 点行 → 固定层详情出现（非 dialog，主表不 reflow）
    await user.click(row as unknown as HTMLElement)
    await waitFor(() => {
      expect(screen.getByText('告警详情')).toBeInTheDocument()
    })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByRole('table')).toBeInTheDocument()

    // 面板内点「标记已读」完成写闭环
    await user.click(within(detailDrawer()).getByRole('button', { name: '标记已读' }))

    // 详情面板内状态徽标更新为「已读」（选中行从最新数据派生；KPI / 筛选下拉的同名文案不在抽屉内）
    await waitFor(() => {
      expect(within(detailDrawer()).getAllByText('已读').length).toBeGreaterThan(0)
    })
  })

  it('填写备注标记处理：处理人与备注即时可见（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)
    await openFirstOpenAlert(user)

    // 备注未填时「标记处理」禁用（resolved 备注必填约束在面板）
    const resolveBtn = within(detailDrawer()).getByRole('button', { name: '标记处理' })
    expect(resolveBtn).toBeDisabled()

    await user.type(screen.getByLabelText('处理备注'), '已重启 agent')
    await user.click(resolveBtn)

    // 写成功 → 列表 invalidate → 面板从最新数据派生出处理人与备注
    expect(await screen.findByText('处理人')).toBeInTheDocument()
    expect(screen.getByText('已重启 agent')).toBeInTheDocument()
  }, 20_000)

  it('处理失败（readonly 403）时面板展示后端脱敏错误文案（不静默）', async () => {
    useScenario('normal')
    // 覆写 handle 端点模拟 readonly 守卫 403（真后端 readonlyWriteGuard 行为）
    server.use(
      http.post('*/admin/v1/alert-events/:id/handle', () =>
        HttpResponse.json(
          { code: 'forbidden', message: '只读模式禁止写操作', traceId: 'trace-test' },
          { status: 403 },
        ),
      ),
    )
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)
    await openFirstOpenAlert(user)

    await user.click(within(detailDrawer()).getByRole('button', { name: '标记已读' }))

    // 后端脱敏 message 原样展示在面板内，错误不被静默（ADR-0057）
    expect(await screen.findByText('只读模式禁止写操作')).toBeInTheDocument()
    // 告警仍为待处理，处理表单未消失（可重试）
    expect(within(detailDrawer()).getByRole('button', { name: '标记已读' })).toBeInTheDocument()
  })
})

// 一键操作（FR-229）：两个并列主操作直接暴露在工具条上，不再让「一键处理」藏在弹窗入口里。
describe('/alert-events 跨页一键操作', () => {
  it('工具条并列给出「一键已读」与「一键处理」两个主操作', async () => {
    useScenario('normal')
    renderPage(<AlertEventsPage />)
    await screen.findByText('告警总数')

    const ackAll = screen.getByRole('button', { name: '一键已读' })
    const resolveAll = screen.getByRole('button', { name: '一键处理' })
    // 两者并列于同一工具条容器内，且都不是 ghost 弱按钮
    expect(ackAll.parentElement).toBe(resolveAll.parentElement)
    expect(ackAll.getAttribute('data-variant')).toBe('default')
    expect(resolveAll.getAttribute('data-variant')).toBe('outline')
    // 作用范围写在 title 上（当前筛选命中的全部待处理条目、跨页）
    expect(ackAll.getAttribute('title')).toContain('跨页')
    expect(resolveAll.getAttribute('title')).toContain('跨页')
    // 列表返回 total > 0 两个操作即可用
    await waitFor(() => {
      expect(ackAll).toBeEnabled()
    })
    expect(resolveAll).toBeEnabled()
  })

  it('一键处理直接打开原因填写弹窗，原因必填后才提交批量处理', async () => {
    useScenario('normal')
    const bodies: { status?: string; note?: string }[] = []
    server.use(
      http.post('/admin/v1/alert-events/handle', async ({ request }) => {
        bodies.push((await request.json()) as { status?: string; note?: string })
        return HttpResponse.json({ affected: 3 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)
    await screen.findByText('告警总数')

    await user.click(screen.getByRole('button', { name: '一键处理' }))

    // 原因必填：未填时确认按钮禁用（逻辑不变，入口改为直接可见）
    const dialog = await screen.findByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: '一键处理' })
    expect(confirm).toBeDisabled()

    await user.type(within(dialog).getByPlaceholderText('批量处理备注（标记处理时必填）'), '已重启 agent')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => {
      expect(bodies.length).toBeGreaterThan(0)
    })
    expect(bodies[0]?.status).toBe('resolved')
    expect(bodies[0]?.note).toBe('已重启 agent')
  })

  it('无匹配告警时两个一键按钮禁用并直接给出原因', async () => {
    useScenario('empty')
    renderPage(<AlertEventsPage />)
    await screen.findByText('当前筛选条件下无告警事件')

    expect(screen.getByRole('button', { name: '一键已读' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '一键处理' })).toBeDisabled()
    expect(screen.getByText('当前筛选无匹配告警，一键操作不可用')).toBeInTheDocument()
  })
})

// FR-229 guard：按筛选批量必须携带页眉观测范围（envId），不得越出当前范围写。
describe('/alert-events 批量按筛选的观测范围 guard', () => {
  it('选择环境后批量携带 envId（不越界）', async () => {
    useScenario('normal')
    const seen: string[] = []
    server.use(
      http.post('/admin/v1/alert-events/handle', ({ request }) => {
        seen.push(request.url)
        return HttpResponse.json({ affected: 0 })
      }),
    )
    setObservationScope({ kind: 'env', envId: 1 })
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)
    await screen.findByText('告警总数')
    await user.click(await screen.findByRole('button', { name: '一键已读' }))
    await waitFor(() => {
      expect(seen.length).toBeGreaterThan(0)
    })
    expect(seen[0]).toContain('envId=1')
    setObservationScope({ kind: 'all' })
  })

  it('「全部环境」时批量不带 envId（全量，与列表一致）', async () => {
    useScenario('normal')
    const seen: string[] = []
    server.use(
      http.post('/admin/v1/alert-events/handle', ({ request }) => {
        seen.push(request.url)
        return HttpResponse.json({ affected: 0 })
      }),
    )
    setObservationScope({ kind: 'all' })
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)
    await screen.findByText('告警总数')
    await user.click(await screen.findByRole('button', { name: '一键已读' }))
    await waitFor(() => {
      expect(seen.length).toBeGreaterThan(0)
    })
    expect(seen[0]).not.toContain('envId=')
  })

  it('选择环境后列表请求按该 env 的 namespace 收窄（FR-178 回归：此前静默不收窄）', async () => {
    useScenario('normal')
    const seen: string[] = []
    server.use(
      http.get('/admin/v1/alert-events', ({ request }) => {
        seen.push(request.url)
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    setObservationScope({ kind: 'env', envId: 1 })
    renderPage(<AlertEventsPage />)
    await screen.findByText('告警总数')
    await waitFor(() => {
      expect(seen.some((u) => u.includes('namespace=prod'))).toBe(true)
    })
    // 不得出现无 scope 的全量请求被当作收窄后的结果
    expect(seen.some((u) => !u.includes('namespace='))).toBe(false)
    setObservationScope({ kind: 'all' })
  })

  it('已选具体环境但 env 选项仍在加载时显示骨架，不误报空态', async () => {
    useScenario('empty')
    // env 选项永久挂起：此间 namespace 作用域无法解析（fail-closed 空集合）
    server.use(
      http.get('/admin/v2/envs', async () => {
        await delay('infinite')
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    setObservationScope({ kind: 'env', envId: 1 })
    renderPage(<AlertEventsPage />)

    // 骨架在场（loading 态）
    await waitFor(() => {
      expect(document.querySelectorAll('[data-slot="skeleton"]').length).toBeGreaterThan(0)
    })
    // 且不得把「范围解析中」渲染成空记录提示
    expect(screen.queryByText('当前筛选条件下无告警事件')).toBeNull()
    setObservationScope({ kind: 'all' })
  })
})
