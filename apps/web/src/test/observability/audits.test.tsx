// /audits 审计页测试：KPI + 列表渲染、空态、操作人筛选、详情打开、互跳 URL query 初始化（FR-157）、结果筛选。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { Route, Routes } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import AuditsPage from '../../pages/audits'
import CommandsPage from '../../pages/commands'
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

describe('/audits 审计页', () => {
  it('常规态渲染 KPI 与审计列表', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />)

    expect(await screen.findByText('审计总数')).toBeInTheDocument()
    // 出现已知审计动作中文标签（observability.audits.action 映射）
    expect((await screen.findAllByText(/确认接入身份|发起换区|启动变更单灰度/)).length).toBeGreaterThan(0)
  })

  it('空态给出无记录提示', async () => {
    useScenario('empty')
    renderPage(<AuditsPage />)

    expect(await screen.findByText('当前筛选条件下无审计记录')).toBeInTheDocument()
  })

  it('勾选「包含归档」冷查询：时间范围自动收敛 30 天、游标翻页取代页码，选回全部时间自动退出', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    await screen.findByText('审计总数')
    expect((await screen.findAllByText(/共 \d+ 条/)).length).toBeGreaterThan(0)

    // 勾选冷查询：时间范围从「全部」自动收敛 30 天，总数隐藏、出游标翻页
    await user.click(screen.getByRole('checkbox', { name: '包含归档' }))
    expect(await screen.findByText(/第 1 页（含归档）/)).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryAllByText(/共 \d+ 条/)).toHaveLength(0)
    })

    // 时间范围选回「全部」：冷查询强制有界范围 → 自动退出冷查询，回热分页与总数
    await user.click(screen.getByLabelText('时间范围'))
    await user.click(await screen.findByRole('option', { name: /全部/ }))
    await waitFor(() => {
      expect(screen.queryByText(/（含归档）/)).not.toBeInTheDocument()
    })
    expect((await screen.findAllByText(/共 \d+ 条/)).length).toBeGreaterThan(0)
  }, 20_000)

  it('列表行直接展示目标（targetRef），无需点开详情', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />)

    // 表头出现「目标」列（基础信息前置到行）
    expect(await screen.findByText('目标')).toBeInTheDocument()
    // 未点开任何行时右侧详情面板不渲染（列表行已可见目标，不产生模态遮罩）
    expect(screen.queryByText('审计详情')).not.toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('点击行打开审计详情', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    // 等待表格行渲染（中文动作标签；排除筛选下拉 option，只取有 tr 祖先的动作单元格）
    let row: HTMLElement | null = null
    await waitFor(() => {
      const cells = screen.getAllByText(/确认接入身份|发起换区|启动变更单灰度/)
      row = cells.map((el) => el.closest('tr')).find((tr): tr is HTMLTableRowElement => tr !== null) ?? null
      expect(row).not.toBeNull()
    })
    await user.click(row as unknown as HTMLElement)

    // 固定层详情出现（含来源 IP）；非 dialog，主表仍在
    await waitFor(() => {
      expect(screen.getByText('审计详情')).toBeInTheDocument()
    })
    expect(screen.getByText(/来源 IP|sourceIp|IP/i)).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByRole('table')).toBeInTheDocument()
  })

  it('URL 查询参数初始化筛选：targetRef 落进输入框并驱动服务端过滤（互跳承接，FR-157）', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />, ['/audits?targetRef=srv-none'])

    // 目标输入框以 URL 参数为初值
    expect(await screen.findByLabelText('目标（支持服务器 ID 子串）')).toHaveValue('srv-none')
    // 不存在的 targetRef → 服务端过滤后列表为空
    expect(await screen.findByText('当前筛选条件下无审计记录')).toBeInTheDocument()
  })

  it('URL action=message.payload.view 免交互定位 payload 查看审计（FR-157）', async () => {
    useScenario('normal')
    renderPage(<AuditsPage />, ['/audits?action=message.payload.view'])

    // 列表出现该动作的中文标签行（服务端按英文枚举过滤），其余动作不再出现在任何列表行内
    await waitFor(() => {
      const cells = screen
        .getAllByText('查看消息载荷')
        .filter((el) => el.closest('tr') !== null)
      expect(cells.length).toBeGreaterThan(0)
    })
    // 断言只针对列表行：KPI「最高频动作」卡片同样展示本地化动作名（不经服务端过滤），
    // 它出现「确认接入身份」属预期，不能算作列表未被过滤。
    const otherActionRows = screen.queryAllByText('确认接入身份').filter((el) => el.closest('tr') !== null)
    expect(otherActionRows).toHaveLength(0)
  })

  it('审计详情互跳命令观测：URL 带 serverId 且落位页筛选初始化生效（FR-157 贯通）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(
      <Routes>
        <Route path="/audits" element={<AuditsPage />} />
        <Route path="/commands" element={<CommandsPage />} />
      </Routes>,
      ['/audits'],
    )

    // 点开首条审计详情
    let row: HTMLElement | null = null
    await waitFor(() => {
      const cells = screen.getAllByText(/确认接入身份|发起换区|启动变更单灰度/)
      row = cells.map((el) => el.closest('tr')).find((tr): tr is HTMLTableRowElement => tr !== null) ?? null
      expect(row).not.toBeNull()
    })
    await user.click(row as unknown as HTMLElement)

    // 详情面板互跳链接带 targetRef 作 serverId 查询参数
    const link = await screen.findByRole('link', { name: /在命令观测中查看/ })
    const href = link.getAttribute('href') ?? ''
    const serverId = new URLSearchParams(href.split('?')[1] ?? '').get('serverId') ?? ''
    expect(serverId).not.toBe('')

    // 点击互跳 → 命令观测页挂载，serverId 搜索框以链接参数为初值（链路真正生效）
    await user.click(link)
    expect(await screen.findByText('命令历史')).toBeInTheDocument()
    expect(await screen.findByLabelText('搜索服务器 ID')).toHaveValue(serverId)
  })

  it('结果筛选：选「失败」请求带 result=fail，选回「全部」不带该参数', async () => {
    useScenario('normal')
    // 只捕获列表请求 URL（数据由服务端按 result 过滤，前端不做本地过滤），专注断言查询参数口径
    const seen: URL[] = []
    server.use(
      http.get('/admin/v1/audits', ({ request }) => {
        seen.push(new URL(request.url))
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    await screen.findByText('审计总数')
    await waitFor(() => {
      expect(seen.length).toBeGreaterThan(0)
    })
    // 初始「全部」：不带 result
    expect(seen[seen.length - 1]?.searchParams.get('result')).toBeNull()

    // 选「失败」→ 新请求带 result=fail（服务端过滤）
    const beforeFail = seen.length
    await user.click(screen.getByLabelText('结果'))
    await user.click(await screen.findByRole('option', { name: '失败' }))
    await waitFor(() => {
      expect(seen.length).toBeGreaterThan(beforeFail)
    })
    expect(seen[seen.length - 1]?.searchParams.get('result')).toBe('fail')

    // 选回「全部」→ 'all' 转 undefined，请求不再带 result
    const beforeAll = seen.length
    await user.click(screen.getByLabelText('结果'))
    await user.click(await screen.findByRole('option', { name: /全部/ }))
    await waitFor(() => {
      expect(seen.length).toBeGreaterThan(beforeAll)
    })
    expect(seen[seen.length - 1]?.searchParams.get('result')).toBeNull()
  }, 20_000)

  it('URL result=fail 初始化结果筛选并直接驱动服务端过滤（互跳承接）', async () => {
    useScenario('normal')
    const seen: URL[] = []
    server.use(
      http.get('/admin/v1/audits', ({ request }) => {
        seen.push(new URL(request.url))
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    renderPage(<AuditsPage />, ['/audits?result=fail'])

    await screen.findByText('审计总数')
    await waitFor(() => {
      expect(seen.some((url) => url.searchParams.get('result') === 'fail')).toBe(true)
    })
    // 空集（服务端按 result 过滤后无命中）走空态，说明筛选真的生效而不是被忽略
    expect(await screen.findByText('当前筛选条件下无审计记录')).toBeInTheDocument()
  }, 20_000)

  it('结果筛选：导出请求同样带 result=fail', async () => {
    useScenario('normal')
    const exported: URL[] = []
    server.use(
      http.get('/admin/v1/audits/export', ({ request }) => {
        exported.push(new URL(request.url))
        return new HttpResponse('id,result\n', {
          headers: { 'Content-Type': 'text/csv', 'Content-Disposition': 'attachment; filename="audits.csv"' },
        })
      }),
    )
    // jsdom 无下载语义：打桩 createObjectURL 与 a.click，让导出链路走完而不是在断言前中断
    Object.assign(URL, { createObjectURL: () => 'blob:mock', revokeObjectURL: () => undefined })
    const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => undefined)
    const user = userEvent.setup()
    renderPage(<AuditsPage />)

    await screen.findByText('审计总数')
    await user.click(screen.getByLabelText('结果'))
    await user.click(await screen.findByRole('option', { name: '失败' }))
    await user.click(screen.getByRole('button', { name: /导出 CSV/ }))

    await waitFor(() => {
      expect(exported.length).toBeGreaterThan(0)
    })
    // 导出与列表同口径：format + result 都在
    expect(exported[0]?.searchParams.get('format')).toBe('csv')
    expect(exported[0]?.searchParams.get('result')).toBe('fail')
    // 导出成功：不落错误提示
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    clickSpy.mockRestore()
  }, 20_000)
})
