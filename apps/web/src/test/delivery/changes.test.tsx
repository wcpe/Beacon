// /changes 变更单页测试：常规列表渲染、空态引导、审批写闭环、批次推进写闭环、提审必填原因。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
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

  it('页头「交付流程」入口以模态弹窗展示五步生命周期', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    await screen.findByText('大厅插件升级 v2.4')
    await user.click(screen.getByRole('button', { name: '交付流程' }))

    // 模态弹窗：五步生命周期（不再内联撑开、不把列表下推）
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('一次交付是怎么走完的')).toBeInTheDocument()
    expect(within(dialog).getByText('灰度批次')).toBeInTheDocument()
    expect(within(dialog).getByText('完成 / 回滚')).toBeInTheDocument()
  })

  it('待审批详情只留「撤回」入口（跳审批中心），不再有通过 / 驳回按钮，且审批进度可见', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 「经济系统配置调优」为种子待审批单（关联审批申请 apr_change_9101）
    const titleCell = await screen.findByText('经济系统配置调优')
    const row = titleCell.closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    // 审批决定只在统一审批中心：详情页不得再出现通过 / 驳回（旧入口真机 403）
    expect(screen.queryByRole('button', { name: '通过' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '驳回' })).not.toBeInTheDocument()
    // 撤回改走审批中心（跳转，不在本页直调废弃的 /withdraw）
    expect(screen.getByRole('link', { name: '撤回' })).toHaveAttribute('href', '/approvals')

    // 审批进度视图：状态 / 审批人 / 申请号 / 去审批中心入口
    expect(await screen.findByText('审批进度')).toBeInTheDocument()
    expect(screen.getByText('apr_change_9101')).toBeInTheDocument()
    expect(screen.getAllByText('待审批').length).toBeGreaterThan(0)
    expect(screen.getByText(/审批人：待处理/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /查看申请/ })).toHaveAttribute(
      'href',
      '/approvals/apr_change_9101',
    )
    expect(screen.getAllByRole('link', { name: '去审批中心' }).length).toBeGreaterThan(0)
  }, 20_000)

  it('批次放行走审批申请：确认后只有票据反馈，批次不立即推进', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 进「Quests 插件灰度 v1.9」（rolling）详情 → 灰度批次 Tab
    const titleCell = await screen.findByText('Quests 插件灰度 v1.9')
    const row = titleCell.closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })
    await user.click(screen.getByRole('tab', { name: '灰度批次' }))
    const batchesPanel = within(await screen.findByRole('tabpanel'))
    expect(await batchesPanel.findByText('当前批')).toBeInTheDocument()

    // 待确认批上「确认放行下一批」→ 确认弹窗 → 确认（携幂等键的申请动作）
    await user.click(await screen.findByRole('button', { name: '确认放行下一批' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.click(within(dialog).getByRole('button', { name: '确认推进' }))

    // 反馈是「已提交审批（申请号 X）」票据，而非「已完成」：批次仍停在待确认门
    expect(await screen.findByText(/已提交审批（申请号/)).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    })
    expect(await screen.findByRole('button', { name: '确认放行下一批' })).toBeInTheDocument()
  }, 20_000)

  // 单次渲染巡检四个只读 Tab（合并跑，避免多次整页渲染在并行 worker 下拖爆时限）
  it('批次放行连续两次各用新幂等键（成功即作废，不复用旧键）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    const keys: (string | null)[] = []
    server.use(
      http.post('*/admin/v2/change-orders/:id/batches/:batchNo/confirm', ({ request }) => {
        keys.push(request.headers.get('Idempotency-Key'))
        return HttpResponse.json(
          {
            approvalRequestId: `apr_change_5004_${String(keys.length)}`,
            status: 'pending',
            operationKey: 'delivery.confirm_batch',
            orderId: 5004,
            impactSummary: { targetCount: 6, batchCount: 3, payloadFiles: 6, payloadConfigs: 1 },
          },
          { status: 202 },
        )
      }),
    )
    renderPage(<ChangesPage />)

    const row = (await screen.findByText('Quests 插件灰度 v1.9')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })
    await user.click(screen.getByRole('tab', { name: '灰度批次' }))
    await screen.findByText('当前批')

    // 第一次放行意图
    await user.click(await screen.findByRole('button', { name: '确认放行下一批' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '确认推进' }))
    expect(await screen.findByText(/已提交审批（申请号/)).toBeInTheDocument()
    await waitFor(() => {
      expect(keys).toHaveLength(1)
    })

    // 第二次放行意图（真机上下一批 payload 已变）：必须换新键，否则服务端按
    // idempotency_key_reused 拒掉，后续批次永远放不出去
    await user.click(await screen.findByRole('button', { name: '确认放行下一批' }))
    await user.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: '确认推进' }))
    await waitFor(() => {
      expect(keys).toHaveLength(2)
    })
    expect(keys[0]).not.toBeNull()
    expect(keys[0] ?? '').not.toBe('')
    expect(keys[1]).not.toBe(keys[0])
  }, 20_000)

  it('详情四 Tab 复用共享控件：变更项 / 影响预览 / 观察窗 / 时间线双模式', async () => {
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

    // 影响预览 Tab：共享编排预览（范围 / 批次 / 生效方式 / 影响面）+ 逐目标表（含配置命中列）
    await user.click(screen.getByRole('tab', { name: '影响预览' }))
    expect(await screen.findByText('目标范围')).toBeInTheDocument()
    expect(screen.getByText('批次规划')).toBeInTheDocument()
    expect(screen.getByText('生效方式')).toBeInTheDocument()
    expect(screen.getByText('影响面汇总')).toBeInTheDocument()
    expect(screen.getByText('逐目标')).toBeInTheDocument()
    // 配置命中列：种子配置项 zone:30（#9001→#9002）命中 zone 30 的目标，未命中目标显示 —
    expect(screen.getByRole('columnheader', { name: '配置命中' })).toBeInTheDocument()
    expect((await screen.findAllByText('zone:30 #9001→#9002')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('—').length).toBeGreaterThan(0)

    // 观察窗 Tab：观察说明 + 当前批标注 + 汇总条 + 逐台表 + 手动刷新
    await user.click(screen.getByRole('tab', { name: '观察窗' }))
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

    // 进度时间线 Tab：默认可视化，种子事件按「主体 · 状态」呈现（单据级关键节点）
    await user.click(screen.getByRole('tab', { name: '进度时间线' }))
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
  }, 20_000)

  it('变更项 Tab 文件行预览按真机契约展示「需审批」引导（不静默失败）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const row = (await screen.findByText('Quests 插件灰度 v1.9')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    // 变更项 Tab 默认展示：文件差异行带「预览」按钮（懒加载，点开才取）
    const previews = await screen.findAllByRole('button', { name: '预览' })
    expect(previews.length).toBeGreaterThan(0)

    // 真机对读源服文件内容恒 409 operation_requires_approval：点开即给审批引导 + 审批中心入口
    const textRow = screen
      .getAllByText('plugins/Essentials/config.yml')
      .map((el) => el.closest('li'))
      .find((li): li is HTMLLIElement => li !== null && within(li).queryByRole('button', { name: '预览' }) !== null)
    if (!textRow) {
      throw new Error('未找到文本差异项所在行')
    }
    await user.click(within(textRow).getByRole('button', { name: '预览' }))
    expect(await within(textRow).findByText('该文件内容需审批后查看')).toBeInTheDocument()
    expect(within(textRow).getByRole('link', { name: '去审批中心' })).toHaveAttribute('href', '/approvals')
  }, 20_000)

  it('详情页提审必填原因：未填写不可确认，填写后提审进入待审批', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 进入「大厅插件升级 v2.4」（draft）详情
    const row = (await screen.findByText('大厅插件升级 v2.4')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    // 草稿单「提交审批」→ 确认弹窗带必填原因输入（与驳回 / 终止同形）
    await user.click(await screen.findByRole('button', { name: '提交审批' }))
    const dialog = await screen.findByRole('alertdialog')
    const confirmButton = within(dialog).getByRole('button', { name: '提交审批' })
    expect(confirmButton).toBeDisabled()

    await user.type(within(dialog).getByRole('textbox'), '插件已在模板源验证通过，申请审批')
    expect(confirmButton).toBeEnabled()
    await user.click(confirmButton)

    // 提审成功：弹窗关闭、状态迁移为待审批。devmock 已对齐后端的两道守卫（原因非空 +
    // 幂等键合法），故这一条同时锁住「原因与 Idempotency-Key 都真的发出且合法」
    await waitFor(() => {
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    })
    expect((await screen.findAllByText('待审批')).length).toBeGreaterThan(0)
  }, 20_000)

  it('提审后审批进度卡显示新产生的申请（走 devmock 全量 handlers，锁死资源类型真值）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 草稿单「大厅插件升级 v2.4」此前无任何申请：进度卡不渲染
    const row = (await screen.findByText('大厅插件升级 v2.4')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })
    expect(screen.queryByText('审批进度')).not.toBeInTheDocument()

    await user.click(await screen.findByRole('button', { name: '提交审批' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByRole('textbox'), '插件已在模板源验证通过，申请审批')
    await user.click(within(dialog).getByRole('button', { name: '提交审批' }))

    // 票据反馈带申请号
    expect(await screen.findByText(/已提交审批（申请号/)).toBeInTheDocument()

    // 进度卡按单号反查必须命中这条新申请（真机资源类型是 change-order 带连字符；
    // mock 物化行写错资源类型时这里查不到 → 本用例即该回归的守卫）。
    // toast 文案里也有申请号，故取落在卡片行（li）内的那一个
    expect(await screen.findByText('审批进度')).toBeInTheDocument()
    // 用卡片行内的「查看申请」深链定位（toast 里也有申请号，不能按文本取行）
    const applyLink = await screen.findByRole('link', { name: /查看申请/ })
    expect(applyLink).toHaveAttribute('href', expect.stringContaining('/approvals/apr_change_5001_'))
    const cardRow = applyLink.closest('li')
    if (!cardRow) {
      throw new Error('审批进度卡未显示新产生的申请')
    }
    expect(within(cardRow).getByText('待审批')).toBeInTheDocument()
  }, 20_000)

  it('详情页提审携带幂等键，且重试复用同一键', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    // 首次失败、二次成功，用于观察两次提审是否复用同一幂等键
    const keys: (string | null)[] = []
    let attempt = 0
    server.use(
      http.post('*/admin/v2/change-orders/:id/submit', ({ request }) => {
        keys.push(request.headers.get('Idempotency-Key'))
        attempt += 1
        if (attempt === 1) {
          return HttpResponse.json({ code: 'internal_error', message: '模拟瞬时失败' }, { status: 500 })
        }
        return HttpResponse.json({ id: 5001 }, { status: 200 })
      }),
    )
    renderPage(<ChangesPage />)

    const row = (await screen.findByText('大厅插件升级 v2.4')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    await user.click(await screen.findByRole('button', { name: '提交审批' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByRole('textbox'), '插件已验证，申请审批')
    await user.click(within(dialog).getByRole('button', { name: '提交审批' }))

    // 首次失败：脱敏错误内联可见、弹窗保持打开
    expect(await within(dialog).findByText('模拟瞬时失败')).toBeInTheDocument()

    // 同一提审意图原样重试：幂等键必须复用，后端据此去重（换键会让重试变成新申请）
    await user.click(within(dialog).getByRole('button', { name: '提交审批' }))
    await waitFor(() => {
      expect(keys).toHaveLength(2)
    })
    expect(keys[0]).not.toBeNull()
    expect(keys[0] ?? '').not.toBe('')
    expect(keys[1]).toBe(keys[0])
  }, 20_000)

  it('熔断暂停的「继续」必填原因与恢复方式（真机对非人工暂停强制 reason 非空）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    const captured: { key: string | null; body: unknown } = { key: null, body: null }
    server.use(
      http.post('*/admin/v2/change-orders/:id/resume', async ({ request }) => {
        captured.key = request.headers.get('Idempotency-Key')
        captured.body = (await request.json()) as unknown
        return HttpResponse.json(
          {
            approvalRequestId: 'apr_change_5006_1',
            status: 'pending',
            operationKey: 'delivery.resume',
            orderId: 5006,
            impactSummary: { targetCount: 1, batchCount: 1, payloadFiles: 6, payloadConfigs: 1 },
          },
          { status: 202 },
        )
      }),
    )
    renderPage(<ChangesPage />)

    // 「PVP 平衡性补丁」为熔断暂停单（pauseKind=circuit_break）
    const row = (await screen.findByText('PVP 平衡性补丁')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    await user.click(await screen.findByRole('button', { name: '继续' }))
    const dialog = await screen.findByRole('alertdialog')
    // 恢复方式选择 + 原因必填（未填不可提交）
    expect(within(dialog).getByRole('combobox', { name: '恢复方式' })).toBeInTheDocument()
    const confirmButton = within(dialog).getByRole('button', { name: '继续' })
    expect(confirmButton).toBeDisabled()

    await user.type(within(dialog).getByRole('textbox'), '熔断原因已排除，重试失败目标')
    expect(confirmButton).toBeEnabled()
    await user.click(confirmButton)

    // 请求体带 mode + reason，请求头带幂等键；成功回票据反馈
    await waitFor(() => {
      expect(captured.key).not.toBeNull()
    })
    expect(captured.body).toEqual({ mode: 'retry_failed', reason: '熔断原因已排除，重试失败目标' })
    expect(await screen.findByText(/已提交审批（申请号/)).toBeInTheDocument()
  }, 20_000)

  it('提审被后端拒绝时内联展示脱敏错误，弹窗不关闭且单据仍为草稿', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    // 模拟真实控制面以缺原因拒绝提审（前端不得静默吞掉写操作错误）
    server.use(
      http.post('*/admin/v2/change-orders/:id/submit', () =>
        HttpResponse.json(
          { code: 'approval_reason_required', message: '审批原因不能为空', traceId: 'trace-test' },
          { status: 400 },
        ),
      ),
    )
    renderPage(<ChangesPage />)

    const row = (await screen.findByText('大厅插件升级 v2.4')).closest('tr')
    if (!row) {
      throw new Error('未找到变更单所在行')
    }
    await user.click(row)
    await screen.findByRole('button', { name: '返回列表' })

    await user.click(await screen.findByRole('button', { name: '提交审批' }))
    const dialog = await screen.findByRole('alertdialog')
    await user.type(within(dialog).getByRole('textbox'), '插件已验证，申请审批')
    await user.click(within(dialog).getByRole('button', { name: '提交审批' }))

    // 失败原因（后端脱敏文案）内联可见，弹窗保持打开供重试，单据未迁移
    expect(await within(dialog).findByText('审批原因不能为空')).toBeInTheDocument()
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    expect(screen.getAllByText('草稿').length).toBeGreaterThan(0)
  }, 20_000)

  it('?order= 深链自动选中该单并打开详情面板', async () => {
    useScenario('normal')
    renderPage(<ChangesPage />, ['/changes?order=5002'])

    // 免点击：右侧非模态详情面板自动打开（经济系统配置调优，待审批）
    await screen.findByRole('button', { name: '返回列表' })
    expect((await screen.findAllByText('经济系统配置调优')).length).toBeGreaterThan(0)
    expect((await screen.findAllByText('待审批')).length).toBeGreaterThan(0)
  })

  it('?order= 深链目标不存在：脱敏真因内联展示，列表仍可用', async () => {
    useScenario('normal')
    renderPage(<ChangesPage />, ['/changes?order=999999'])

    // 加载失败提示（后端 message：变更单不存在），不静默吞掉；列表照常渲染
    expect(await screen.findByText(/打开变更单 #999999 失败/)).toBeInTheDocument()
    expect(await screen.findByText('大厅插件升级 v2.4')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '返回列表' })).not.toBeInTheDocument()
  })

  it('作用域选择走组件库 Select（可展开命名空间选项）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)
    await screen.findByText('大厅插件升级 v2.4')

    // NamespacePicker 现为组件库 Select（combobox 角色），可展开出命名空间选项
    await user.click(screen.getByRole('combobox', { name: '选择 命名空间' }))
    expect(await screen.findByRole('option', { name: 'test' })).toBeInTheDocument()
  })

  it('整单回滚走审批申请：提交后仅票据反馈，单据状态不变', async () => {
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

    // 真机回滚是申请动作（202 票据）：提交后只有票据反馈，单据仍是已完成（不进入回滚中）
    expect(await screen.findByText(/已提交审批（申请号/)).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByRole('alertdialog')).not.toBeInTheDocument()
    })
    expect(screen.getAllByText('已完成').length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: '人工结束回滚' })).not.toBeInTheDocument()
  }, 20_000)
})
