// /connections 与 /messages 页测试（FR-181）：默认全局近期 / 条件收窄检索 / 详情面板 /
// 游标分页文案 / payload 受控查看入口。数据走 devmock（查询防护与游标切片在 mock 内真实生效）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ConnectionsPage from '../../pages/connections'
import MessagesPage from '../../pages/messages'
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

/** 等 DataTable 真实数据行（带 onRowClick → cursor-pointer），避开骨架屏假行 */
async function waitForDataRows(serverId?: string): Promise<HTMLElement[]> {
  return waitFor(() => {
    const rows = screen
      .getAllByRole('row')
      .filter((r): r is HTMLElement => r.classList.contains('cursor-pointer'))
    expect(rows.length).toBeGreaterThan(0)
    if (serverId) {
      expect(rows.every((row) => row.textContent?.includes(serverId))).toBe(true)
    }
    return rows
  })
}

describe('/connections 连接明细页', () => {
  it('进页默认全局近期：查询按钮可用并直接出连接行', async () => {
    useScenario('normal')
    renderPage(<ConnectionsPage />)

    // 热查询默认 committed 近 24h，无需 selector
    expect(screen.getByRole('button', { name: '查询' })).toBeEnabled()
    await waitForDataRows()
  })

  it('URL 深链 ?serverId=&window= 进页即按该服 / 该时间窗查询（供 dashboard 明细下钻）', async () => {
    useScenario('normal')
    const capturedUrls: string[] = []
    server.use(
      http.get('/admin/v2/connections', ({ request }) => {
        capturedUrls.push(request.url)
        return HttpResponse.json({ items: [], nextCursor: null })
      }),
    )

    renderPage(<ConnectionsPage />, ['/connections?serverId=proxy-1&window=24h'])

    // URL 的 serverId 已预填到筛选框，且进页首次请求即带上该 serverId（无需再点「查询」）
    expect(await screen.findByLabelText('服务器 ID（代理或后端）')).toHaveValue('proxy-1')
    await waitFor(() => {
      expect(capturedUrls.some((u) => u.includes('serverId=proxy-1'))).toBe(true)
    })
  })

  it('URL 深链 window 为非法值时回落 24h 默认口径，不因脏参数改变默认', async () => {
    useScenario('normal')
    const capturedUrls: string[] = []
    server.use(
      http.get('/admin/v2/connections', ({ request }) => {
        capturedUrls.push(request.url)
        return HttpResponse.json({ items: [], nextCursor: null })
      }),
    )

    renderPage(<ConnectionsPage />, ['/connections?window=999d'])

    await waitFor(() => {
      expect(capturedUrls.length).toBeGreaterThan(0)
    })
    const first = capturedUrls.at(0)
    if (first === undefined) {
      throw new Error('未捕获 connections 请求')
    }
    const url = new URL(first)
    const from = url.searchParams.get('from')
    const to = url.searchParams.get('to')
    if (from === null || to === null) {
      throw new Error('热查询应带 from/to 时间窗')
    }
    // 默认 24h 窗口：跨度落在 24 小时附近（容忍毫秒级取整偏差）
    const spanMs = Date.parse(to) - Date.parse(from)
    expect(spanMs).toBeGreaterThan(86_000_000)
    expect(spanMs).toBeLessThan(86_600_000)
  })

  it('按服务器 ID 收窄后点行开详情面板', async () => {    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConnectionsPage />)

    await waitForDataRows()

    await user.clear(screen.getByLabelText('服务器 ID（代理或后端）'))
    await user.type(screen.getByLabelText('服务器 ID（代理或后端）'), 'proxy-1')
    await user.click(screen.getByRole('button', { name: '查询' }))

    const rows = await waitForDataRows()

    // 点首个数据行 → 右侧详情面板出 connId / 客户端 IP 字段
    await user.click(rows[0])
    await waitFor(() => {
      expect(screen.getByText('连接详情')).toBeInTheDocument()
    })
    expect(screen.getByText('客户端 IP')).toBeInTheDocument()
    expect(screen.getByText('后端切换次数')).toBeInTheDocument()
  })

  it('超大量场景走原生游标翻页（第 N 页无「含归档」标注、可往返）', async () => {
    useScenario('huge')
    const user = userEvent.setup()
    renderPage(<ConnectionsPage />)

    // huge 场景代理命名带补零（proxy-01..proxy-12）；收窄到单代理保证分页稳定
    await user.type(screen.getByLabelText('服务器 ID（代理或后端）'), 'proxy-01')
    await user.click(screen.getByRole('button', { name: '查询' }))

    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText(/第 2 页$/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
  }, 10_000)

  it('空态场景默认查询返回空列表提示', async () => {
    useScenario('empty')
    renderPage(<ConnectionsPage />)

    // 空态文案含查询口径说明（按「时间窗内新开会话」过滤），用前缀匹配避开整句
    expect(await screen.findByText(/^当前条件下无连接记录/)).toBeInTheDocument()
  })

  it('可点击行键盘可达：Tab 聚焦 + Enter 打开详情，主表列集保持完整', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConnectionsPage />)

    const rows = await waitForDataRows()
    const firstRow = rows[0]
    // 可聚焦：DOM tabIndex 属性或 HTMLElement.tabIndex 皆可
    expect(firstRow.tabIndex).toBe(0)
    firstRow.focus()
    await user.keyboard('{Enter}')
    await waitFor(() => {
      expect(screen.getByText('连接详情')).toBeInTheDocument()
    })
    // 详情为固定层抽屉，主表宽度不变，次要列始终保留
    expect(screen.getByText('后端路径')).toBeInTheDocument()
    expect(screen.getByText('时长')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
})

describe('/messages 消息链路页', () => {
  it('进页默认全局近期：查询按钮可用并直接出消息行', async () => {
    useScenario('normal')
    renderPage(<MessagesPage />)

    expect(screen.getByRole('button', { name: '查询' })).toBeEnabled()
    await waitForDataRows()
  })

  it('按服务器 ID 收窄后详情面板含逐跳链路与 payload 查看入口', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<MessagesPage />)

    await waitForDataRows()

    await user.clear(screen.getByLabelText('服务器 ID（来源或目标）'))
    await user.type(screen.getByLabelText('服务器 ID（来源或目标）'), 'game-1')
    await user.click(screen.getByRole('button', { name: '查询' }))

    const rows = await waitForDataRows('game-1')

    // 点首个数据行 → 详情面板：逐跳链路 + payload 受控查看入口（mock 全部 payloadStored）
    await user.click(rows[0])
    await waitFor(() => {
      expect(screen.getByText('逐跳链路')).toBeInTheDocument()
    })
    expect(screen.getByRole('button', { name: '查看 payload' })).toBeInTheDocument()
    // 逐跳时间线至少含发出 + 接收两跳
    const hops = screen.getByText('逐跳链路').closest('div')
    expect(within(hops as HTMLElement).getByText('发出')).toBeInTheDocument()
    expect(within(hops as HTMLElement).getByText('接收')).toBeInTheDocument()
  })

  it('超大量场景消息可稳定游标翻页（数据集中于头部后端）', async () => {
    useScenario('huge')
    const user = userEvent.setup()
    renderPage(<MessagesPage />)

    // huge 场景后端命名带补零（game-0001..1200）；消息集中于前 8 台
    await user.type(screen.getByLabelText('服务器 ID（来源或目标）'), 'game-0001')
    await user.click(screen.getByRole('button', { name: '查询' }))

    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText(/第 2 页$/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
  })

  it('payload 受控查看：填写原因后直读并展示正文、哈希和大小，关闭后清除内容', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    const payload = '{"type":"lodestone:teleport","seq":7}'
    let requestBody: unknown = null
    let requestMessageId = ''
    let requestCount = 0
    server.use(
      http.post('*/admin/v2/messages/:messageId/payload', async ({ request, params }) => {
        requestMessageId = String(params.messageId)
        requestBody = await request.json()
        requestCount += 1
        return HttpResponse.json({ payload, sha256: 'a'.repeat(64), size: payload.length })
      }),
    )
    const { queryClient } = renderPage(<MessagesPage />)

    const rows = await waitForDataRows()
    await user.click(rows[0])
    // 等详情就绪后再点 payload 入口（勿点骨架行）
    await user.click(await screen.findByRole('button', { name: '查看 payload' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).queryByText(payload)).not.toBeInTheDocument()
    const reasonBox = within(dialog).getByRole('textbox')
    await user.type(reasonBox, '排查跨服传送失败')
    await user.click(within(dialog).getByRole('button', { name: '确认查看' }))

    expect(await within(dialog).findByText(payload)).toBeInTheDocument()
    expect(within(dialog).getByText(/SHA-256/)).toBeInTheDocument()
    expect(within(dialog).getByText('a'.repeat(64))).toBeInTheDocument()
    expect(within(dialog).getByText(`大小 ${String(payload.length)} 字节`)).toBeInTheDocument()
    expect(requestBody).toEqual({ reason: '排查跨服传送失败' })
    expect(requestMessageId).not.toBe('')
    expect(requestCount).toBe(1)
    expect(within(dialog).queryByRole('link', { name: '前往审批中心' })).not.toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '关闭' }))
    expect(screen.queryByText(payload)).not.toBeInTheDocument()

    // 敏感正文不得滞留在全局 MutationCache：该 mutation 设了 gcTime: 0，
    // 无 observer 后应被回收，否则关闭弹窗后仍可从缓存取到正文。
    await waitFor(() => {
      const cached = queryClient.getMutationCache().getAll()
      expect(cached.some((mutation) => JSON.stringify(mutation.state.data ?? '').includes(payload))).toBe(false)
    })
  })
})
