// 变更单申请动作契约回归（FR-252）：六类动作 202 + 票据，五类动作必须携带 Idempotency-Key。
// 真机事实：缺键 → 400 INVALID_PARAM（且 submit 的两步非事务会让单据卡死），
// 票据形为 {approvalRequestId, status, operationKey, orderId, impactSummary}——
// 不再声明为 ChangeOrderDetail（旧前端按 200 详情消费，点完静默关闭无反馈）。
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import {
  confirmChangeBatch,
  deleteChangeOrder,
  fetchChangeOrder,
  fetchChangeOrders,
  fetchChangeTargets,
  fetchRollbackRecords,
  finishRollbackChangeOrder,
  isApprovalTicket,
  resumeChangeOrder,
  rollbackChangeOrder,
  rollbackChangeTargets,
  submitChangeOrder,
} from '../../api/delivery-changes'
import { approveApproval } from '../../api/approvals'
import { createTestServer, useScenario } from './harness'

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

const TICKET = {
  approvalRequestId: 'apr_change_7001_1',
  status: 'pending',
  operationKey: 'delivery.approve',
  orderId: 7001,
  impactSummary: { targetCount: 3, batchCount: 2, payloadFiles: 4, payloadConfigs: 1 },
}

describe('变更单申请动作契约（202 票据 + 幂等键）', () => {
  it('submit / delete / resume / 批次确认 / 回滚 / 结束回滚 全部按 202 票据形消费', async () => {
    useScenario('normal')
    server.use(
      http.all('*/admin/v2/change-orders/:id/*', () => HttpResponse.json(TICKET, { status: 202 })),
      // 删除申请没有子路径（DELETE /change-orders/{id}），单独接管
      http.delete('*/admin/v2/change-orders/:id', () => HttpResponse.json(TICKET, { status: 202 })),
    )

    const tickets = [
      await submitChangeOrder(7001, '提审原因', 'key-submit'),
      await deleteChangeOrder(7001, '删除原因', 'key-delete'),
      await resumeChangeOrder(7001, { mode: 'retry_failed', reason: '继续原因' }, 'key-resume'),
      await confirmChangeBatch(7001, 2, 'key-confirm'),
      await rollbackChangeOrder(7001, '回滚原因', 'key-rollback'),
      await finishRollbackChangeOrder(7001, 'key-finish'),
    ]

    for (const ticket of tickets) {
      // 票据四键必须齐备：调用方据此展示「已提交审批（申请号 X）」并跳转审批中心
      expect(ticket.approvalRequestId).toBe('apr_change_7001_1')
      expect(ticket.operationKey).toBe('delivery.approve')
      expect(ticket.orderId).toBe(7001)
      expect(ticket.impactSummary).toEqual({ targetCount: 3, batchCount: 2, payloadFiles: 4, payloadConfigs: 1 })
      // 票据不是变更单详情（不再声明 ChangeOrderDetail）
      expect(isApprovalTicket(ticket)).toBe(true)
    }
  })

  it('五个非提审动作各自发出合法 Idempotency-Key（缺键真机 400 且冻结单据）', async () => {
    useScenario('normal')
    const captured: Record<string, string | null> = {}
    const capture = ({ request }: { request: Request }): HttpResponse<typeof TICKET> => {
      const url = new URL(request.url)
      const suffix = url.pathname.replace('/admin/v2/change-orders/7001', '')
      captured[suffix] = request.headers.get('Idempotency-Key')
      return HttpResponse.json(TICKET, { status: 202 })
    }
    server.use(
      http.all('*/admin/v2/change-orders/:id/*', capture),
      http.delete('*/admin/v2/change-orders/:id', capture),
    )

    await deleteChangeOrder(7001, '删除原因', 'key-delete')
    await resumeChangeOrder(7001, { reason: '继续原因' }, 'key-resume')
    await confirmChangeBatch(7001, 2, 'key-confirm')
    await rollbackChangeOrder(7001, '回滚原因', 'key-rollback')
    await finishRollbackChangeOrder(7001, 'key-finish')

    expect(captured['']).toBe('key-delete')
    expect(captured['/resume']).toBe('key-resume')
    expect(captured['/batches/2/confirm']).toBe('key-confirm')
    expect(captured['/rollback']).toBe('key-rollback')
    expect(captured['/rollback/finish']).toBe('key-finish')
  })

  it('票据缺失 approvalRequestId 时不被误判为详情（类型守卫按键判定）', () => {
    expect(isApprovalTicket({ id: 1, title: '变更单详情' } as never)).toBe(false)
    expect(isApprovalTicket(TICKET)).toBe(true)
  })
})

