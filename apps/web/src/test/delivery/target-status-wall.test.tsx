// 单服级状态墙（FR-254）组件级断言：逐台展示正推状态、失败原因、回滚状态与备份标记，
// 让「执行中哪台到哪一步、哪台失败为什么」可见（此前 error / rollbackError 全站无渲染）。
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import TargetStatusWall from '../../features/delivery/target-status-wall'
import { RollbackRecordsSection } from '../../features/delivery/order-rollback'
import { fetchChangeOrders, fetchChangeOrder } from '../../api/delivery-changes'
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

/** 按标题取种子单号（避免测试硬编码种子顺序） */
async function orderIdByTitle(title: string): Promise<number> {
  const list = await fetchChangeOrders({ keyword: title, pageSize: 20 })
  const row = list.items.find((item) => item.title === title)
  if (!row) {
    throw new Error(`种子缺少变更单 ${title}`)
  }
  return row.id
}

describe('TargetStatusWall 单服级状态墙', () => {
  it('熔断暂停单：逐台可见失败状态与失败原因，并标出备份缺失', async () => {
    useScenario('normal')
    const orderId = await orderIdByTitle('PVP 平衡性补丁')
    const detail = await fetchChangeOrder(orderId)
    const failed = detail.batches.find((batch) => batch.status === 'failed')
    expect(failed).toBeDefined()

    renderPage(<TargetStatusWall orderId={orderId} orderStatus={detail.status} />)

    // 区块标题 + 汇总（总台数与失败计数）
    expect(await screen.findByText('单服状态')).toBeInTheDocument()
    expect(await screen.findByText(/共 \d+ 台目标/)).toBeInTheDocument()
    // 计数只覆盖当前页，文案显式限定「本页」（主口径是响应 total）
    expect(screen.getByText(/本页 \d+ 台失败/)).toBeInTheDocument()
    // 状态徽标（失败）与失败原因（真因可见，不静默隐藏）
    const table = screen.getByRole('table')
    expect(within(table).getAllByText('失败').length).toBeGreaterThan(0)
    expect(within(table).getByText('生效超时：重启后 300 秒内心跳未回归')).toBeInTheDocument()
    // 备份标记两态之一可见（失败目标仍应显示是否留有备份）
    expect(within(table).getAllByText(/已备份|无备份/).length).toBeGreaterThan(0)
  })

  it('灰度中单：逐台展示正推状态与批次归属，未到的批次显示待推送', async () => {
    useScenario('normal')
    const orderId = await orderIdByTitle('Quests 插件灰度 v1.9')
    const detail = await fetchChangeOrder(orderId)

    renderPage(<TargetStatusWall orderId={orderId} orderStatus={detail.status} />)

    const table = await screen.findByRole('table')
    // 表头：正推状态 / 回滚状态 / 失败原因 / 回滚失败原因 / 备份
    expect(within(table).getByRole('columnheader', { name: '正推状态' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '失败原因' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '回滚失败原因' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '备份' })).toBeInTheDocument()
    // 已生效 / 待推送两态并存（当前批已生效、后续批次待推送）
    expect(within(table).getAllByText('已生效').length).toBeGreaterThan(0)
    expect(within(table).getAllByText('待推送').length).toBeGreaterThan(0)
    // 未进入回滚的目标给「未回滚」占位，不显示空白
    expect(within(table).getAllByText('未回滚').length).toBeGreaterThan(0)
  })

  it('回滚中单：回滚失败目标的原因按脱敏真因展示，成功目标显示已回滚', async () => {
    useScenario('normal')
    const orderId = await orderIdByTitle('坏更新整单回滚示例')
    const detail = await fetchChangeOrder(orderId)
    // 注入一行回滚失败 + 一行回滚成功（回滚失败原因是运维定位关键，必须可见）
    server.use(
      http.get('*/admin/v2/change-orders/:id/targets', () =>
        HttpResponse.json({
          items: [
            {
              serverId: 'game-1',
              batchNo: 1,
              status: 'activated',
              pushedAt: null,
              activatedAt: null,
              changedFileCount: 5,
              skippedFileCount: 0,
              backupPresent: false,
              error: null,
              rollbackStatus: 'failed',
              rollbackError: '备份不存在（已被保留策略清理），无法文件回滚',
            },
            {
              serverId: 'game-2',
              batchNo: 2,
              status: 'activated',
              pushedAt: null,
              activatedAt: null,
              changedFileCount: 5,
              skippedFileCount: 0,
              backupPresent: true,
              error: null,
              rollbackStatus: 'rolled_back',
              rollbackError: null,
            },
          ],
          total: 2,
        }),
      ),
    )

    renderPage(<TargetStatusWall orderId={orderId} orderStatus={detail.status} />)

    const table = await screen.findByRole('table')
    expect(within(table).getByText('game-1')).toBeInTheDocument()
    expect(within(table).getByText('回滚失败')).toBeInTheDocument()
    expect(within(table).getByText('备份不存在（已被保留策略清理），无法文件回滚')).toBeInTheDocument()
    expect(within(table).getByText('已回滚')).toBeInTheDocument()
    // 汇总行：总台数 + 本页回滚失败计数（同一段落内，用正则匹配）
    expect(screen.getByText(/本页 1 台回滚失败/)).toBeInTheDocument()
  })
})

