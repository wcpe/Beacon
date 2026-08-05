// /connections 与 /messages 页测试（FR-181）：默认全局近期 / 条件收窄检索 / 详情面板 /
// 游标分页文案 / payload 受控查看入口。数据走 devmock（查询防护与游标切片在 mock 内真实生效）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ConnectionsPage from '../../pages/connections'
import MessagesPage from '../../pages/messages'
import { approveApproval, fetchApprovalDetail } from '../../api/approvals'
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

    // 热查询默认 committed 近 1h，无需 selector
    expect(screen.getByRole('button', { name: '查询' })).toBeEnabled()
    await waitForDataRows()
  })

  it('按服务器 ID 收窄后点行开详情面板', async () => {
    useScenario('normal')
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
  })

  it('空态场景默认查询返回空列表提示', async () => {
    useScenario('empty')
    renderPage(<ConnectionsPage />)

    expect(await screen.findByText('当前条件下无连接记录')).toBeInTheDocument()
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

  it('payload 受控查看：调用真实专用申请与一次性消费接口，正文不进入页面', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    let approved = false
    let consumed = false
    const requestId = 'apr_real_message_payload'
    let messageId = ''
    const approval = () => ({
      id: 10001, requestId, operationKey: 'message.payload.read', operationKind: 'message.payload.read', resourceType: 'message', resourceId: messageId,
      riskLevel: 'high', status: approved ? 'succeeded' : 'pending', requestReason: '排查跨服传送失败', safeSummary: '脱敏摘要', frozenPayloadSha256: 'a'.repeat(64),
      requesterType: 'human', requesterId: 'admin', deciderType: approved ? 'human' : null, deciderId: approved ? 'admin' : null,
      approvedBy: approved ? 'admin' : null, rejectReason: null, decisionReason: null, expiresAt: null, version: approved ? 2 : 1,
      sensitiveAccessGrant: approved ? { grantId: 'sag_real_message_payload' } : undefined,
    })
    server.use(
      http.post('*/admin/v2/messages/:messageId/payload/approval-requests', async ({ request, params }) => {
        messageId = String(params.messageId)
        expect(messageId).not.toBe('')
        expect(request.headers.get('Idempotency-Key')).not.toBeNull()
        expect(await request.json()).toEqual({ reason: '排查跨服传送失败' })
        return HttpResponse.json({ requestId, status: 'pending' }, { status: 202 })
      }),
      http.get(`*/admin/v2/approval-requests/${requestId}`, () => HttpResponse.json(approval())),
      http.post(`*/admin/v2/approval-requests/${requestId}/approve`, () => {
        approved = true
        return HttpResponse.json(approval(), { status: 202 })
      }),
      http.post('*/admin/v2/sensitive-access-grants/sag_real_message_payload/consume', async ({ request }) => {
        expect(await request.json()).toEqual({ messageId })
        consumed = true
        return HttpResponse.json({ payload: '不得渲染的敏感正文', sha256: 'a'.repeat(64), size: 27 })
      }),
    )
    renderPage(<MessagesPage />)

    const rows = await waitForDataRows()
    await user.click(rows[0])
    // 等详情就绪后再点 payload 入口（勿点骨架行）
    await user.click(await screen.findByRole('button', { name: '查看 payload' }))

    // 弹窗只提交审批，批准前不请求或展示 payload 正文。
    const dialog = await screen.findByRole('dialog')
    const reasonBox = within(dialog).getByRole('textbox')
    await user.type(reasonBox, '排查跨服传送失败')
    await user.click(within(dialog).getByRole('button', { name: '提交审批' }))

    const approvalLink = await within(dialog).findByRole('link', { name: '前往审批中心' })
    const approvalId = approvalLink.getAttribute('href')?.split('/').at(-1)
    expect(approvalId).toBe(requestId)
    const detail = await fetchApprovalDetail(requestId)
    expect(detail.operationKey).toBe('message.payload.read')
    expect(detail.requestReason).toBe('排查跨服传送失败')
    await approveApproval(requestId)

    await user.click(await within(dialog).findByRole('button', { name: '刷新审批状态' }))
    await user.click(await within(dialog).findByRole('button', { name: '一次性消费授权' }))
    expect(await within(dialog).findByText('授权已消费；敏感正文未被页面保存或展示。')).toBeInTheDocument()
    expect(within(dialog).queryByText(/SHA-256/i)).not.toBeInTheDocument()
    expect(within(dialog).queryByText('不得渲染的敏感正文')).not.toBeInTheDocument()
    expect(consumed).toBe(true)
  })
})