// —— 返工：目标级子集回滚必须经**真实 devmock**（不 server.use 打桩）走完申请与批准 ——
// 批 1 的教训：用 server.use 覆盖端点会让「演示模式实际按整单执行」这类偏差被完全掩盖。
describe('目标级子集回滚的演示模式闭环（不经打桩）', () => {
  /** 找一个已完成且有 ≥2 台可回滚（曾推送）目标的种子单 */
  async function completedOrderWithRollbackTargets(): Promise<{ orderId: number; rows: { serverId: string }[] }> {
    const list = await fetchChangeOrders({ status: 'completed', pageSize: 50 })
    for (const summary of list.items) {
      const page = await fetchChangeTargets(summary.id, { page: 1, pageSize: 50 })
      const eligible = page.items.filter((row) => row.pushedAt !== null)
      if (eligible.length >= 2) {
        return { orderId: summary.id, rows: eligible.map((row) => ({ serverId: row.serverId })) }
      }
    }
    throw new Error('种子缺少「已完成且 ≥2 台可回滚目标」的变更单')
  }

  it('批准后只有选中目标被回滚，未选中目标与单主状态都不动', async () => {
    useScenario('normal')
    const { orderId, rows } = await completedOrderWithRollbackTargets()
    const picked = rows[0].serverId
    const untouched = rows.slice(1).map((row) => row.serverId)

    const ticket = await rollbackChangeTargets(orderId, [picked], '只回滚一台', 'key-subset-real')
    expect(ticket.approvalRequestId).toBeTruthy()
    // 申请阶段零副作用：单主状态不变、目标回滚态未写
    expect((await fetchChangeOrder(orderId)).status).toBe('completed')
    const beforeApproval = await fetchChangeTargets(orderId, { page: 1, pageSize: 50 })
    expect(beforeApproval.items.every((row) => row.rollbackStatus === null)).toBe(true)

    await approveApproval(ticket.approvalRequestId)

    const after = await fetchChangeTargets(orderId, { page: 1, pageSize: 50 })
    const byId = new Map(after.items.map((row) => [row.serverId, row]))
    expect(byId.get(picked)?.rollbackStatus).toBe('rolled_back')
    for (const serverId of untouched) {
      expect(byId.get(serverId)?.rollbackStatus).toBeNull()
    }
    // 子集回滚不改单主状态，且被回滚台的交付版本被清空（演示模式可复现「回滚后回退显示」）
    expect((await fetchChangeOrder(orderId)).status).toBe('completed')
    expect(byId.get(picked)?.deliveredVersion).toBeNull()
  })

  it('全选（覆盖全部可回滚目标）按整单回滚执行：全部目标回滚、单收口并落整单记录', async () => {
    useScenario('normal')
    const { orderId, rows } = await completedOrderWithRollbackTargets()
    const ticket = await rollbackChangeTargets(orderId, rows.map((row) => row.serverId), '全选回滚', 'key-subset-all')
    await approveApproval(ticket.approvalRequestId)

    // 整单路径：单状态迁移并收口（演示模式无后台推进，整单一步到位），目标全量回滚
    expect((await fetchChangeOrder(orderId)).status).toBe('rolled_back')
    const after = await fetchChangeTargets(orderId, { page: 1, pageSize: 50 })
    expect(after.items.every((row) => row.rollbackStatus === 'rolled_back')).toBe(true)
    // 记录里落的是**整单**动作且标注已回退配置——与子集动作可区分
    const records = await fetchRollbackRecords(orderId)
    expect(records.items[0].kind).toBe('order')
    expect(records.items[0].configRolledBack).toBe(true)
  })
})
