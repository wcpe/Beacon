// /dashboard 运维总览页测试：健康 / 玩家流 / 告警 / 调度四区渲染、空态、下钻链接。
import { screen, within } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import DashboardPage from '../../pages/dashboard'
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

describe('/dashboard 运维总览页', () => {
  it('常规态渲染健康 / 玩家流 / 告警 / 调度四区', async () => {
    useScenario('normal')
    renderPage(<DashboardPage />)

    expect(await screen.findByText('集群健康总览')).toBeInTheDocument()
    expect(await screen.findByText('玩家流 / 连接流')).toBeInTheDocument()
    expect(await screen.findByText('告警概览')).toBeInTheDocument()
    expect(await screen.findByText('调度概览')).toBeInTheDocument()
  })

  it('健康区提供下钻到服务器页的入口', async () => {
    useScenario('normal')
    renderPage(<DashboardPage />)

    const link = await screen.findByRole('link', { name: '前往服务器' })
    expect(link).toHaveAttribute('href', '/servers')
  })

  it('空态健康区给出接入引导', async () => {
    useScenario('empty')
    renderPage(<DashboardPage />)

    expect(
      await screen.findByText('暂无健康数据，接入服务器后展示集群健康总览'),
    ).toBeInTheDocument()
  })

  it('调度区提供下钻到告警事件页的入口', async () => {
    useScenario('normal')
    renderPage(<DashboardPage />)

    const card = (await screen.findByText('调度概览')).closest('section')
    if (card === null) {
      throw new Error('未找到调度概览卡')
    }
    const link = within(card).getByRole('link', { name: '前往告警事件' })
    expect(link).toHaveAttribute('href', '/alert-events')
  })
})
