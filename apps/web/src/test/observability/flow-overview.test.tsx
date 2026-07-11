// FlowOverview 卡（/dashboard 玩家流 / 连接流）测试：
// 1) 生产源码不得引用 @beacon/devmock（生产代码只依赖 @beacon/contracts，mock 包仅测试 / 演示装配可用）；
// 2) 时间窗按「挂载时刻 5 分钟取整」本地计算（to = 取整基准，from = to − 1h），不再取 mock 包的 BASE_MS。
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from 'vitest'

import FlowOverview from '../../pages/dashboard/flow-overview'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
  vi.restoreAllMocks()
})
afterAll(() => {
  server.close()
})

describe('FlowOverview 玩家流 / 连接流卡', () => {
  it('生产源码不引用 @beacon/devmock', () => {
    // vitest 工作目录为 apps/web，jsdom 下 import.meta.url 非 file 协议，故按 cwd 相对路径读源码
    const sourcePath = join(process.cwd(), 'src/pages/dashboard/flow-overview.tsx')
    const source = readFileSync(sourcePath, 'utf-8')
    expect(source).not.toContain('@beacon/devmock')
  })

  it('时间窗按挂载时刻 5 分钟取整计算：to = 取整基准、from = to − 1h', async () => {
    useScenario('normal')
    // 固定「当前时刻」为一个非 5 分钟整点，验证组件自行做取整
    const fixedNowMs = Date.UTC(2026, 6, 12, 8, 7, 23)
    vi.spyOn(Date, 'now').mockReturnValue(fixedNowMs)

    const capturedUrls: string[] = []
    server.use(
      http.get('/admin/v2/connections/stats', ({ request }) => {
        capturedUrls.push(request.url)
        return HttpResponse.json({ buckets: [] })
      }),
    )

    renderPage(<FlowOverview />)
    // 空桶 → 空态文案出现即请求已完成
    expect(await screen.findByText('当前时间窗内无连接活动')).toBeInTheDocument()

    const captured = capturedUrls.at(0)
    if (captured === undefined) {
      throw new Error('未捕获 connections/stats 请求')
    }
    const url = new URL(captured)
    const expectedBaseMs = Math.floor(fixedNowMs / 300_000) * 300_000
    expect(url.searchParams.get('to')).toBe(new Date(expectedBaseMs).toISOString())
    expect(url.searchParams.get('from')).toBe(new Date(expectedBaseMs - 3_600_000).toISOString())
    expect(url.searchParams.get('bucket')).toBe('5m')
  })
})