// —— FR-270 目标级（子集）回滚 / FR-271 交付版本与回滚记录 / FR-255r 回滚预检 ——

describe('目标级回滚与回滚预检', () => {
  const subsetOrder = {
    orderId: 9001,
    detail: {
      id: 9001,
      status: 'completed' as const,
      targets: [
        {
          serverId: 't-1',
          batchNo: 1,
          status: 'activated' as const,
          pushedAt: '2026-07-16T07:00:00Z',
          activatedAt: '2026-07-16T07:05:00Z',
          changedFileCount: 3,
          skippedFileCount: 0,
          backupPresent: true,
          error: null,
          rollbackStatus: null,
          rollbackError: null,
          deliveredVersion: {
            serverId: 't-1',
            orderId: 9000,
            orderTitle: '上一单交付',
            activatedAt: '2026-07-16T07:05:00Z',
          },
        },
        {
          serverId: 't-2',
          batchNo: 1,
          status: 'activated' as const,
          pushedAt: '2026-07-16T07:00:00Z',
          activatedAt: '2026-07-16T07:05:00Z',
          changedFileCount: 3,
          skippedFileCount: 0,
          // 备份缺失：回滚预检必须点出来（FR-255r）
          backupPresent: false,
          error: null,
          rollbackStatus: null,
          rollbackError: null,
          deliveredVersion: null,
        },
      ],
    },
  }

  function stubSubsetOrder(): { bodies: unknown[]; headers: (string | null)[] } {
    const bodies: unknown[] = []
    const headers: (string | null)[] = []
    server.use(
      http.get('*/admin/v2/change-orders/:id/targets', () =>
        HttpResponse.json({ items: subsetOrder.detail.targets, total: 2 }),
      ),
      http.post('*/admin/v2/change-orders/:id/rollback/targets', async ({ request }) => {
        bodies.push(await request.json())
        headers.push(request.headers.get('Idempotency-Key'))
        return HttpResponse.json(
          {
            approvalRequestId: 'apr_subset_1',
            status: 'pending',
            operationKey: 'delivery.rollback',
            orderId: 9001,
            impactSummary: { targetCount: 1, batchCount: 1, payloadFiles: 0, payloadConfigs: 0 },
          },
          { status: 202 },
        )
      }),
    )
    return { bodies, headers }
  }

  it('FR-271：逐台显示当前交付版本，无交付记录显示「无交付记录」', async () => {
    useScenario('normal')
    stubSubsetOrder()
    renderPage(<TargetStatusWall orderId={subsetOrder.orderId} orderStatus="completed" />)

    const table = await screen.findByRole('table')
    expect(within(table).getByRole('columnheader', { name: '当前交付版本' })).toBeInTheDocument()
    expect(within(table).getByText(/单 #9000/)).toBeInTheDocument()
    expect(within(table).getByText('无交付记录')).toBeInTheDocument()
  })

  it('FR-255r：勾选目标后弹窗先列明「不可文件回滚」的台', async () => {
    useScenario('normal')
    stubSubsetOrder()
    renderPage(<TargetStatusWall orderId={subsetOrder.orderId} orderStatus="completed" />)

    const table = await screen.findByRole('table')
    fireEvent.click(within(table).getByRole('checkbox', { name: 't-2' }))
    fireEvent.click(screen.getByRole('button', { name: /回滚选中目标/ }))

    expect(await screen.findByText('回滚预检')).toBeInTheDocument()
    expect(screen.getByText(/1 台因备份缺失不可文件回滚/)).toBeInTheDocument()
    // 逐台清单给出 serverId，运维能直接对上是哪台（弹窗内与表格里各有一处）
    expect(screen.getAllByText(/t-2/).length).toBeGreaterThan(0)
  })

  it('FR-270：回滚选中目标提交 serverIds 与幂等键，并明示配置未回退', async () => {
    useScenario('normal')
    const { bodies, headers } = stubSubsetOrder()
    renderPage(<TargetStatusWall orderId={subsetOrder.orderId} orderStatus="completed" />)

    const table = await screen.findByRole('table')
    fireEvent.click(within(table).getByRole('checkbox', { name: 't-1' }))
    fireEvent.click(screen.getByRole('button', { name: /回滚选中目标/ }))

    // 弹窗内必须明示配置版本不回退（不可忽略，不是折叠说明）
    expect(await screen.findByText(/配置版本不会回退/)).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText(/请输入「回滚」以确认/), { target: { value: '回滚' } })
    fireEvent.change(screen.getByLabelText('回滚原因'), { target: { value: '该台版本启动异常' } })
    fireEvent.click(screen.getByRole('button', { name: '确认回滚选中目标' }))

    await waitFor(() => {
      expect(bodies.length).toBe(1)
    })
    expect(bodies[0]).toEqual({ reason: '该台版本启动异常', serverIds: ['t-1'] })
    expect(headers[0]).toBeTruthy()
    // 申请成功给出票据反馈（申请号 + 去审批中心），不静默
    expect(await screen.findByText(/apr_subset_1/)).toBeInTheDocument()
  })

  it('FR-271：回滚记录按动作列出原因、台数与逐台结果，并标注配置是否回退', async () => {
    useScenario('normal')
    server.use(
      http.get('*/admin/v2/change-orders/:id/rollback-records', () =>
        HttpResponse.json({
          items: [
            {
              id: 2,
              kind: 'targets',
              reason: '只回滚出问题的一台',
              operator: 'ops-chen',
              configRolledBack: false,
              targetCount: 1,
              createdAt: '2026-07-16T08:00:00Z',
              targets: [{ serverId: 't-1', result: 'rolled_back', error: null }],
            },
            {
              id: 1,
              kind: 'order',
              reason: '整单回退',
              operator: 'ops-chen',
              configRolledBack: true,
              targetCount: 2,
              createdAt: '2026-07-16T07:30:00Z',
              targets: [
                { serverId: 't-1', result: 'failed', error: '备份不存在，无法文件回滚' },
                { serverId: 't-2', result: 'rolled_back', error: null },
              ],
            },
          ],
        }),
      ),
    )
    renderPage(<RollbackRecordsSection orderId={9001} />)

    expect(await screen.findByText('回滚记录')).toBeInTheDocument()
    // 子集动作标注「配置未回退」，整单动作标注「配置版本已回退」
    expect(await screen.findByText('目标级回滚')).toBeInTheDocument()
    expect(screen.getByText('配置未回退')).toBeInTheDocument()
    expect(screen.getByText('配置版本已回退')).toBeInTheDocument()
    // 逐台结果与失败原因都在记录里（不只能翻审计）
    expect(screen.getByText(/t-1：rolled_back/)).toBeInTheDocument()
    expect(screen.getByText(/失败原因：备份不存在，无法文件回滚/)).toBeInTheDocument()
  })
})

// —— 返工：全选判定必须以「可回滚目标数」（曾推送）为基数，不能用 total ——

describe('全选判定基数（可回滚口径）', () => {
  const target = (serverId: string, batchNo: number, pushed: boolean) => ({
    serverId,
    batchNo,
    status: 'activated' as const,
    pushedAt: pushed ? '2026-07-16T07:00:00Z' : null,
    activatedAt: pushed ? '2026-07-16T07:05:00Z' : null,
    changedFileCount: 3,
    skippedFileCount: 0,
    backupPresent: true,
    error: null,
    rollbackStatus: null,
    rollbackError: null,
    deliveredVersion: null,
  })

  it('含未推送台时：勾满可勾选目标即等价整单（提示含配置回退），与后端 eligible 口径一致', async () => {
    useScenario('normal')
    server.use(
      http.get('*/admin/v2/change-orders/:id/targets', () =>
        HttpResponse.json({
          // t-3 从未推送：计入 total，但不可勾选、也不计入可回滚数
          items: [target('t-1', 1, true), target('t-2', 1, true), target('t-3', 2, false)],
          total: 3,
          rollbackEligibleCount: 2,
        }),
      ),
    )
    renderPage(<TargetStatusWall orderId={9002} orderStatus="completed" />)

    const table = await screen.findByRole('table')
    fireEvent.click(within(table).getByRole('checkbox', { name: 't-1' }))
    fireEvent.click(within(table).getByRole('checkbox', { name: 't-2' }))
    // 未推送台不可勾选（disabled）
    expect(within(table).getByRole('checkbox', { name: 't-3' })).toBeDisabled()
    expect(screen.getByRole('button', { name: /整单回滚（已全选）/ })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /整单回滚（已全选）/ }))
    expect(await screen.findByText(/已全选全部可回滚目标/)).toBeInTheDocument()
  })

  it('按批筛选勾满该批：仍按子集执行（提示配置不回退），不得因本页勾满而误判整单', async () => {
    useScenario('normal')
    server.use(
      http.get('*/admin/v2/change-orders/:id/targets', ({ request }) => {
        const batch = new URL(request.url).searchParams.get('batch')
        const all = [target('t-1', 1, true), target('t-2', 1, true), target('t-3', 2, true)]
        const items = batch === null ? all : all.filter((row) => String(row.batchNo) === batch)
        // total 与真机同口径：**筛选后**的总数（受 batch 影响），不是全单总数——
        // 否则「勾满本页」与「勾满全单」在断言上不可区分，用例对基数错误没有判别力。
        return HttpResponse.json({ items, total: items.length, rollbackEligibleCount: all.length })
      }),
    )
    renderPage(
      <TargetStatusWall orderId={9003} orderStatus="completed" batches={[1, 2]} />,
    )

    const table = await screen.findByRole('table')
    // 先筛到第 1 批（2 台），再勾满本批
    fireEvent.change(screen.getByLabelText('批次'), { target: { value: '1' } })
    await waitFor(() => {
      expect(within(table).queryByText('t-3')).not.toBeInTheDocument()
    })
    fireEvent.click(within(table).getByRole('checkbox', { name: 't-1' }))
    fireEvent.click(within(table).getByRole('checkbox', { name: 't-2' }))
    expect(screen.getByRole('button', { name: /^回滚选中目标$/ })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: /^回滚选中目标$/ }))
    expect(await screen.findByText(/本次不回退配置版本/)).toBeInTheDocument()
  })
})
