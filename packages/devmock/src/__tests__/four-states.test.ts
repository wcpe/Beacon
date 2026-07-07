// FR-159 验收核心：各域代表性端点在四态场景下的响应形状。
// empty 空列表 / normal 有真实感数据 / huge total>1000 且分页真实生效 / error 统一错误体。
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { BASE_MS, MOCK_TRACE_ID, setMockScenario } from '../index'
import { callJson, resetMockWorld, server } from './msw-server'

interface PagedShape {
  items: unknown[]
  total: number
}

function asPaged(json: unknown): PagedShape {
  return json as PagedShape
}

const FROM = new Date(BASE_MS - 3_600_000).toISOString()
const TO = new Date(BASE_MS + 60_000).toISOString()

// 各域代表性列表端点（统一 {items,total} 形状）
const LIST_ENDPOINTS: { domain: string; path: string }[] = [
  { domain: 'identity', path: '/admin/v2/agent-identities' },
  { domain: 'namespace', path: '/admin/v2/namespaces' },
  { domain: 'zone-authority', path: '/admin/v2/servers' },
  { domain: 'metrics-health', path: '/admin/v2/health' },
  { domain: 'archive', path: '/admin/v2/archive/jobs' },
  { domain: 'config-center', path: '/admin/v2/config-files?namespaceId=1' },
  { domain: 'file-assets', path: '/admin/v2/assets?namespaceId=1' },
  { domain: 'delivery', path: '/admin/v2/change-orders' },
  { domain: 'observability(legacy)', path: '/admin/v1/audits' },
]

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  resetMockWorld()
})
afterAll(() => {
  server.close()
})

describe('empty 场景：空列表引导态', () => {
  it.each(LIST_ENDPOINTS)('$domain $path 返回空 items 与 total=0', async ({ path }) => {
    setMockScenario('empty')
    const { status, json } = await callJson('GET', path)
    expect(status).toBe(200)
    const paged = asPaged(json)
    expect(paged.items).toEqual([])
    expect(paged.total).toBe(0)
  })

  it('zone-tree 返回空结构树', async () => {
    setMockScenario('empty')
    const { status, json } = await callJson('GET', '/admin/v2/zone-tree?namespaceId=1')
    expect(status).toBe(200)
    const tree = json as { clusters: unknown[]; unassignedCount: number }
    expect(tree.clusters).toEqual([])
    expect(tree.unassignedCount).toBe(0)
  })
})

describe('normal 场景：真实感中文运维数据', () => {
  it.each(LIST_ENDPOINTS)('$domain $path 返回非空数据', async ({ path }) => {
    const { status, json } = await callJson('GET', path)
    expect(status).toBe(200)
    const paged = asPaged(json)
    expect(paged.total).toBeGreaterThan(0)
    expect(paged.items.length).toBeGreaterThan(0)
  })

  it('身份列表覆盖状态机枚举值且形状符合契约', async () => {
    const { json } = await callJson('GET', '/admin/v2/agent-identities?pageSize=100')
    const paged = asPaged(json) as { items: { identityId: string; serverId: string; status: string }[]; total: number }
    const statuses = new Set(paged.items.map((item) => item.status))
    for (const expected of ['pending', 'active', 'rejected', 'expired', 'disabled', 'conflict', 'unbound']) {
      expect(statuses).toContain(expected)
    }
    const first = paged.items[0]
    expect(typeof first.identityId).toBe('string')
    expect(typeof first.serverId).toBe('string')
  })

  it('keyword 筛选在服务端真实生效', async () => {
    const { json } = await callJson('GET', '/admin/v2/agent-identities?keyword=lobby&pageSize=100')
    const paged = asPaged(json) as { items: { serverId: string }[] }
    expect(paged.items.length).toBeGreaterThan(0)
    for (const item of paged.items) {
      expect(item.serverId).toContain('lobby')
    }
  })

  it('未分配篮：assigned=false 只返回未分配 server', async () => {
    const { json } = await callJson('GET', '/admin/v2/servers?assigned=false&namespaceId=1')
    const paged = asPaged(json) as { items: { assigned: boolean; zoneId: number | null }[] }
    expect(paged.items.length).toBeGreaterThan(0)
    for (const item of paged.items) {
      expect(item.assigned).toBe(false)
    }
  })

  it('消息查询防护：缺过滤或时间范围返回 400', async () => {
    const missing = await callJson('GET', '/admin/v2/messages')
    expect(missing.status).toBe(400)
    const ok = await callJson('GET', `/admin/v2/messages?serverId=lobby-1&from=${FROM}&to=${TO}`)
    expect(ok.status).toBe(200)
  })

  it('有效配置预览返回逐键来源（provenance）', async () => {
    const files = asPaged((await callJson('GET', '/admin/v2/config-files?namespaceId=1&keyword=Essentials')).json) as {
      items: { id: number; name: string }[]
    }
    const file = files.items.find((f) => f.name === 'plugins/Essentials/config.yml')
    expect(file).toBeDefined()
    const { json } = await callJson('GET', `/admin/v2/config-files/${String(file?.id ?? 0)}/effective?serverId=lobby-1`)
    const effective = json as {
      effectiveContent: string
      provenance: { path: string; scopeLevel: string }[]
      layers: unknown[]
    }
    expect(effective.effectiveContent).toContain('teleport-cooldown')
    const cooldown = effective.provenance.find((p) => p.path === 'teleport-cooldown')
    expect(cooldown?.scopeLevel).toBe('zone')
    expect(effective.layers.length).toBeGreaterThanOrEqual(2)
  })

  it('敏感配置读出口统一脱敏', async () => {
    const files = asPaged((await callJson('GET', '/admin/v2/config-files?namespaceId=1&keyword=Economy')).json) as {
      items: { id: number }[]
    }
    const { json } = await callJson('GET', `/admin/v2/config-files/${String(files.items[0].id)}/effective`)
    const effective = json as { effectiveContent: string }
    expect(effective.effectiveContent).toContain('__BEACON_MASKED__')
    expect(effective.effectiveContent).not.toContain('prod-secret-233')
  })

  it('敏感文件预览：无原因 403，携原因放行', async () => {
    const assets = asPaged(
      (await callJson('GET', '/admin/v2/assets?namespaceId=1&pathPrefix=plugins/Economy/database-password.yml')).json,
    ) as { items: { serverId: string; path: string }[] }
    expect(assets.items.length).toBeGreaterThan(0)
    const target = assets.items[0]
    const denied = await callJson('POST', '/admin/v2/assets/preview', { serverId: target.serverId, path: target.path })
    expect(denied.status).toBe(403)
    expect((denied.json as { sensitive?: boolean }).sensitive).toBe(true)
    const allowed = await callJson('POST', '/admin/v2/assets/preview', {
      serverId: target.serverId,
      path: target.path,
      reason: '排查线上口令配置漂移',
    })
    expect(allowed.status).toBe(200)
  })
})

