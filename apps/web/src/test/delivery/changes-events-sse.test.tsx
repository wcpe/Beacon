// 进度事件 SSE 契约（FR-256）：详情页事件 Tab 优先走 SSE 实时推送，断线 / 不可用回退 5s 轮询。
// 真机：GET /admin/v2/change-orders/{id}/events 按 Accept 内容协商（text/event-stream → SSE 流）。
// 前端不用 EventSource（无法带 Authorization 头），改用 fetch + ReadableStream 手工读帧。
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import ChangesPage from '../../pages/changes'
import { parseSseFrame } from '../../api/delivery-changes'
import { useChangeEvents } from '../../features/delivery/use-change-events'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
  vi.useRealTimers()
})
afterAll(() => {
  server.close()
})

// 种子单「Quests 插件灰度 v1.9」（rolling）
const ORDER_ID = 5004

const STREAMED_EVENT = {
  seq: 9001,
  at: '2024-05-01T00:00:00.000Z',
  type: 'target_status' as const,
  orderId: ORDER_ID,
  batchNo: 2,
  serverId: 'stream-9',
  status: 'failed',
}

/** 造一条 SSE 帧（与后端 deliverySSESink 同形：event 名 + data JSON + 空行） */
function sseFrames(events: unknown[]): string {
  return events.map((event) => `event: target_status\ndata: ${JSON.stringify(event)}\n\n`).join('')
}

/** 事件端点：SSE 分支回帧流（keepOpen 时保持长连，模拟真机实时推送） */
function eventsHandler(options: { keepOpen: boolean }): HttpResponse<ReadableStream<Uint8Array>> {
  const encoder = new TextEncoder()
  const stream = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(encoder.encode(`: keepalive\n\n${sseFrames([STREAMED_EVENT])}`))
      if (!options.keepOpen) {
        controller.close()
      }
    },
  })
  return new HttpResponse(stream, {
    headers: { 'Content-Type': 'text/event-stream; charset=utf-8' },
  })
}

describe('变更单进度事件 SSE', () => {
  it('SSE 实时推送：流内事件进入时间线，并标注「实时推送中」', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    server.use(
      http.get('*/admin/v2/change-orders/:id/events', ({ request }) => {
        if (!(request.headers.get('Accept') ?? '').includes('text/event-stream')) {
          // 轮询快照：只含单据级事件，流内目标事件必须由 SSE 补齐
          return HttpResponse.json({
            events: [
              {
                seq: 1,
                at: '2024-04-30T00:00:00.000Z',
                type: 'order_status',
                orderId: ORDER_ID,
                batchNo: null,
                serverId: null,
                status: 'rolling',
              },
            ],
          })
        }
        return eventsHandler({ keepOpen: true })
      }),
    )
    renderPage(<ChangesPage />, [`/changes?order=${String(ORDER_ID)}`])

    await screen.findByRole('button', { name: '返回列表' })
    await user.click(await screen.findByRole('tab', { name: '进度时间线' }))
    const panel = within(await screen.findByRole('tabpanel'))

    // 流内事件（快照里没有的目标失败事件）出现在时间线（可视化行 = 主体 · 状态）
    expect(await panel.findByText('目标 stream-9 · 失败')).toBeInTheDocument()
    // 连接建立后标注实时推送（不再显示轮询）
    expect(await panel.findByText('实时推送中')).toBeInTheDocument()
    expect(panel.queryByText(/轮询刷新中/)).not.toBeInTheDocument()
  }, 20_000)

  it('SSE 不可用（500）时回退 5s 轮询：按间隔重复取快照', async () => {
    vi.useFakeTimers()
    useScenario('normal')

    let jsonCalls = 0
    server.use(
      http.get('*/admin/v2/change-orders/:id/events', ({ request }) => {
        if ((request.headers.get('Accept') ?? '').includes('text/event-stream')) {
          return HttpResponse.json({ code: 'internal_error', message: '事件流不可用' }, { status: 500 })
        }
        jsonCalls += 1
        return HttpResponse.json({
          events: [
            {
              seq: 1,
              at: '2024-04-30T00:00:00.000Z',
              type: 'order_status',
              orderId: ORDER_ID,
              batchNo: null,
              serverId: null,
              status: 'rolling',
            },
          ],
        })
      }),
    )
    renderPage(<EventsProbe orderId={ORDER_ID} />)

    // 首屏取到快照；SSE 不可用 → live=false → 回退轮询
    await vi.waitFor(() => {
      expect(screen.getByTestId('snapshot')).toHaveTextContent('order_status:rolling')
    })
    expect(screen.getByTestId('live')).toHaveTextContent('polling')

    const before = jsonCalls
    await vi.advanceTimersByTimeAsync(5100)
    // 5s 回退间隔到点即再取一次快照（进度在断线后仍持续跟随）
    expect(jsonCalls).toBeGreaterThan(before)
  }, 20_000)

  it('SSE 帧解析：忽略保活注释与非法负载，只取 data 内的事件对象', () => {
    expect(parseSseFrame(': keepalive')).toBeNull()
    expect(parseSseFrame('event: target_status')).toBeNull()
    expect(parseSseFrame('data: not-json')).toBeNull()
    expect(parseSseFrame(`data: ${JSON.stringify(STREAMED_EVENT)}`)).toEqual(STREAMED_EVENT)
  })

  it('流结束后回退轮询：立刻关闭的流不阻塞快照展示', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    server.use(
      http.get('*/admin/v2/change-orders/:id/events', ({ request }) => {
        if (!(request.headers.get('Accept') ?? '').includes('text/event-stream')) {
          return HttpResponse.json({
            events: [
              {
                seq: 1,
                at: '2024-04-30T00:00:00.000Z',
                type: 'order_status',
                orderId: ORDER_ID,
                batchNo: null,
                serverId: null,
                status: 'rolling',
              },
            ],
          })
        }
        return eventsHandler({ keepOpen: false })
      }),
    )
    renderPage(<ChangesPage />, [`/changes?order=${String(ORDER_ID)}`])

    await screen.findByRole('button', { name: '返回列表' })
    await user.click(await screen.findByRole('tab', { name: '进度时间线' }))
    const panel = within(await screen.findByRole('tabpanel'))

    // 流补发的事件与快照事件合并展示（seq 去重、按 seq 排序）
    expect(await panel.findByText('目标 stream-9 · 失败')).toBeInTheDocument()
    expect(panel.getByText('变更单 · 灰度中')).toBeInTheDocument()
    // 流已结束 → 回退轮询提示可见
    expect(await panel.findByText('轮询刷新中（5 秒）')).toBeInTheDocument()
  }, 20_000)
})

// 事件订阅探针（hook 级）：默认 Tab 不是事件 Tab，页面级渲染不会触发事件查询，
// 断线回退的间隔断言因此落在 hook 本身。
function EventsProbe({ orderId }: { orderId: number }) {
  const { events, live } = useChangeEvents(orderId)
  return (
    <div>
      <span data-testid="live">{live ? 'live' : 'polling'}</span>
      <span data-testid="snapshot">{events.map((event) => `${event.type}:${event.status}`).join(',')}</span>
    </div>
  )
}
