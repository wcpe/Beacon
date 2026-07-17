// /topology 拓扑页测试（双模式）：可视化模式渲染放射网络拓扑图（代理节点 / 大区分区 / 小区健康节点 /
// 异常链路失败率标签）、画布缩放平移（右下角控件 / 适应视图重置 / 拖拽超阈值抑制点选）、
// 点节点或链路出右侧固定侧面板明细、空态引导、超大量按大区聚合折叠并明示聚合；
// 数据剖析模式渲染异常链路表并点击边看明细。默认可视化模式，切到「数据剖析」看表。
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
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
  it('可视化模式渲染放射拓扑图：代理节点、大区分区与小区聚合节点', async () => {
    useScenario('normal')
    renderPage(<TopologyPage />)

    // 拓扑图标题与代理节点（SVG 内 text，每个 BC 集群一个靛蓝代理节点）
    expect(await screen.findByText('BC-子服链路')).toBeInTheDocument()
    expect(await screen.findByText('bc-main')).toBeInTheDocument()
    // 大区分区背景标签与小区聚合节点
    expect(await screen.findByText('华东大区')).toBeInTheDocument()
    expect(await screen.findByText('area-1')).toBeInTheDocument()
    // 图例悬浮卡
    expect(await screen.findByText('图例')).toBeInTheDocument()
  })

  it('异常聚合链路失败率标签直显在图上（红色加粗边旁）', async () => {
    useScenario('normal')
    // 注入一条两端均可解析的异常边，避免依赖超大量场景生成失败链路
    server.use(
      http.get('/admin/v2/messages/stats', () =>
        HttpResponse.json({
          edges: [
            {
              sourceServerId: 'game-1',
              resolvedServerId: 'game-3',
              total: 1,
              failed: 1,
              expired: 0,
              failRatePercent: 100,
              p95DurationMs: 1,
              topFailReasons: [],
              sampleMessageIds: [],
            },
          ],
        }),
      ),
    )
    const { container } = renderPage(<TopologyPage />)

    await screen.findByText('BC-子服链路')
    // 等消息边与服务器归属解析完成（图上出现可点击的聚合链路）
    await screen.findAllByRole('button', { name: / → / })
    // 图内出现「x.x% 失败率」标签（异常聚合链路直显失败率）
    await waitFor(() => {
      const graphSvg = container.querySelector('svg[role="img"]')
      expect(graphSvg).not.toBeNull()
      const labels = [...(graphSvg as SVGElement).querySelectorAll('text')].map((el) => el.textContent)
      expect(labels).toContain('100% 失败率')
    })
  })

  it('点击小区节点，右侧固定侧面板展示该区概要（健康分布）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<TopologyPage />)

    await screen.findByText('area-1')
    // 小区节点是可点击的 SVG 分组（role=button，aria-label 为小区名）
    await user.click(await screen.findByRole('button', { name: 'area-1' }))

    const summary = await screen.findByText('小区概要')
    expect(summary.closest('aside')).not.toBeNull()
    expect(await screen.findByText('健康分布')).toBeInTheDocument()
  })

  it('点击图上聚合链路，右侧固定侧面板展示链路明细（样本消息）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<TopologyPage />)

    await screen.findByText('area-1')
    // 聚合链路是可点击的 SVG 分组（role=button，aria-label 为「集群 → 节点」）
    const linkButtons = await screen.findAllByRole('button', { name: / → / })
    await user.click(linkButtons[0])

    const sample = await screen.findByText('样本消息')
    expect(sample.closest('aside')).not.toBeNull()
  })

  it('画布缩放控件存在：放大 / 缩小 / 适应视图与当前百分比', async () => {
    useScenario('normal')
    renderPage(<TopologyPage />)

    await screen.findByText('area-1')
    expect(screen.getByRole('button', { name: '放大' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '缩小' })).toBeInTheDocument()
    const fitButton = screen.getByRole('button', { name: '适应视图' })
    // 百分比展示在控件组内；jsdom 量不到画布尺寸时适应视图回退 1x，显示 100%
    const controls = fitButton.closest('div')
    expect(controls).not.toBeNull()
    expect(within(controls as HTMLElement).getByText('100%')).toBeInTheDocument()
  })

  it('放大后百分比变化，点适应视图重置回初始倍率', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<TopologyPage />)

    await screen.findByText('area-1')
    const controls = screen.getByRole('button', { name: '适应视图' }).closest('div') as HTMLElement
    await user.click(screen.getByRole('button', { name: '放大' }))
    expect(within(controls).getByText('125%')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '适应视图' }))
    expect(within(controls).getByText('100%')).toBeInTheDocument()
  })

  it('拖拽超过阈值抑制点选，阈值内视为点击（点选节点正常出侧面板）', async () => {
    useScenario('normal')
    renderPage(<TopologyPage />)

    await screen.findByText('area-1')
    const node = screen.getByRole('button', { name: 'area-1' })

    // 拖拽：按下后位移超过阈值再松开，随后的 click 被捕获阶段抑制（不触发点选）
    fireEvent(node, new MouseEvent('pointerdown', { bubbles: true, button: 0, clientX: 100, clientY: 100 }))
    fireEvent(window, new MouseEvent('pointermove', { bubbles: true, clientX: 140, clientY: 130 }))
    fireEvent(window, new MouseEvent('pointerup', { bubbles: true, clientX: 140, clientY: 130 }))
    fireEvent(node, new MouseEvent('click', { bubbles: true, cancelable: true, clientX: 140, clientY: 130 }))
    expect(screen.queryByText('小区概要')).not.toBeInTheDocument()

    // 点击：位移在阈值内，click 正常触发点选出侧面板
    fireEvent(node, new MouseEvent('pointerdown', { bubbles: true, button: 0, clientX: 100, clientY: 100 }))
    fireEvent(window, new MouseEvent('pointermove', { bubbles: true, clientX: 101, clientY: 101 }))
    fireEvent(window, new MouseEvent('pointerup', { bubbles: true, clientX: 101, clientY: 101 }))
    fireEvent(node, new MouseEvent('click', { bubbles: true, cancelable: true, clientX: 101, clientY: 101 }))
    expect(await screen.findByText('小区概要')).toBeInTheDocument()
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

  it('数据剖析模式消息聚合查询失败时如实展示脱敏错误（不静默）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    server.use(
      http.get('/admin/v2/messages/stats', () =>
        HttpResponse.json({ code: 'internal', message: '数据库连接失败' }, { status: 500 }),
      ),
    )
    renderPage(<TopologyPage />)

    await user.click(await screen.findByRole('tab', { name: /数据剖析/ }))
    expect(await screen.findByText(/加载失败：数据库连接失败/)).toBeInTheDocument()
  })

  it('超大量态服务器间原始链路聚合为集群 → 大区链路并明示，避免渲染上千条动画边（防卡死）', async () => {
    useScenario('huge')
    const { container } = renderPage(<TopologyPage />)

    // 等图加载
    await screen.findByText('BC-子服链路')
    // 聚合明示文案出现（说明上千条服务器间原始边未被逐条渲染）
    expect(await screen.findByText(/条服务器间链路聚合为/)).toBeInTheDocument()

    // 只数拓扑图主 SVG（role="img"）直属的链路 path（排除节点图标等嵌套 svg 内的 path）：
    // 上限 60 条聚合链路，每条至多 3 个 path（选中光晕 + 静态底 + 动画流），故 ≤180；
    // 断言远小于「上千原始边逐条渲染」规模，证明聚合生效。
    await waitFor(() => {
      const graphSvg = container.querySelector('svg[role="img"]')
      expect(graphSvg).not.toBeNull()
      const paths = [...(graphSvg as SVGElement).querySelectorAll('path')].filter(
        (p) => p.closest('svg') === graphSvg,
      )
      expect(paths.length).toBeGreaterThan(0)
      expect(paths.length).toBeLessThanOrEqual(60 * 3)
    })
  })
})

