// /mcp-invocations 页面测试（FR-241）：四态（空 / 常规 / 超大量 / 异常）+ 六维筛选 + 游标翻页 +
// 危险操作视图 + 详情抽屉。
//
// 期望值取自 devmock 的流水数据集（packages/devmock/src/domains/mcp.ts：常规 18 行、huge 620 行），
// 断言口径与 §3.2 / §3.4 / §3.7 的字段语义一一对应（结果三态、四档风险、六种原因码）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import McpInvocationsPage from '../../pages/mcp-invocations'
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

// 数据行 = 除表头外的所有 <tr>（空态 / 错误态下的行也计入，需要时用文案断言区分）
function dataRows(): HTMLElement[] {
  return screen.getAllByRole('row').slice(1)
}

// 单行第 N 列文本（列序：时间 / 工具名 / 客户端 ID / 结果 / 风险等级 / 原因 / 耗时）
function cellText(row: HTMLElement, index: number): string {
  return within(row).getAllByRole('cell')[index]?.textContent ?? ''
}

// 选择筛选项：FilterSelect 触发器带 aria-label，「全部」项的文案形如「结果 · 全部」
async function pickFilter(user: ReturnType<typeof userEvent.setup>, label: string, option: string): Promise<void> {
  await user.click(screen.getByRole('combobox', { name: label }))
  await user.click(await screen.findByRole('option', { name: option }))
}

