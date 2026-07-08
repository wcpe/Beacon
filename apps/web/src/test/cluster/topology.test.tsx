// /topology 拓扑页测试（双模式）：可视化模式渲染拓扑图与集群节点、空态引导、超大量按聚合折叠明示；
// 数据剖析模式渲染异常链路表并点击边看明细。默认可视化模式，切到「数据剖析」看表。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import TopologyPage from '../../pages/topology'
import { createTestServer, renderPage, useScenario } from './harness'

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

describe('/topology 拓扑页', () => {
  it('可视化模式渲染拓扑图与集群节点', async () => {
    useScenario('normal')
    renderPage(<TopologyPage />)

    // 拓扑图标题与集群节点（SVG 内 text）
    expect(await screen.findByText('BC-子服链路')).toBeInTheDocument()
    expect(await screen.findByText('bc-main')).toBeInTheDocument()
  })

  it('空态给出接入引导', async () => {
    useScenario('empty')
    renderPage(<TopologyPage />)

    expect(
      await screen.findByText('暂无拓扑数据，接入服务器并完成区服分配后展示链路'),
    ).toBeInTheDocument()
  })

  it('可视化模式点击异常链路 chip，明细在图区右侧固定侧面板展示（不 reflow 图）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    const { container } = renderPage(<TopologyPage />)

    // 等图加载：拓扑 SVG 出现
    await screen.findByText('BC-子服链路')
    const svg = container.querySelector('svg')
    expect(svg).not.toBeNull()

    // 点击一个异常链路 chip（异常链路层里带失败率百分比的按钮）
    const chips = await screen.findAllByRole('button', { name: /%$/ })
    await user.click(chips[0])

    // 明细出现在 <aside> 固定侧面板内（样本消息标题），且 SVG 仍在（图未被替换/移除）
    const sample = await screen.findByText('样本消息')
    expect(sample.closest('aside')).not.toBeNull()
    expect(container.querySelector('svg')).not.toBeNull()
  })

  it('切到数据剖析模式，点击异常边展开链路明细（样本消息）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<TopologyPage />)

    // 切换到数据剖析 Tab
    await user.click(await screen.findByRole('tab', { name: /数据剖析/ }))

    // 等待边表格出数据：定位第一条边的表格行（含目标列的 → 文本）
    const targetCells = await screen.findAllByText(/^→ /)
    const firstRow = targetCells[0].closest('tr')
    expect(firstRow).not.toBeNull()
    await user.click(firstRow as HTMLElement)

    // 明细区出现样本消息标题，且落在右侧固定侧面板（<aside>）内——不再放页面底部
    await waitFor(() => {
      const sample = screen.getByText('样本消息')
      expect(sample).toBeInTheDocument()
      expect(sample.closest('aside')).not.toBeNull()
    })
  })

  it('超大量态按聚合折叠并明示截断', async () => {
    useScenario('huge')
    renderPage(<TopologyPage />)

    // 出现折叠提示文案（节点过多按大区聚合）
    expect(await screen.findByText('节点过多，已按大区聚合折叠')).toBeInTheDocument()
  })

  it('超大量态请求链路按节点盒对去重并硬性截断，避免渲染上千条动画边（防卡死）', async () => {
    useScenario('huge')
    const { container } = renderPage(<TopologyPage />)

    // 等图加载
    await screen.findByText('BC-子服链路')
    // 链路截断明示文案出现（说明上千聚合边未被逐条渲染）
    expect(await screen.findByText(/链路过多，仅展示失败率最高的前/)).toBeInTheDocument()

    // 只数拓扑图主 SVG（role="img"）内的链路 path：上限 60 条链路，每条至多 2 个 path
    // （静态底 + 动画流），故 ≤120；断言远小于「上千聚合边逐条渲染」规模，证明截断生效。
    await waitFor(() => {
      const graphSvg = container.querySelector('svg[role="img"]')
      expect(graphSvg).not.toBeNull()
      const paths = (graphSvg as SVGElement).querySelectorAll('path')
      expect(paths.length).toBeGreaterThan(0)
      expect(paths.length).toBeLessThanOrEqual(60 * 2)
    })
  })
})
