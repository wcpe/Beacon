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

    // E5：健康区不再渲染区段标题（页面身份在页眉面包屑），仅 KPI 卡行
    expect(await screen.findByText('可调度服务器')).toBeInTheDocument()
    expect(screen.queryByText('集群健康总览')).not.toBeInTheDocument()
    expect(await screen.findByText('玩家流 / 连接流')).toBeInTheDocument()
    expect(await screen.findByText('告警概览')).toBeInTheDocument()
    expect(await screen.findByText('调度概览')).toBeInTheDocument()
  })

  it('服务器页入口由状态墙承担，健康区不再重复提供', async () => {
    useScenario('normal')
    renderPage(<DashboardPage />)

    // 健康区的「前往服务器」已移除（同页不留重复入口）
    expect(await screen.findByText('可调度服务器')).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '前往服务器' })).not.toBeInTheDocument()
    // /servers 入口仍可达：状态墙卡的「查看全部」
    expect(screen.getByRole('link', { name: '查看全部' })).toHaveAttribute('href', '/servers')
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
