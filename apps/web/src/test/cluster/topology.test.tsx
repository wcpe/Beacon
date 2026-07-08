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

    // 明细区出现样本消息标题
    await waitFor(() => {
      expect(screen.getByText('样本消息')).toBeInTheDocument()
    })
  })

  it('超大量态按聚合折叠并明示截断', async () => {
    useScenario('huge')
    renderPage(<TopologyPage />)

    // 出现折叠提示文案（节点过多按大区聚合）
    expect(await screen.findByText('节点过多，已按大区聚合折叠')).toBeInTheDocument()
  })
})
