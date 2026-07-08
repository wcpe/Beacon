// /service-analysis 服务分析页测试：服务器选择 + 指标时序渲染、空态、选服后出时序。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ServiceAnalysisPage from '../../pages/service-analysis'
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

describe('/service-analysis 服务分析页', () => {
  it('常规态渲染可选服务器清单', async () => {
    useScenario('normal')
    renderPage(<ServiceAnalysisPage />)

    // 服务器选择区标题出现
    expect(await screen.findByText('选择服务器（可多选对比）')).toBeInTheDocument()
    // 出现可选的在线子服（devmock 已知 lobby-1 等）
    await waitFor(() => {
      expect(screen.getAllByRole('checkbox').length).toBeGreaterThan(0)
    })
  })

  it('空态给出无可分析子服提示', async () => {
    useScenario('empty')
    renderPage(<ServiceAnalysisPage />)

    expect(
      await screen.findByText('暂无在线子服可供分析，接入并分配子服后展示指标时序'),
    ).toBeInTheDocument()
  })

  it('勾选服务器后展示指标时序区', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await screen.findByText('选择服务器（可多选对比）')
    const firstCheckbox = (await screen.findAllByRole('checkbox'))[0]
    await user.click(firstCheckbox)

    // 时序区出现（含指标切换）
    await waitFor(() => {
      expect(screen.getByText('指标时序')).toBeInTheDocument()
    })
    expect(screen.getByLabelText('指标')).toBeInTheDocument()
  })

  it('搜索关键词过滤左侧服务器清单', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ServiceAnalysisPage />)

    await screen.findByText('选择服务器（可多选对比）')
    // 首屏应含 lobby-* 与 game-* 在线子服
    await waitFor(() => {
      expect(screen.getByRole('checkbox', { name: 'lobby-1' })).toBeInTheDocument()
    })
    expect(screen.getByRole('checkbox', { name: 'game-1' })).toBeInTheDocument()

    // 搜索 lobby 后仅剩 lobby-* 候选
    await user.type(screen.getByLabelText('搜索 serverId'), 'lobby')
    await waitFor(() => {
      expect(screen.queryByRole('checkbox', { name: 'game-1' })).not.toBeInTheDocument()
    })
    expect(screen.getByRole('checkbox', { name: 'lobby-1' })).toBeInTheDocument()
  })
})
