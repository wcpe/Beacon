// 提审请求体回归：`POST /change-orders/{id}/submit` 必须携带 reason —— 后端 RequestSubmit
// 强制原因非空（缺则 400 approval_reason_required），此前前端不带 body，真机必然提审失败。
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import { submitChangeOrder } from '../../api/delivery-changes'
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

describe('变更单提审请求体', () => {
  it('submitChangeOrder 以 { reason } 作为请求体发出', async () => {
    useScenario('normal')
    // 以原始文本捕获：无请求体时记 null（而非在 handler 里解析空 body 抛异常），失败差异更直白
    let captured: unknown = null
    server.use(
      http.post('*/admin/v2/change-orders/:id/submit', async ({ request }) => {
        const raw = await request.text()
        captured = raw === '' ? null : (JSON.parse(raw) as unknown)
        // 直接回成功体，用例只关心请求侧契约
        return HttpResponse.json({ id: 5001 }, { status: 200 })
      }),
    )

    await submitChangeOrder(5001, '插件已在模板源验证通过')

    expect(captured).toEqual({ reason: '插件已在模板源验证通过' })
  })

  it('原因缺失时后端的 400 原样抛给调用方（不被静默吞掉）', async () => {
    useScenario('normal')

    // devmock 的提审端点已对齐后端守卫：无原因 → 400 approval_reason_required
    await expect(submitChangeOrder(5001, '   ')).rejects.toMatchObject({
      status: 400,
      code: 'approval_reason_required',
      message: '审批原因不能为空',
    })
  })
})
