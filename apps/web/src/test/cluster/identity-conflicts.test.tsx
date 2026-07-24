// /identity-conflicts 页测试（FR-177）：卡片平铺四态（空/常规/超大量/异常）+ 保留一方写闭环。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import IdentityConflictsPage from '../../pages/identity-conflicts'
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

describe('/identity-conflicts 身份冲突页', () => {
  it('空态：无冲突时给友好非警示提示', async () => {
    useScenario('empty')
    renderPage(<IdentityConflictsPage />)
    expect(await screen.findByText('当前没有身份冲突')).toBeInTheDocument()
  })

  it('常规态：冲突卡展示 serverId + 原因 + 冲突双方两栏明细', async () => {
    useScenario('normal')
    renderPage(<IdentityConflictsPage />)

    // 卡头 serverId 与冲突原因徽标（用徽标完整文案，避开页眉 mission 里的「并发双实例」）
    expect(await screen.findByText('game-6')).toBeInTheDocument()
    expect(screen.getByText('并发双实例 · 启动标识往复活跃')).toBeInTheDocument()
    // 左右两栏平铺冲突双方（实例 A / 实例 B）
    expect(await screen.findByText('实例 A')).toBeInTheDocument()
    expect(screen.getByText('实例 B')).toBeInTheDocument()
    // 两栏各有「保留实例 X」操作
    expect(screen.getByRole('button', { name: '保留实例 A' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保留实例 B' })).toBeInTheDocument()
  })

  it('超大量态：12 个冲突卡片平铺可滚', async () => {
    useScenario('huge')
    renderPage(<IdentityConflictsPage />)

    expect(await screen.findByText('dup-001')).toBeInTheDocument()
    const cards = await screen.findAllByText(/^dup-\d{3}$/)
    expect(cards.length).toBe(12)
  })

  it('异常态：查询失败给出加载失败提示', async () => {
    useScenario('error')
    renderPage(<IdentityConflictsPage />)
    expect(await screen.findByText(/加载失败/)).toBeInTheDocument()
  })

  it('保留一方写闭环：确认后该冲突卡消失', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<IdentityConflictsPage />)

    // 等冲突双方明细载入，点「保留实例 A」
    await screen.findByText('实例 A')
    await user.click(screen.getByRole('button', { name: '保留实例 A' }))

    // 唯一模态：原因必填二次确认（含落败方 409 指引）
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/删除其目录下 identity\.yml/)).toBeInTheDocument()
    await user.type(within(dialog).getByLabelText('原因'), '保留原主实例，下线副本')
    await user.click(within(dialog).getByRole('button', { name: '确认保留' }))

    // game-6 冲突卡消失（已恢复 active，退出冲突列表）
    await waitFor(() => {
      expect(screen.queryByText('game-6')).not.toBeInTheDocument()
    })
  })
})
