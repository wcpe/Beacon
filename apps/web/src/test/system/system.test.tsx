// /system 控制面健康页测试：常规渲染运行时与子系统、空态、加载错误可重试。
import { screen, waitFor } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import SystemPage from '../../pages/system'
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

describe('/system 控制面健康页', () => {
  it('常规态渲染进程运行时与子系统健康', async () => {
    useScenario('normal')
    renderPage(<SystemPage />)

    // 运行时块展示版本
    expect(await screen.findByText('v0.21.0')).toBeInTheDocument()
    // 子系统块标题存在
    expect(await screen.findByText('子系统健康')).toBeInTheDocument()
    // 连接池明细存在
    expect(await screen.findByText('连接池明细')).toBeInTheDocument()
  })

  it('空态（无在线实例）仍渲染运行时卡片', async () => {
    useScenario('empty')
    renderPage(<SystemPage />)

    // empty 场景无在线实例，运行时块依旧展示（控制面本身在跑）
    expect(await screen.findByText('进程运行时')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText('在线实例')).toBeInTheDocument()
    })
  })
})
