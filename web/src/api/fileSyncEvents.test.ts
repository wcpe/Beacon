// 文件同步 SSE client 单测：锁定 fetch ReadableStream + Authorization 令牌注入。
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setAuth, clearAuth } from '../state/auth'
import { streamFileSyncTaskEvents } from './client'
import type { FileSyncEvent } from './types'

afterEach(() => {
  vi.restoreAllMocks()
  clearAuth()
})

describe('file-sync SSE client', () => {
  it('通过 ReadableStream 解析 SSE data 行并携带 Authorization', async () => {
    setAuth('mock-token', 'admin')
    const encoder = new TextEncoder()
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(
          encoder.encode('event: log\ndata: {"type":"log","log":{"message":"开始扫描"}}\n\n'),
        )
        controller.enqueue(
          encoder.encode('data: {"type":"task","task":{"id":"task-1","status":"running"}}\n\n'),
        )
        controller.close()
      },
    })
    const fetchMock = vi
      .spyOn(window, 'fetch')
      .mockResolvedValue(new Response(stream, { status: 200 }))

    const events: FileSyncEvent[] = []
    await streamFileSyncTaskEvents('task-1', (event) => events.push(event), { afterLogId: 12 })

    expect(fetchMock).toHaveBeenCalledWith(
      '/admin/v1/file-sync/tasks/task-1/events?afterLogId=12',
      expect.objectContaining({
        headers: expect.objectContaining({
          Accept: 'text/event-stream',
          Authorization: 'Bearer mock-token',
        }),
      }),
    )
    expect(events).toEqual([
      { type: 'log', log: { message: '开始扫描' } },
      { type: 'task', task: { id: 'task-1', status: 'running' } },
    ])
  })
})
