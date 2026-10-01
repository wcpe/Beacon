// MCP 工具调用流水 mock（FR-240 §3.7）：六维过滤 / 游标分页 / 详情 404 / 被拒原因区分，
// 以及空 / 常规 / 超大量 / 异常四态与非法参数边界。断言口径与真端点冻结语义一致。
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import type { MCPInvocationItem, MCPInvocationListResponse } from '@beacon/contracts'
import { BASE_MS, MOCK_TRACE_ID, setMockScenario } from '../index'
import { callJson, resetMockWorld, server } from './msw-server'

const LIST_PATH = '/admin/v2/mcp/invocations'
/** 常规场景的预置行数（与 mock 配方表一致） */
const NORMAL_ROWS = 18
/** 超大量场景的预置行数（与 INVOCATION_HUGE_COUNT 一致） */
const HUGE_ROWS = 620
/** 详情项字段全集（§3.2 一一对应，出现多余键即契约漂移） */
const ITEM_FIELDS = [
  'argBytes',
  'argKeys',
  'clientId',
  'clientIp',
  'createdAt',
  'durationMs',
  'errorSummary',
  'invocationId',
  'profile',
  'reason',
  'result',
  'riskLevel',
  'targetDigest',
  'toolName',
  'traceId',
].sort()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  resetMockWorld()
})
afterAll(() => {
  server.close()
})

/** 请求列表端点并要求 200（非法参数与异常场景由各自用例单独断言状态码） */
async function list(query = ''): Promise<MCPInvocationListResponse> {
  const { status, json } = await callJson('GET', `${LIST_PATH}${query}`)
  expect(status).toBe(200)
  return json as MCPInvocationListResponse
}

/** 从首页反复续翻到末页，返回拼接后的全部行（用于断言不漏行、不重复） */
async function listAll(query = ''): Promise<MCPInvocationItem[]> {
  const items: MCPInvocationItem[] = []
  let cursor = ''
  for (let page = 0; page < 50; page += 1) {
    const separator = query === '' ? '?' : '&'
    const next = await list(`${query}${separator}cursor=${encodeURIComponent(cursor)}`)
    items.push(...next.items)
    if (next.nextCursor === '') {
      return items
    }
    cursor = next.nextCursor
  }
  throw new Error('游标续翻未在 50 页内到达末页')
}

function ids(rows: readonly MCPInvocationItem[]): string[] {
  return rows.map((row) => row.invocationId)
}

describe('流水列表：游标分页与边界', () => {
  it('默认页返回全部常规行且 nextCursor 为空串（末页）', async () => {
    const page = await list()
    expect(page.items).toHaveLength(NORMAL_ROWS)
    expect(page.nextCursor).toBe('')
  })

  it('limit 越小越可续：续翻结果无重复无遗漏，总数与预置一致', async () => {
    const all = await listAll('?limit=5')
    expect(all).toHaveLength(NORMAL_ROWS)
    expect(new Set(ids(all)).size).toBe(NORMAL_ROWS)
    // 跨页顺序保持 createdAt 倒序（keyset 语义的最直观可观测形式）
    const times = all.map((row) => Date.parse(row.createdAt))
    expect([...times].sort((a, b) => b - a)).toEqual(times)
  })

  it('limit 服务端规整：≤0 取默认 20，>100 取 100', async () => {
    const zero = await list('?limit=0')
    expect(zero.items).toHaveLength(NORMAL_ROWS)

    setMockScenario('huge')
    const hugeDefault = await list('?limit=0')
    expect(hugeDefault.items).toHaveLength(20)
    const hugeMax = await list('?limit=1000')
    expect(hugeMax.items).toHaveLength(100)
    expect(hugeMax.nextCursor).toBe('100')
  })

  it('游标越界返回空页与空串游标（末页边界）', async () => {
    const page = await list('?cursor=9999')
    expect(page.items).toEqual([])
    expect(page.nextCursor).toBe('')
  })
})

