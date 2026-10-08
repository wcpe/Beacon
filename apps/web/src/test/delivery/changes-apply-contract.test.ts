// 变更单申请动作契约回归（FR-252）：六类动作 202 + 票据，五类动作必须携带 Idempotency-Key。
// 真机事实：缺键 → 400 INVALID_PARAM（且 submit 的两步非事务会让单据卡死），
// 票据形为 {approvalRequestId, status, operationKey, orderId, impactSummary}——
// 不再声明为 ChangeOrderDetail（旧前端按 200 详情消费，点完静默关闭无反馈）。
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import {
  confirmChangeBatch,
  deleteChangeOrder,
  finishRollbackChangeOrder,
  isApprovalTicket,
  resumeChangeOrder,
  rollbackChangeOrder,
  submitChangeOrder,
} from '../../api/delivery-changes'
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