describe('/mcp-invocations MCP 工具调用流水页', () => {
  it('常规态：整页渲染，危险行（被拒 / 关键风险）带视觉标记，末页不渲染分页条', async () => {
    useScenario('normal')
    renderPage(<McpInvocationsPage />)

    // 常规数据集 18 行，一页装得下
    expect(await screen.findByText('beacon.config.publish')).toBeInTheDocument()
    expect(dataRows()).toHaveLength(18)

    // 汇总口径是「本页」而不是全局总数（后端不返回 total）
    expect(screen.getByText('本页条数')).toBeInTheDocument()
    expect(screen.getByText('本页被拒').parentElement?.textContent).toContain('6')
    expect(screen.getByText('本页关键风险').parentElement?.textContent).toContain('4')
    expect(screen.getByText('本页失败').parentElement?.textContent).toContain('4')

    // 危险行整行标红（被拒的撞目录行 + 关键风险的行），普通成功行不标
    const rejectedRow = screen.getByText('beacon.ops.undeclared_tool').closest('tr')
    // update_apply 有「失败 + 关键风险」与「被拒 + 生产模式」两行，取第一行即可（两行都属危险行）
    const criticalRow = screen.getAllByText('beacon.system.update_apply')[0]
    expect(rejectedRow?.className).toContain('bg-crit-bg')
    expect(criticalRow.closest('tr')?.className).toContain('bg-crit-bg')
    expect(screen.getByText('beacon.messages.list').closest('tr')?.className).not.toContain('bg-crit-bg')

    // 末页（nextCursor 为空串）不渲染分页条
    expect(screen.queryByRole('button', { name: '下一页' })).toBeNull()
    expect(screen.queryByText(/第 1 页/)).toBeNull()
  })

  it('空态：无流水时给出空态文案而非错误', async () => {
    useScenario('empty')
    renderPage(<McpInvocationsPage />)

    expect(await screen.findByText('当前条件下无工具调用流水')).toBeInTheDocument()
    expect(screen.queryByText('beacon.config.publish')).toBeNull()
    expect(screen.queryByText(/加载失败/)).toBeNull()
  })

  it('异常态：列表加载失败给出错误态且不渲染数据行', async () => {
    useScenario('error')
    renderPage(<McpInvocationsPage />)

    await waitFor(() => {
      expect(screen.getByText(/模拟内部错误/)).toBeInTheDocument()
    })
    expect(screen.queryByText('beacon.config.publish')).toBeNull()
    expect(screen.queryAllByRole('row')).toHaveLength(0)
  })

  it('超大量：游标翻页第 1→2 页无重复、可返回第 1 页', async () => {
    useScenario('huge')
    const user = userEvent.setup()
    renderPage(<McpInvocationsPage />)

    // 首页：单页 20 条 + 「第 1 页」页序（流水无总数，故只给页序不给总页数）
    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
    expect(dataRows()).toHaveLength(20)
    const page1 = dataRows().map((row) => cellText(row, 0))

    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText(/第 2 页$/)).toBeInTheDocument()
    const page2 = dataRows().map((row) => cellText(row, 0))
    expect(page2).toHaveLength(20)
    // 时间列行内唯一且倒序：两页交集为空即无重复无错位
    expect(page2.filter((time) => page1.includes(time))).toHaveLength(0)

    await user.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
    expect(dataRows().map((row) => cellText(row, 0))).toEqual(page1)
  })

  it('筛选：结果收窄为被拒，叠加风险等级可进一步收窄并可回到全部', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<McpInvocationsPage />)
    await screen.findByText('beacon.config.publish')

    await pickFilter(user, '结果', '被拒')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(6)
    })
    for (const row of dataRows()) {
      expect(cellText(row, 3)).toBe('被拒')
    }

    // 被拒 ∩ 关键风险 = 生产模式拒执的两行
    await pickFilter(user, '风险等级', '关键')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(2)
    })
    for (const row of dataRows()) {
      expect(cellText(row, 4)).toBe('关键')
    }

    // 风险等级是独立维度：清掉结果筛选后关键风险共 4 行（含 1 条成功、1 条失败）
    await pickFilter(user, '结果', '结果 · 全部')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(4)
    })
    expect(dataRows().map((row) => cellText(row, 3))).toContain('成功')

    await pickFilter(user, '风险等级', '风险等级 · 全部')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(18)
    })
  })

  it('筛选：工具名 / 客户端 ID / 原因 / 时间范围各自精确收窄', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<McpInvocationsPage />)
    await screen.findByText('beacon.config.publish')

    // 工具名精确匹配（含成功与被拒各一行）
    await user.type(screen.getByLabelText('工具名'), 'beacon.approvals.approve')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(2)
    })
    await user.clear(screen.getByLabelText('工具名'))
    await waitFor(() => {
      expect(dataRows()).toHaveLength(18)
    })

    // 客户端 ID（第三个 mock 客户端共 5 行）
    await user.type(screen.getByLabelText('客户端 ID'), 'mcp_4e8b2a17c93d05f6ab73')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(5)
    })
    await user.clear(screen.getByLabelText('客户端 ID'))
    await waitFor(() => {
      expect(dataRows()).toHaveLength(18)
    })

    // 原因码：生产模式拒执两行
    await pickFilter(user, '原因', '生产模式拒执')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(2)
    })
    await pickFilter(user, '原因', '原因 · 全部')

    // 时间范围：近 24 小时排除昨日三行（近 7 天则全量）
    await pickFilter(user, '时间范围', '近 24 小时')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(15)
    })
    await pickFilter(user, '时间范围', '近 7 天')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(18)
    })
  })

  it('危险操作视图：一键收窄为被拒或关键风险（OR 判据，不是交集）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<McpInvocationsPage />)
    await screen.findByText('beacon.config.publish')

    // 开关上带当前结果为危险操作的条数（6 条被拒 ∪ 4 条关键风险 = 8 行）
    const dangerToggle = screen.getByRole('checkbox', { name: '仅看危险操作' })
    expect(dangerToggle.closest('label')?.textContent).toContain('8')

    await user.click(dangerToggle)
    await waitFor(() => {
      expect(dataRows()).toHaveLength(8)
    })
    // 收窄口径常驻可见：明确这是「当前筛选结果内」的二次过滤
    expect(screen.getByText(/在当前筛选结果内收窄为/)).toBeInTheDocument()
    // 被拒行（撞目录）与「结果成功但关键风险」的行同时在列 → 判据是 OR
    expect(screen.getByText('beacon.ops.undeclared_tool')).toBeInTheDocument()
    expect(screen.getAllByText('beacon.approvals.approve')).toHaveLength(2)
    // 低风险成功行被收窄掉
    expect(screen.queryByText('beacon.messages.list')).toBeNull()
    // 收窄后每行都是危险行 → 整行标记保持
    for (const row of dataRows()) {
      expect(row.className).toContain('bg-crit-bg')
    }

    // 叠加服务端筛选（风险等级=关键）后按交集收窄：关键风险 4 行全部命中（其中 2 行同时被拒）
    await pickFilter(user, '风险等级', '关键')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(4)
    })
    for (const row of dataRows()) {
      expect(cellText(row, 4)).toBe('关键')
    }
  })

  it('危险操作视图：当前结果内没有危险操作时给专属空态文案', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<McpInvocationsPage />)
    await screen.findByText('beacon.config.publish')

    // 先筛到「只剩低风险行」（低风险档里没有被拒、也没有关键风险），再开危险视图 →
    // 得到与「真的没有数据」区分开的专属空态
    await pickFilter(user, '风险等级', '低')
    await waitFor(() => {
      expect(dataRows()).toHaveLength(6)
    })
    await user.click(screen.getByRole('checkbox', { name: '仅看危险操作' }))

    expect(await screen.findByText('当前筛选结果内没有被拒或关键风险调用')).toBeInTheDocument()
    expect(screen.queryByText('当前条件下无工具调用流水')).toBeNull()
  })

  it('详情：点击行打开抽屉，展示字段全集与脱敏摘要口径说明', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<McpInvocationsPage />)
    await screen.findByText('beacon.config.publish')

    await user.click(screen.getByText('beacon.config.publish'))
    const drawer = await screen.findByRole('complementary', { name: '调用详情' })

    // 字段全集（含脱敏摘要：目标摘要与参数键摘要都是摘要形态，不是参数正文）
    expect(await within(drawer).findByText('目标摘要')).toBeInTheDocument()
    expect(
      within(drawer).getByText('namespaceCode=lobby;path=plugins/Essentials/config.yml'),
    ).toBeInTheDocument()
    expect(within(drawer).getByText('参数键摘要')).toBeInTheDocument()
    expect(within(drawer).getByText('comment:24,namespaceCode:5,path:35')).toBeInTheDocument()
    expect(within(drawer).getByText('客户端 ID')).toBeInTheDocument()
    expect(within(drawer).getByText('mcp_c18d4b70a6e93f25c0b1')).toBeInTheDocument()
    expect(within(drawer).getByText('Trace ID')).toBeInTheDocument()
    expect(within(drawer).getByText('耗时')).toBeInTheDocument()
    // 脱敏口径常驻，避免误以为能展开看到参数原文
    expect(within(drawer).getByText(/参数正文与结果正文都不入库/)).toBeInTheDocument()

    // 换一条被拒行：原因码与错误 / 拒绝摘要如实呈现
    await user.click(within(drawer).getByRole('button', { name: '关闭' }))
    await waitFor(() => {
      expect(screen.queryByRole('complementary', { name: '调用详情' })).toBeNull()
    })
    await user.click(screen.getByText('beacon.ops.undeclared_tool'))
    const rejectedDrawer = await screen.findByRole('complementary', { name: '调用详情' })
    // 原因码与风险等级在头部药丸与字段行各出现一次，故用 getAllByText
    expect(await within(rejectedDrawer).findAllByText('未登记工具')).not.toHaveLength(0)
    expect(within(rejectedDrawer).getAllByText('未登记')).not.toHaveLength(0)
    expect(within(rejectedDrawer).getByText('错误 / 拒绝摘要')).toBeInTheDocument()
    expect(within(rejectedDrawer).getByText('unknown tool: beacon.ops.undeclared_tool')).toBeInTheDocument()
  })
})
