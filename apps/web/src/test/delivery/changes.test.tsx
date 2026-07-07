// /changes 变更单页测试：常规列表渲染、空态引导、审批写闭环、批次推进写闭环。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ChangesPage from '../../pages/changes'
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

describe('/changes 变更单页', () => {
  it('常规态渲染变更单列表', async () => {
    useScenario('normal')
    renderPage(<ChangesPage />)

    // 列表出现已知草稿单
    expect(await screen.findByText('大厅插件升级 v2.4')).toBeInTheDocument()
    // 状态徽标（草稿）出现
    expect(await screen.findAllByText('草稿')).not.toHaveLength(0)
  })

  it('空态给出新建引导', async () => {
    useScenario('empty')
    renderPage(<ChangesPage />)

    expect(await screen.findByText('暂无变更单，点「新建变更单」发起一次交付')).toBeInTheDocument()
  })

  it('审批写闭环：对待审批单点通过后状态变为已批准', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 找到「经济系统配置调优」（pending_approval）所在行，点详情
    const titleCell = await screen.findByText('经济系统配置调优')
    const row = titleCell.closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(within(row).getByRole('button', { name: '详情' }))

    // 详情视图出现，标题显示 + 待审批徽标
    await screen.findByRole('button', { name: '返回列表' })
    expect(await screen.findByText('待审批')).toBeInTheDocument()

    // 点「通过」→ 确认弹窗 → 确认
    await user.click(screen.getByRole('button', { name: '通过' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '通过' }))

    // 状态迁移为已批准（弹窗关闭后，详情头部徽标更新）
    await waitFor(() => {
      expect(screen.getByText('已批准')).toBeInTheDocument()
    })
  })

  it('批次推进写闭环：对灰度中单确认待确认批次', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 进入「Quests 插件灰度 v1.9」（rolling）详情
    const titleCell = await screen.findByText('Quests 插件灰度 v1.9')
    const row = titleCell.closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(within(row).getByRole('button', { name: '详情' }))

    // 切到「灰度批次」Tab
    await screen.findByRole('button', { name: '返回列表' })
    await user.click(screen.getByRole('tab', { name: '灰度批次' }))

    // 找到「确认推进」按钮并点击 → 确认弹窗 → 确认
    const confirmBtn = await screen.findByRole('button', { name: '确认推进' })
    await user.click(confirmBtn)
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '确认推进' }))

    // 批次推进后：待确认批徽标消失（该批变已完成 / 下一批推进），确认弹窗关闭
    await waitFor(() => {
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    })
  })
})
