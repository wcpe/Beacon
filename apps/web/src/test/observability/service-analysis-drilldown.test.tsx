// /service-analysis 下钻子视图测试（调度决策 + 健康快照回放）：
// 四态齐备——常规（列表 / 详情 / 过滤 / 快照卡）、空态、错误态、超大量分页；另验 ?view= 板块定位。
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ServiceAnalysisPage from '../../pages/service-analysis'
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

// 带初始路由渲染（验 ?view= 板块定位；harness.renderPage 不暴露 initialEntries）
function renderAt(url: string): void {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[url]}>
        <ServiceAnalysisPage />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('/service-analysis 调度决策下钻板块', () => {
  it('常规态：切到调度决策板块出分页列表与筛选（无需先选服）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '调度决策' }))

    // 时间窗 / serverId / 结果筛选齐备，devmock 固定种子 48 条 → 服务端分页
    expect(await screen.findByLabelText('时间范围')).toBeInTheDocument()
    expect(screen.getByLabelText('搜索 serverId（发起方或选中）')).toBeInTheDocument()
    expect(screen.getByLabelText('结果')).toBeInTheDocument()
    expect((await screen.findAllByText(/共 48 条/)).length).toBeGreaterThan(0)
    // 列表表头（原因摘要列）出现
    expect(screen.getByText('原因摘要')).toBeInTheDocument()
  })

  it('点行打开右侧详情面板：决策上下文 + 逐台排除原因', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '调度决策' }))
    await screen.findAllByText(/共 48 条/)

    // 取列表首个数据行（表头行之后）点击
    const rows = screen.getAllByRole('row')
    expect(rows.length).toBeGreaterThan(1)
    await user.click(rows[1])

    // 右侧非模态详情面板：标题 + 决策上下文字段 + 逐台排除原因区
    await waitFor(() => {
      expect(screen.getByText('决策详情')).toBeInTheDocument()
    })
    expect(screen.getByText('发起方')).toBeInTheDocument()
    expect(screen.getByText('逐台排除原因')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('按结果筛选失败后列表仅剩失败决策', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '调度决策' }))
    await screen.findAllByText(/共 48 条/)

    // 选择结果=失败：组件库 Select 先展开触发器再点选项
    await user.click(screen.getByLabelText('结果'))
    await user.click(await screen.findByRole('option', { name: '失败' }))

    // 筛选生效：总数缩小且列表不再出现「成功」药丸
    await waitFor(() => {
      expect(screen.queryAllByText(/共 48 条/)).toHaveLength(0)
    })
    expect(screen.queryByText('成功')).not.toBeInTheDocument()
    expect(screen.getAllByText('失败').length).toBeGreaterThan(0)
  })

  it('空态给出无调度决策提示', async () => {
    useScenario('empty')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '调度决策' }))

    expect(await screen.findByText('当前时间窗与筛选条件下无调度决策')).toBeInTheDocument()
  })

  it('错误态展示脱敏错误文案（不静默）', async () => {
    useScenario('error')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '调度决策' }))

    // AsyncSection 错误分支：展示「加载失败：<脱敏真因>」
    expect((await screen.findAllByText(/加载失败：/)).length).toBeGreaterThan(0)
  })

  it('超大量走服务端分页且可翻页', async () => {
    useScenario('huge')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '调度决策' }))

    // huge 场景固定 3200 条 → 214 页
    expect((await screen.findAllByText(/共 3200 条/)).length).toBeGreaterThan(0)
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText(/第 2 \/ 214 页/)).toBeInTheDocument()
  })

  it('?view=decisions 直达调度决策板块（dashboard 下钻入口）', async () => {
    useScenario('normal')
    renderAt('/service-analysis?view=decisions')

    expect(await screen.findByRole('tab', { name: '调度决策' })).toHaveAttribute('aria-selected', 'true')
    expect((await screen.findAllByText(/共 48 条/)).length).toBeGreaterThan(0)
  })

  it('勾选「包含归档」切冷查询：游标翻页取代页码、总数隐藏，取消勾选回热分页', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '调度决策' }))
    await screen.findAllByText(/共 48 条/)

    // 勾选冷查询：无总数（后端不回 total），出「第 N 页（含归档）」游标翻页
    await user.click(screen.getByRole('checkbox', { name: '包含归档' }))
    expect(await screen.findByText(/第 1 页（含归档）/)).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryAllByText(/共 48 条/)).toHaveLength(0)
    })

    // 游标前进 / 回退（48 条 · 每页 15 → 有下一页）
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText(/第 2 页（含归档）/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText(/第 1 页（含归档）/)).toBeInTheDocument()

    // 取消勾选：回热查询页码分页与总数
    await user.click(screen.getByRole('checkbox', { name: '包含归档' }))
    expect((await screen.findAllByText(/共 48 条/)).length).toBeGreaterThan(0)
  })
})

describe('/service-analysis 健康快照回放板块', () => {
  it('未选服时显选服引导占位', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await user.click(await screen.findByRole('tab', { name: '健康快照' }))

    expect(await screen.findByText('至少选择一台在线子服回放健康快照')).toBeInTheDocument()
  })

  it('选服后展示快照回放卡（等级药丸 + 权重 rev + 快照点数）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    // 选一台在线子服（devmock 已知 lobby-1）
    await user.click(await screen.findByRole('checkbox', { name: 'lobby-1' }))
    await user.click(screen.getByRole('tab', { name: '健康快照' }))

    // 快照卡：时间窗控制 + 权重 rev（normal 场景当前 rev=2）+ 快照点数统计
    expect(await screen.findByLabelText('时间范围')).toBeInTheDocument()
    expect(await screen.findByText('权重 rev 2')).toBeInTheDocument()
    expect(screen.getByText(/个快照点/)).toBeInTheDocument()
    // 最新 / 最低 / 最高统计齐备
    expect(screen.getByText('最新分')).toBeInTheDocument()
    expect(screen.getByText('最低')).toBeInTheDocument()
    expect(screen.getByText('最高')).toBeInTheDocument()
  })

  it('空态（empty 场景无在线子服）显选择列空态而快照板块保持引导', async () => {
    useScenario('empty')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    expect(
      await screen.findByText('暂无在线子服可供分析，接入并分配子服后展示指标时序'),
    ).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '健康快照' }))
    expect(await screen.findByText('至少选择一台在线子服回放健康快照')).toBeInTheDocument()
  })
})