describe('流水列表：六维过滤', () => {
  it('按 tool 精确匹配（含未登记工具名）', async () => {
    const ok = await list(`?tool=${encodeURIComponent('beacon.files.create')}`)
    expect(ok.items.length).toBeGreaterThan(0)
    for (const row of ok.items) {
      expect(row.toolName).toBe('beacon.files.create')
    }

    const unknown = await list(`?tool=${encodeURIComponent('beacon.ops.undeclared_tool')}`)
    expect(unknown.items).toHaveLength(1)
    expect(unknown.items[0].riskLevel).toBe('unknown')
    expect(unknown.items[0].reason).toBe('unknown_tool')
  })

  it('按 clientId 精确匹配', async () => {
    const clientId = 'mcp_7f3a91c4e8b25d60a1f9'
    const rows = await list(`?clientId=${clientId}`)
    expect(rows.items.length).toBeGreaterThan(0)
    for (const row of rows.items) {
      expect(row.clientId).toBe(clientId)
      expect(row.profile).toBe('observer')
    }
  })

  it('按 result 过滤：三种取值互斥且各自自洽', async () => {
    for (const result of ['ok', 'fail', 'rejected'] as const) {
      const rows = await list(`?result=${result}`)
      expect(rows.items.length).toBeGreaterThan(0)
      for (const row of rows.items) {
        expect(row.result).toBe(result)
      }
    }
    const rejected = await list('?result=rejected')
    const ok = await list('?result=ok')
    expect(rejected.items).toHaveLength(6)
    expect(ok.items).toHaveLength(8)
    expect(ids(rejected.items).some((id) => ids(ok.items).includes(id))).toBe(false)
    for (const row of ok.items) {
      expect(row.reason).toBe('')
      expect(row.errorSummary).toBe('')
    }
  })

  it('按 riskLevel 过滤：四档齐备，unknown 只对应未登记工具', async () => {
    for (const riskLevel of ['low', 'high', 'critical', 'unknown'] as const) {
      const rows = await list(`?riskLevel=${riskLevel}`)
      expect(rows.items.length).toBeGreaterThan(0)
      for (const row of rows.items) {
        expect(row.riskLevel).toBe(riskLevel)
      }
    }
    const unknown = await list('?riskLevel=unknown')
    for (const row of unknown.items) {
      expect(row.reason).toBe('unknown_tool')
    }
  })

  it('被拒原因区分：production_mode 全为 critical 且带拒执文案前缀', async () => {
    const production = await list('?result=rejected&reason=production_mode')
    expect(production.items).toHaveLength(2)
    for (const row of production.items) {
      expect(row.riskLevel).toBe('critical')
      expect(row.errorSummary).toContain('生产模式已禁用 critical 风险等级工具：')
    }

    const unknownTool = await list('?result=rejected&reason=unknown_tool')
    expect(unknownTool.items).toHaveLength(2)
    for (const row of unknownTool.items) {
      expect(row.riskLevel).toBe('unknown')
    }

    const business = await list('?result=rejected&reason=handler_rejected')
    expect(business.items).toHaveLength(2)
    for (const row of business.items) {
      expect(row.errorSummary).not.toContain('生产模式已禁用')
    }
  })

  it('按时间窗过滤：闭区间，与全量结果按窗口切分精确一致', async () => {
    const all = await listAll()
    const fromMs = BASE_MS - 24 * 3_600_000
    const toMs = BASE_MS
    const from = new Date(fromMs).toISOString()
    const to = new Date(toMs).toISOString()

    const windowed = await list(`?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}&limit=100`)
    const expected = all.filter((row) => {
      const createdMs = Date.parse(row.createdAt)
      return createdMs >= fromMs && createdMs <= toMs
    })
    expect(windowed.items.length).toBeGreaterThan(0)
    expect(windowed.items.length).toBeLessThan(all.length)
    expect(new Set(ids(windowed.items))).toEqual(new Set(ids(expected)))

    // 收窄到「昨日窗口」只剩更早的行，今日行不得出现
    const yesterdayFrom = new Date(BASE_MS - 31 * 3_600_000).toISOString()
    const yesterdayTo = new Date(BASE_MS - 25 * 3_600_000).toISOString()
    const yesterday = await list(
      `?from=${encodeURIComponent(yesterdayFrom)}&to=${encodeURIComponent(yesterdayTo)}&limit=100`,
    )
    expect(yesterday.items.length).toBeGreaterThan(0)
    for (const row of yesterday.items) {
      const createdMs = Date.parse(row.createdAt)
      expect(createdMs).toBeGreaterThanOrEqual(Date.parse(yesterdayFrom))
      expect(createdMs).toBeLessThanOrEqual(Date.parse(yesterdayTo))
    }
  })

  it('多维组合过滤：结果集是单维结果的交集', async () => {
    const clientId = 'mcp_c18d4b70a6e93f25c0b1'
    const combined = await list(`?clientId=${clientId}&result=rejected&limit=100`)
    for (const row of combined.items) {
      expect(row.clientId).toBe(clientId)
      expect(row.result).toBe('rejected')
    }
    expect(combined.items.length).toBeGreaterThan(0)
  })
})

