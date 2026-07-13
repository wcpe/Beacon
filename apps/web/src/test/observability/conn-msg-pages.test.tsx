// /connections 与 /messages 页测试（FR-181）：查询防护引导空态 / 条件检索出行 / 详情面板 /
// 游标分页文案 / payload 受控查看入口。数据走 devmock（查询防护与游标切片在 mock 内真实生效）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
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

describe('/connections 连接明细页', () => {
  it('初始为查询引导空态，条件不满足防护时查询按钮禁用', async () => {
    useScenario('normal')
    renderPage(<ConnectionsPage />)

    expect(
      await screen.findByText('输入查询条件后开始检索：精确 connId，或 serverId / 玩家 UUID + 时间范围'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '查询' })).toBeDisabled()
  })

  it('按 serverId + 时间范围检索出连接行，点行开详情面板', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConnectionsPage />)

    await user.type(screen.getByLabelText('serverId（代理或后端）'), 'proxy-1')
    await user.click(screen.getByRole('button', { name: '查询' }))

    // 出数据行（常规场景单页装得下，分页器隐藏）
    await waitFor(() => {
      expect(screen.getAllByRole('row').length).toBeGreaterThan(1)
    })

    // 点首个数据行 → 右侧详情面板出 connId / 客户端 IP 字段
    await user.click(screen.getAllByRole('row')[1])
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

    // huge 场景代理命名带补零（proxy-01..proxy-12）
    await user.type(screen.getByLabelText('serverId（代理或后端）'), 'proxy-01')
    await user.click(screen.getByRole('button', { name: '查询' }))

    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText(/第 2 页$/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText(/第 1 页$/)).toBeInTheDocument()
  })

  it('空态场景检索返回空列表提示', async () => {
    useScenario('empty')
    const user = userEvent.setup()
    renderPage(<ConnectionsPage />)

    await user.type(screen.getByLabelText('serverId（代理或后端）'), 'proxy-1')
    await user.click(screen.getByRole('button', { name: '查询' }))

    expect(await screen.findByText('当前条件下无连接记录')).toBeInTheDocument()
  })
})

describe('/messages 消息链路页', () => {
  it('初始为查询引导空态', async () => {
    useScenario('normal')
    renderPage(<MessagesPage />)

    expect(
      await screen.findByText(
        '输入查询条件后开始检索：精确 messageId / correlationId，或 serverId / 玩家 UUID + 时间范围',
      ),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '查询' })).toBeDisabled()
  })

  it('按 serverId 检索出消息行，详情面板含逐跳链路与 payload 查看入口', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<MessagesPage />)

    await user.type(screen.getByLabelText('serverId（来源或目标）'), 'lobby-1')
    await user.click(screen.getByRole('button', { name: '查询' }))

    await waitFor(() => {
      expect(screen.getAllByRole('row').length).toBeGreaterThan(1)
    })

    // 点首个数据行 → 详情面板：逐跳链路 + payload 受控查看入口（mock 全部 payloadStored）
    await user.click(screen.getAllByRole('row')[1])
    await waitFor(() => {
      expect(screen.getByText('逐跳链路')).toBeInTheDocument()
    })
    expect(screen.getByRole('button', { name: '查看 payload' })).toBeInTheDocument()
    // 逐跳时间线至少含发出 + 接收两跳
    const hops = screen.getByText('逐跳链路').closest('div')
    expect(within(hops as HTMLElement).getByText('发出')).toBeInTheDocument()
    expect(within(hops as HTMLElement).getByText('接收')).toBeInTheDocument()
  })

  it('payload 受控查看：原因必填提交后展示原文与 SHA-256', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<MessagesPage />)

    await user.type(screen.getByLabelText('serverId（来源或目标）'), 'lobby-1')
    await user.click(screen.getByRole('button', { name: '查询' }))
    await waitFor(() => {
      expect(screen.getAllByRole('row').length).toBeGreaterThan(1)
    })
    await user.click(screen.getAllByRole('row')[1])
    await user.click(await screen.findByRole('button', { name: '查看 payload' }))

    // 弹窗内填原因并提交（复用 /topology 的受控查看弹窗）
    const dialog = await screen.findByRole('dialog')
    const reasonBox = within(dialog).getByRole('textbox')
    await user.type(reasonBox, '排查跨服传送失败')
    await user.click(within(dialog).getByRole('button', { name: /查看|确认/ }))
    await waitFor(() => {
      expect(within(dialog).getByText(/SHA-256/i)).toBeInTheDocument()
    })
  })
})
