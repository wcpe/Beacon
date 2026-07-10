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

  it('空态给出任务卡引导（三张任务说明卡 + 引导创建按钮）', async () => {
    useScenario('empty')
    renderPage(<ChangesPage />)

    // 空态不再是一句空文案，而是任务说明卡 + 大号引导创建按钮
    expect(await screen.findByText('还没有变更单')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /更新插件 \/ 服务端文件/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /更新配置文件/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /混合交付/ })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /引导创建/ }).length).toBeGreaterThan(0)
  })

  it('行内前置基础字段可见 + 吸顶工具条与筛选存在', async () => {
    useScenario('normal')
    renderPage(<ChangesPage />)

    await screen.findByText('大厅插件升级 v2.4')
    // 行内前置列：单号 / 状态 / 批次策略 / 创建人 / 更新时间
    expect(screen.getByRole('columnheader', { name: '单号' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '批次策略' })).toBeInTheDocument()
    // 吸顶工具条：引导创建（主）+ 高级创建（保留原表单）+ 状态筛选始终可见
    expect(screen.getByRole('button', { name: '引导创建' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '高级创建' })).toBeInTheDocument()
    expect(screen.getByLabelText('按状态过滤')).toBeInTheDocument()
  })

  it('页头「交付流程」入口展开五步生命周期说明卡', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    await screen.findByText('大厅插件升级 v2.4')
    await user.click(screen.getByRole('button', { name: '交付流程' }))

    // 非模态说明卡：五步生命周期
    expect(await screen.findByText('一次交付是怎么走完的')).toBeInTheDocument()
    expect(screen.getByText('灰度批次')).toBeInTheDocument()
    expect(screen.getByText('完成 / 回滚')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('审批写闭环：对待审批单点通过后状态变为已批准', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 找到「经济系统配置调优」（pending_approval）所在行，点行打开右侧非模态详情面板
    const titleCell = await screen.findByText('经济系统配置调优')
    const row = titleCell.closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)

    // 详情面板出现（关闭按钮 + 待审批徽标，列表主列仍在故可能多处出现）且未产生模态遮罩
    await screen.findByRole('button', { name: '返回列表' })
    expect((await screen.findAllByText('待审批')).length).toBeGreaterThan(0)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    // 点「通过」→ 确认弹窗 → 确认
    await user.click(screen.getByRole('button', { name: '通过' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '通过' }))

    // 状态迁移为已批准（弹窗关闭后，详情头部徽标更新）
    await waitFor(() => {
      expect(screen.getAllByText('已批准').length).toBeGreaterThan(0)
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
    await user.click(row)

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
