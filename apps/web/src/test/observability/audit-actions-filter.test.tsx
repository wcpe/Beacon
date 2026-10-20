// 审计页动作筛选 UI 守护（issue #152）
//
// 守护三件事：
//   1. 候选项覆盖后端全部动作（= 生成物 + 「全部」），运维不再选不到动作；
//   2. 选中动作后请求真的带上 action=（筛选链路未被控件替换破坏）；
//   3. URL 带入的未知 action 仍能回显（withCurrent 兜底行为保持，不因清单收紧而丢）。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AuditsPage from '../../pages/audits'
import { AUDIT_ACTIONS } from '../../pages/audits/audit-actions.generated'
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

// 只捕获列表请求 URL（数据由服务端按 action 过滤，前端不做本地过滤），专注断言查询参数口径
function captureAuditListRequests(): URL[] {
  const seen: URL[] = []
  server.use(
    http.get('/admin/v1/audits', ({ request }) => {
      seen.push(new URL(request.url))
      return HttpResponse.json({ items: [], total: 0 })
    }),
  )
  return seen
}

describe('/audits 动作筛选与后端枚举同源', () => {
  it('候选覆盖后端全部动作（157 项 + 全部），且含旧手写清单缺失的动作', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    // 触发器回显「全部」（Combobox 严格选模式：关闭时显示当前选中项文案）
    const actionInput = await screen.findByRole('textbox', { name: '动作' })
    expect(actionInput).toHaveValue('全部')

    // 展开候选：数量必须等于生成物全量 +1（「全部」首项），这是「选得到」的核心断言
    await user.click(actionInput)
    const options = await screen.findAllByRole('option')
    expect(options).toHaveLength(AUDIT_ACTIONS.length + 1)

    // 抽样断言历史上漏掉的动作已在候选里（issue 原文点名的几个）
    for (const label of ['拉取实例日志', '浏览实例文件', '对比文件资产差异', '发布文件']) {
      expect(screen.getByRole('option', { name: label })).toBeInTheDocument()
    }
  }, 20_000)

  it('键入英文枚举子串可过滤候选，选中后请求带 action=', async () => {
    useScenario('normal')
    const seen = captureAuditListRequests()
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    const actionInput = await screen.findByRole('textbox', { name: '动作' })
    await screen.findByText('审计总数')

    // 键入英文枚举片段：Combobox 按 value（枚举）或 label（中文）子串命中，
    // 便于熟悉后端枚举的运维直接打动作名，不必记中文译名
    await user.click(actionInput)
    await user.type(actionInput, 'tail-logs')
    const matched = await screen.findByRole('option', { name: '拉取实例日志' })
    await user.click(matched)

    await waitFor(() => {
      expect(seen.some((url) => url.searchParams.get('action') === 'instance.tail-logs')).toBe(true)
    })
    // 触发器回显中文标签，确认选中态真的落到受控值上
    await waitFor(() => {
      expect(actionInput).toHaveValue('拉取实例日志')
    })
  }, 20_000)

  it('选回「全部」后请求不再带 action=', async () => {
    useScenario('normal')
    const seen = captureAuditListRequests()
    const user = userEvent.setup()
    renderPage(<AuditsPage />, ['/audits?action=instance.tail-logs'])

    const actionInput = await screen.findByRole('textbox', { name: '动作' })
    await waitFor(() => {
      expect(seen.some((url) => url.searchParams.get('action') === 'instance.tail-logs')).toBe(true)
    })

    // 选回「全部」→ 'all' 转 undefined，请求不再带 action
    const beforeAll = seen.length
    await user.click(actionInput)
    await user.click(await screen.findByRole('option', { name: '全部' }))
    await waitFor(() => {
      expect(seen.length).toBeGreaterThan(beforeAll)
      expect(seen[seen.length - 1]?.searchParams.get('action')).toBeNull()
    })
  }, 20_000)

  it('URL 带入的未知 action 仍能回显并驱动过滤（清单收紧不挡互跳旧链接）', async () => {
    useScenario('normal')
    const seen = captureAuditListRequests()
    renderPage(<AuditsPage />, ['/audits?action=legacy.removed-action'])

    // 未知动作不在生成物里，经 withCurrent 并入候选首位并以原文回显（不显示成「全部」）
    const actionInput = await screen.findByRole('textbox', { name: '动作' })
    await waitFor(() => {
      expect(actionInput).toHaveValue('legacy.removed-action')
    })
    // 服务端照旧按该值过滤（空集 → 空态），说明值真的传下去了
    await waitFor(() => {
      expect(seen.some((url) => url.searchParams.get('action') === 'legacy.removed-action')).toBe(true)
    })
    expect(await screen.findByText('当前筛选条件下无审计记录')).toBeInTheDocument()
  }, 20_000)
  it('键盘可用：方向键移动高亮、回车选中候选', async () => {
    useScenario('normal')
    const seen = captureAuditListRequests()
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    const actionInput = await screen.findByRole('textbox', { name: '动作' })
    await screen.findByText('审计总数')

    // 该筛选器候选量级大（后端全部动作），原先只能鼠标点选；
    // 这里锁住「键盘可打开、可移动高亮、可回车选中」这条路径。
    await user.click(actionInput)
    await user.type(actionInput, 'tail-logs')
    await screen.findByRole('option', { name: '拉取实例日志' })

    await user.keyboard('{ArrowDown}')
    // 高亮项通过 aria-activedescendant 暴露给读屏
    await waitFor(() => {
      expect(actionInput).toHaveAttribute('aria-activedescendant')
    })

    await user.keyboard('{Enter}')
    await waitFor(() => {
      expect(seen.some((url) => url.searchParams.get('action') === 'instance.tail-logs')).toBe(true)
    })
  }, 20_000)

})
