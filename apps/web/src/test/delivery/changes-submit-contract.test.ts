// 提审请求契约回归：`POST /admin/v2/change-orders/{id}/submit` 必须同时满足
// ① 请求体携带 reason —— 后端 RequestSubmit 强制原因非空（缺则 400 approval_reason_required）；
// ② 请求头携带 Idempotency-Key —— 后端建审批申请时校验幂等键（缺则 400 INVALID_PARAM），
//    且 submit 是「先冻结状态、后建申请」两步非事务，缺键会把单据永久卡在 pending_approval。
// 此前前端两样都不带，真机上提审必然失败。
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

describe('变更单提审请求契约', () => {
  it('submitChangeOrder 同时发出 reason 请求体与 Idempotency-Key 头', async () => {
    useScenario('normal')
    // 以原始文本捕获 body：无请求体时记 null（而非在 handler 里解析空 body 抛异常），失败差异更直白
    let capturedBody: unknown = null
    let capturedKey: string | null = null
    server.use(
      http.post('*/admin/v2/change-orders/:id/submit', async ({ request }) => {
        const raw = await request.text()
        capturedBody = raw === '' ? null : (JSON.parse(raw) as unknown)
        capturedKey = request.headers.get('Idempotency-Key')
        // 直接回成功体，用例只关心请求侧契约
        return HttpResponse.json({ id: 5001 }, { status: 200 })
      }),
    )

    await submitChangeOrder(5001, '插件已在模板源验证通过', 'test-idem-key-0001')

    expect(capturedBody).toEqual({ reason: '插件已在模板源验证通过' })
    expect(capturedKey).toBe('test-idem-key-0001')
  })

  it('原因缺失时后端的 400 原样抛给调用方（不被静默吞掉）', async () => {
    useScenario('normal')

    // devmock 的提审端点已对齐后端守卫：无原因 → 400 approval_reason_required
    await expect(submitChangeOrder(5001, '   ', 'test-idem-key-0001')).rejects.toMatchObject({
      status: 400,
      code: 'approval_reason_required',
      message: '审批原因不能为空',
    })
  })

  it('幂等键缺失或非法时后端的 400 原样抛给调用方（不被静默吞掉）', async () => {
    useScenario('normal')

    // devmock 的提审端点已对齐后端 validIdempotencyKey：缺键 / 超长键 → 400 INVALID_PARAM
    await expect(submitChangeOrder(5001, '插件已验证', '')).rejects.toMatchObject({
      status: 400,
      code: 'INVALID_PARAM',
    })
    await expect(submitChangeOrder(5001, '插件已验证', 'k'.repeat(65))).rejects.toMatchObject({
      status: 400,
      code: 'INVALID_PARAM',
    })
  })
})