// 在数据剖析模式选中失败率最高的边（必有失败样本），打开首个样本的 payload 查看弹窗
async function openPayloadDialog(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(await screen.findByRole('tab', { name: /数据剖析/ }))
  const targetCells = await screen.findAllByText(/^→ /)
  await user.click(targetCells[0].closest('tr') as HTMLElement)
  const viewButtons = await screen.findAllByRole('button', { name: '查看 payload' })
  await user.click(viewButtons[0])
}

describe('/topology 消息 payload 受控查看弹窗', () => {
  it('原因必填：为空禁用确认，填写后可提交', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<TopologyPage />)
    await openPayloadDialog(user)

    // 弹窗打开：标题 + 确认按钮在原因为空时禁用
    expect(await screen.findByText('查看消息 payload')).toBeInTheDocument()
    const confirm = screen.getByRole('button', { name: '确认查看' })
    expect(confirm).toBeDisabled()
    await user.type(screen.getByLabelText(/查看原因/), '排查异常链路失败样本')
    expect(confirm).toBeEnabled()
  })

  it('填写原因提交成功：展示 payload 原文 + SHA-256 + 大小', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<TopologyPage />)
    await openPayloadDialog(user)

    await user.type(screen.getByLabelText(/查看原因/), '排查异常链路失败样本')
    await user.click(screen.getByRole('button', { name: '确认查看' }))

    // devmock payload 为 JSON 原文（含 "type" 键），并带 64 位十六进制 SHA-256 与字节大小
    expect(await screen.findByText(/"type"/)).toBeInTheDocument()
    expect(screen.getByText('SHA-256')).toBeInTheDocument()
    expect(screen.getByText(/^[0-9a-f]{64}$/)).toBeInTheDocument()
    expect(screen.getByText(/大小 \d+ 字节/)).toBeInTheDocument()
    // 展示后确认按钮换为关闭
    expect(screen.queryByRole('button', { name: '确认查看' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '关闭' })).toBeInTheDocument()
  })

  it('无权限（403）时弹窗内展示脱敏错误文案，不静默', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    server.use(
      http.post('/admin/v2/messages/:messageId/payload', () =>
        HttpResponse.json({ code: 'forbidden', message: '只读密钥无权执行写操作' }, { status: 403 }),
      ),
    )
    renderPage(<TopologyPage />)
    await openPayloadDialog(user)

    await user.type(screen.getByLabelText(/查看原因/), '排查异常链路失败样本')
    await user.click(screen.getByRole('button', { name: '确认查看' }))

    // 脱敏错误内联展示在弹窗内，payload 未被展示
    expect(await screen.findByText('只读密钥无权执行写操作')).toBeInTheDocument()
    expect(screen.queryByText('SHA-256')).not.toBeInTheDocument()
  })
})
