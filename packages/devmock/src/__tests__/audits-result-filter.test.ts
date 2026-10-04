// 审计列表 result 过滤（对齐真后端 audit_handler 口径）：ok / fail 精确匹配，
// 缺省与 'all' 同义（不过滤，承接前端 'all' → undefined 的兜底）。
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { callJson, resetMockWorld, server } from './msw-server'

interface AuditItem {
  id: number
  result: string
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  resetMockWorld()
})
afterAll(() => {
  server.close()
})

/** 取一页审计（size 放大到覆盖全量，专注过滤口径） */
async function fetchAudits(query: string): Promise<AuditItem[]> {
  const { json } = await callJson('GET', `/admin/v1/audits?size=200${query}`)
  return (json as { items: AuditItem[] }).items
}

describe('审计列表 result 过滤', () => {
  it('result=ok / result=fail 各自只返回同 result 行，且两集合恰好划分全量', async () => {
    const all = await fetchAudits('')
    const ok = await fetchAudits('&result=ok')
    const fail = await fetchAudits('&result=fail')

    expect(all.length).toBeGreaterThan(0)
    expect(ok.length).toBeGreaterThan(0)
    expect(fail.length).toBeGreaterThan(0)
    expect(ok.every((row) => row.result === 'ok')).toBe(true)
    expect(fail.every((row) => row.result === 'fail')).toBe(true)
    expect(ok.length + fail.length).toBe(all.length)
  })

  it("result='all' 与缺省同口径：全量不过滤", async () => {
    const all = await fetchAudits('')
    const allValue = await fetchAudits('&result=all')
    expect(allValue.length).toBe(all.length)
  })

  it('result 与其它过滤条件叠加：namespace + result 同时生效', async () => {
    const ok = await fetchAudits('&result=ok')
    const sample = ok[0]
    expect(sample).toBeDefined()
    const scoped = await fetchAudits(`&namespace=prod&result=ok`)
    expect(scoped.length).toBeGreaterThan(0)
    expect(scoped.length).toBeLessThanOrEqual(ok.length)
    expect(scoped.every((row) => row.result === 'ok')).toBe(true)
  })
})