describe('huge 场景：1000+ 规模且分页真实生效', () => {
  it('servers total 超过 1200，翻页返回不同数据', async () => {
    setMockScenario('huge')
    const page1 = asPaged((await callJson('GET', '/admin/v2/servers?page=1&pageSize=50')).json) as {
      items: { serverId: string }[]
      total: number
    }
    const page2 = asPaged((await callJson('GET', '/admin/v2/servers?page=2&pageSize=50')).json) as {
      items: { serverId: string }[]
      total: number
    }
    expect(page1.total).toBeGreaterThan(1200)
    expect(page1.items).toHaveLength(50)
    expect(page2.items).toHaveLength(50)
    expect(page1.items[0].serverId).not.toBe(page2.items[0].serverId)
    expect(page2.total).toBe(page1.total)
  })

  it('config-files 与 sched-decisions 均达到超大量级', async () => {
    setMockScenario('huge')
    const files = asPaged((await callJson('GET', '/admin/v2/config-files?namespaceId=1')).json)
    expect(files.total).toBeGreaterThan(1000)
    const decisions = asPaged((await callJson('GET', '/admin/v2/sched-decisions')).json)
    expect(decisions.total).toBeGreaterThan(1000)
  })

  it('末页分页切片正确', async () => {
    setMockScenario('huge')
    const probe = asPaged((await callJson('GET', '/admin/v2/servers?page=1&pageSize=100')).json)
    const lastPage = Math.ceil(probe.total / 100)
    const tail = asPaged((await callJson('GET', `/admin/v2/servers?page=${String(lastPage)}&pageSize=100`)).json)
    expect(tail.items.length).toBe(probe.total - (lastPage - 1) * 100)
  })
})

describe('error 场景：统一错误体', () => {
  it.each(LIST_ENDPOINTS)('$domain $path 返回 500 与 {code,message,traceId}', async ({ path }) => {
    setMockScenario('error')
    const { status, json } = await callJson('GET', path)
    expect(status).toBe(500)
    const body = json as { code: string; message: string; traceId: string }
    expect(body.code).toBe('internal_error')
    expect(body.message.length).toBeGreaterThan(0)
    expect(body.traceId).toBe(MOCK_TRACE_ID)
  })

  it('写端点在 error 场景同样返回统一错误体', async () => {
    setMockScenario('error')
    const { status, json } = await callJson('POST', '/admin/v2/namespaces', { name: 'x' })
    expect(status).toBe(500)
    expect((json as { code: string }).code).toBe('internal_error')
  })
})
