// /changes/history 交付历史页测试：常规渲染历史单、空态引导、进入详情看单服状态、整单回滚写闭环。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ChangesHistoryPage from '../../pages/changes-history'
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

describe('/changes/history 交付历史页', () => {
  it('常规态渲染历史变更单列表', async () => {
    useScenario('normal')
    renderPage(<ChangesHistoryPage />)

    // 已完成单出现
    expect(await screen.findByText('全网核心配置基线对齐')).toBeInTheDocument()
    // 已回滚单出现
    expect(await screen.findByText('坏更新整单回滚示例')).toBeInTheDocument()
  })

  it('空态给出引导文案', async () => {
    useScenario('empty')
    renderPage(<ChangesHistoryPage />)

    expect(await screen.findByText('暂无历史变更单')).toBeInTheDocument()
  })

  it('点行打开右侧非模态详情面板并展示执行回放（批次状态机 + 单服状态）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesHistoryPage />)

    const row = (await screen.findByText('全网核心配置基线对齐')).closest('tr')
    expect(row).not.toBeNull()
    await user.click(row as HTMLElement)

    // 默认「执行回放」Tab：批次状态机只读回放 + 单服状态表，且未产生模态遮罩
    expect(await screen.findByText('批次状态')).toBeInTheDocument()
    expect(await screen.findByText('第 1 批')).toBeInTheDocument()
    expect(await screen.findByText('单服状态')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    // 只读回放：不出现放行按钮
    expect(screen.queryByRole('button', { name: '确认放行下一批' })).not.toBeInTheDocument()
  })

  it('历史详情复用共享控件：变更内容 / 交付编排 / 进度时间线可完整追溯', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesHistoryPage />)

    const row = (await screen.findByText('全网核心配置基线对齐')).closest('tr')
    await user.click(row as HTMLElement)
    await screen.findByText('批次状态')

    // 变更内容 Tab：共享变更内容预览（当时改了什么）
    await user.click(screen.getByRole('tab', { name: '变更内容' }))
    expect(await screen.findByText('配置变更清单（1 项）')).toBeInTheDocument()

    // 交付编排 Tab：共享编排预览（发给谁 / 怎么编排）
    await user.click(screen.getByRole('tab', { name: '交付编排' }))
    expect(await screen.findByText('目标范围')).toBeInTheDocument()
    expect(screen.getByText('批次规划')).toBeInTheDocument()
    expect(screen.getByText('生效方式')).toBeInTheDocument()

    // 进度时间线 Tab：共享双模式时间线（出过什么事）
    await user.click(screen.getByRole('tab', { name: '进度时间线' }))
    const timelinePanel = within(await screen.findByRole('tabpanel'))
    expect(await timelinePanel.findByText('变更单 · 已完成')).toBeInTheDocument()
    await user.click(timelinePanel.getByRole('button', { name: '详细' }))
    expect(await timelinePanel.findByRole('columnheader', { name: '序号' })).toBeInTheDocument()
  }, 20_000)

  it('行内前置基础字段可见 + 吸顶筛选存在', async () => {
    useScenario('normal')
    renderPage(<ChangesHistoryPage />)

    await screen.findByText('全网核心配置基线对齐')
    expect(screen.getByRole('columnheader', { name: '单号' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '结束时间' })).toBeInTheDocument()
    expect(screen.getByLabelText('按状态过滤')).toBeInTheDocument()
  })

  it('整单回滚写闭环：完成单回滚后状态推进', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesHistoryPage />)

    // 进入已完成单详情
    const row = (await screen.findByText('全网核心配置基线对齐')).closest('tr')
    await user.click(row as HTMLElement)
    await screen.findByText('单服状态')

    // 触发整单回滚
    await user.click(await screen.findByRole('button', { name: '整单回滚' }))
    const dialog = await screen.findByRole('alertdialog')
    // 高摩擦：手输复述「回滚」 + 原因（两个 textbox：phrase / reason）
    const textboxes = within(dialog).getAllByRole('textbox')
    await user.type(textboxes[0], '回滚')
    await user.type(textboxes[1], '新版本异常，回滚')
    await user.click(within(dialog).getByRole('button', { name: '确认回滚' }))

    // 回滚后状态标签变为「已回滚」（该完成单无缺失备份，一次回滚到 rolled_back）
    await waitFor(() => {
      expect(screen.getAllByText('已回滚').length).toBeGreaterThan(0)
    })
  })
})