describe('流水列表：非法参数', () => {
  it.each(['?result=success', '?riskLevel=medium', '?reason=whatever'])(
    '%s 返回 400 INVALID_PARAM',
    async (query) => {
      const { status, json } = await callJson('GET', `${LIST_PATH}${query}`)
      expect(status).toBe(400)
      expect((json as { code: string }).code).toBe('INVALID_PARAM')
    },
  )

  it('无法解析的 from / to 视为不限（与真端点 parseISOms 同口径，不报错）', async () => {
    const all = await list('?limit=100')
    const garbage = await list('?from=not-a-time&to=2026-13-45T99:00:00Z&limit=100')
    expect(new Set(ids(garbage.items))).toEqual(new Set(ids(all.items)))
  })

  it('from 晚于 to 返回 400（时间窗倒置）', async () => {
    const from = new Date(BASE_MS).toISOString()
    const to = new Date(BASE_MS - 3_600_000).toISOString()
    const { status, json } = await callJson(
      'GET',
      `${LIST_PATH}?from=${encodeURIComponent(from)}&to=${encodeURIComponent(to)}`,
    )
    expect(status).toBe(400)
    expect((json as { code: string }).code).toBe('INVALID_PARAM')
  })
})

describe('流水详情：命中与 404', () => {
  it('按 invocationId 命中返回与列表项同形的单行', async () => {
    const page = await list('?limit=1')
    const expected = page.items[0]
    const { status, json } = await callJson('GET', `${LIST_PATH}/${expected.invocationId}`)
    expect(status).toBe(200)
    expect(json).toEqual(expected)
  })

  it('详情项字段与契约完全一致（不含任何参数 / 结果正文）', async () => {
    const page = await list('?result=rejected&limit=1')
    const { json } = await callJson('GET', `${LIST_PATH}/${page.items[0].invocationId}`)
    expect(Object.keys(json as Record<string, unknown>).sort()).toEqual(ITEM_FIELDS)
    // argKeys 只出现「键名」或「键名:字节数」，值本身（如正文片段）不出现
    expect(page.items[0].argKeys).not.toContain('=')
  })

  it('乱造 ID 与非法格式 ID 一律 404 mcp_invocation_not_found（不区分）', async () => {
    const fabricated = await callJson('GET', `${LIST_PATH}/0197f0c2-1a34-7b21-8c47-5d9e0a1b2c3d`)
    expect(fabricated.status).toBe(404)
    expect((fabricated.json as { code: string }).code).toBe('mcp_invocation_not_found')

    const malformed = await callJson('GET', `${LIST_PATH}/not-a-uuid`)
    expect(malformed.status).toBe(404)
    expect((malformed.json as { code: string }).code).toBe('mcp_invocation_not_found')
  })
})

describe('四态场景', () => {
  it('empty 场景：空列表与空串游标', async () => {
    setMockScenario('empty')
    const page = await list()
    expect(page.items).toEqual([])
    expect(page.nextCursor).toBe('')
  })

  it('huge 场景：620 行、多页续翻无重复无遗漏', async () => {
    setMockScenario('huge')
    const first = await list('?limit=100')
    expect(first.items).toHaveLength(100)
    expect(first.nextCursor).toBe('100')

    const all = await listAll('?limit=100')
    expect(all).toHaveLength(HUGE_ROWS)
    expect(new Set(ids(all)).size).toBe(HUGE_ROWS)

    const filtered = await list(`?clientId=mcp_4e8b2a17c93d05f6ab73&limit=100`)
    expect(filtered.items.length).toBeGreaterThan(0)
    for (const row of filtered.items) {
      expect(row.clientId).toBe('mcp_4e8b2a17c93d05f6ab73')
    }
  })

  it('error 场景：列表与详情均返回统一错误体', async () => {
    setMockScenario('error')
    const listError = await callJson('GET', LIST_PATH)
    expect(listError.status).toBe(500)
    expect((listError.json as { code: string }).code).toBe('internal_error')
    expect((listError.json as { traceId: string }).traceId).toBe(MOCK_TRACE_ID)

    const detailError = await callJson('GET', `${LIST_PATH}/0197f0c2-1a34-7b21-8c47-5d9e0a1b2c3d`)
    expect(detailError.status).toBe(500)
    expect((detailError.json as { code: string }).code).toBe('internal_error')
  })
})
