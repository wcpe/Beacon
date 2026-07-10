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

  it('批次推进写闭环：状态机放行待确认批后即时推进到下一批', async () => {
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

    // 切到「灰度批次」Tab：状态机流呈现当前批 + 快捷操作（暂停 / 终止；页眉同款动作并存故用 All）
    await screen.findByRole('button', { name: '返回列表' })
    await user.click(screen.getByRole('tab', { name: '灰度批次' }))
    const batchesPanel = within(await screen.findByRole('tabpanel'))
    expect(await batchesPanel.findByText('当前批')).toBeInTheDocument()
    expect(batchesPanel.getByRole('button', { name: '暂停' })).toBeInTheDocument()
    expect(batchesPanel.getByRole('button', { name: '终止' })).toBeInTheDocument()

    // 待确认批（第 2 批，非末批）上是醒目主按钮「确认放行下一批」→ 确认弹窗 → 确认
    const confirmBtn = await screen.findByRole('button', { name: '确认放行下一批' })
    await user.click(confirmBtn)
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '确认推进' }))

    // 推进后即时刷新：弹窗关闭，推进指针到末批（第 3 批），按钮文案变「确认完成整单」
    await waitFor(() => {
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    })
    expect(await screen.findByRole('button', { name: '确认完成整单' })).toBeInTheDocument()
  })

  it('变更项与影响预览 Tab 使用共享预览控件', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const row = (await screen.findByText('Quests 插件灰度 v1.9')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    // 变更项 Tab（默认）：共享变更内容预览的分组清单
    expect(await screen.findByText('文件差异清单（6 项）')).toBeInTheDocument()
    expect(screen.getByText('配置变更清单（1 项）')).toBeInTheDocument()

    // 影响预览 Tab：共享编排预览（范围 / 批次 / 生效方式 / 影响面）+ 逐目标表
    await user.click(screen.getByRole('tab', { name: '影响预览' }))
    expect(await screen.findByText('目标范围')).toBeInTheDocument()
    expect(screen.getByText('批次规划')).toBeInTheDocument()
    expect(screen.getByText('生效方式')).toBeInTheDocument()
    expect(screen.getByText('影响面汇总')).toBeInTheDocument()
    expect(screen.getByText('逐目标')).toBeInTheDocument()
  })

  it('观察窗 Tab 渲染汇总与逐台指标并支持手动刷新', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const row = (await screen.findByText('Quests 插件灰度 v1.9')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })
    await user.click(screen.getByRole('tab', { name: '观察窗' }))

    // 观察说明 + 当前批标注 + 汇总条 + 逐台表
    expect(await screen.findByText('观察批次：第 2 批')).toBeInTheDocument()
    expect(screen.getByText(/确认放行下一批前/)).toBeInTheDocument()
    expect(screen.getByText('均值健康分')).toBeInTheDocument()
    expect(screen.getByText('最差健康分')).toBeInTheDocument()
    expect(screen.getByText('告警总数')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '健康分' })).toBeInTheDocument()
    // 逐台表有数据行（表头行之外至少一行）
    expect(screen.getAllByRole('row').length).toBeGreaterThan(1)

    // 手动刷新可点（请求进行中短暂置灰，不崩溃即视为闭环）
    await user.click(screen.getByRole('button', { name: '刷新' }))
    expect(await screen.findByText('观察批次：第 2 批')).toBeInTheDocument()
  })

  it('进度时间线双模式切换：可视化时间轴与详细表格', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const row = (await screen.findByText('Quests 插件灰度 v1.9')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })
    await user.click(screen.getByRole('tab', { name: '进度时间线' }))

    // 默认可视化：种子事件按「主体 · 状态」呈现（单据级关键节点）
    const eventsPanel = within(await screen.findByRole('tabpanel'))
    expect(await eventsPanel.findByText('变更单 · 灰度中')).toBeInTheDocument()
    expect(eventsPanel.getByText('变更单 · 已批准')).toBeInTheDocument()

    // 切到详细表格：全字段列头出现，可视化标题消失
    await user.click(eventsPanel.getByRole('button', { name: '详细' }))
    expect(await eventsPanel.findByRole('columnheader', { name: '序号' })).toBeInTheDocument()
    expect(eventsPanel.getByRole('columnheader', { name: '状态' })).toBeInTheDocument()
    expect(eventsPanel.queryByText('变更单 · 灰度中')).not.toBeInTheDocument()

    // 切回可视化
    await user.click(eventsPanel.getByRole('button', { name: '可视化' }))
    expect(await eventsPanel.findByText('变更单 · 灰度中')).toBeInTheDocument()
  })

  it('整单回滚与结束回滚写闭环：残留失败回滚人工收单', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 进入「排行榜插件升级 v3.1」（completed，首目标缺失备份）详情
    const row = (await screen.findByText('排行榜插件升级 v3.1')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    // 详情头部动作区「整单回滚」→ 高摩擦确认（手输「回滚」+ 原因）
    await user.click(await screen.findByRole('button', { name: '整单回滚' }))
    const dialog = await screen.findByRole('alertdialog')
    const boxes = within(dialog).getAllByRole('textbox')
    await user.type(boxes[0], '回滚')
    await user.type(boxes[1], '新版本排行异常，整单回滚')
    await user.click(within(dialog).getByRole('button', { name: '确认回滚' }))

    // 缺失备份目标回滚失败 → 单据停在回滚中：横幅显示回滚进度，动作区出现「人工结束回滚」
    expect(await screen.findByText(/回滚进度/)).toBeInTheDocument()
    expect((await screen.findAllByText('回滚中')).length).toBeGreaterThan(0)

    // 人工结束回滚 → 确认 → 单据收到已回滚
    await user.click(await screen.findByRole('button', { name: '人工结束回滚' }))
    const finishDialog = await screen.findByRole('alertdialog')
    await user.click(within(finishDialog).getByRole('button', { name: '确认结束' }))
    await waitFor(() => {
      expect(screen.getAllByText('已回滚').length).toBeGreaterThan(0)
    })
  })
})
