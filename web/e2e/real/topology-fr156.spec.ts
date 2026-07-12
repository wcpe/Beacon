// FR-156（/topology 接真收口）真后端 E2E：
// /topology 页打真端点渲染（可视化 / 数据剖析两模式可达、真数据或优雅空态、全页无「加载失败」）；
// 并以 page.request 携带 Bearer 令牌直打 /admin/v2/messages/stats 与 /admin/v2/connections/stats
// 交叉校验契约形状（camelCase 对齐 contracts），以及 payload 受控查看端点的原因必填 / 未命中语义。
//
// 注意：真后端为 sqlite 单实例、用例间共享库（含其他 spec 的 seed 数据），故页面断言用
// 「空态文案 或 真数据元素」二选一的稳健形式，不假定库一定为空。

import { test, expect, type Page } from '@playwright/test'
import { loginRealAdmin } from '../shared/auth'
import { gotoPageViaNav } from './pages'

function authHeader(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

// GET 返回 JSON（断言 2xx）
async function apiGetJson(page: Page, token: string, path: string): Promise<unknown> {
  const res = await page.request.get(path, { headers: authHeader(token) })
  expect(res.ok(), `GET ${path} → HTTP ${String(res.status())}`).toBeTruthy()
  return (await res.json()) as unknown
}

// 从 unknown 里取对象字段（避免 any，逐层收窄）
function field(obj: unknown, key: string): unknown {
  expect(typeof obj === 'object' && obj !== null, `响应应为对象（取 ${key}）`).toBeTruthy()
  return (obj as Record<string, unknown>)[key]
}

test('拓扑页：可视化 / 数据剖析两模式打真端点渲染，真数据或空态优雅、全页无加载失败', async ({
  page,
}) => {
  await loginRealAdmin(page)
  await gotoPageViaNav(page, 'topology')

  // 两个模式 Tab 齐备
  await expect(page.getByRole('tab', { name: '可视化' })).toBeVisible()
  await expect(page.getByRole('tab', { name: '数据剖析' })).toBeVisible()

  // 可视化模式：空态引导 或 放射图卡标题（库里有无拓扑数据均可）
  await expect(
    page
      .getByText('暂无拓扑数据，接入服务器并完成区服分配后展示链路')
      .or(page.getByText('BC-子服链路'))
      .first(),
  ).toBeVisible()

  // 数据剖析模式：链路表空态 或 表头（消息边聚合打真 /admin/v2/messages/stats）
  await page.getByRole('tab', { name: '数据剖析' }).click()
  await expect(page.getByText('消息异常链路')).toBeVisible()
  await expect(
    page.getByText('当前无跨服消息链路').or(page.getByText('源服务器')).first(),
  ).toBeVisible()

  // 全页不得出现「加载失败」（各区块均为真数据 / 空态，无错误分支）
  await expect(page.getByText(/加载失败/)).toHaveCount(0)
})

test('契约交叉校验：messages/stats 与 connections/stats 响应形状对齐 contracts', async ({
  page,
}) => {
  const token = await loginRealAdmin(page)

  // /admin/v2/messages/stats?groupBy=edge → {edges: MessageEdgeStat[]}（有行时抽查首行字段）
  const edgeStats = await apiGetJson(page, token, '/admin/v2/messages/stats?groupBy=edge')
  const edges = field(edgeStats, 'edges')
  expect(Array.isArray(edges)).toBeTruthy()
  const firstEdge = (edges as unknown[])[0]
  if (firstEdge !== undefined) {
    expect(typeof field(firstEdge, 'sourceServerId')).toBe('string')
    expect(typeof field(firstEdge, 'resolvedServerId')).toBe('string')
    expect(typeof field(firstEdge, 'total')).toBe('number')
    expect(typeof field(firstEdge, 'failed')).toBe('number')
    expect(typeof field(firstEdge, 'expired')).toBe('number')
    expect(typeof field(firstEdge, 'failRatePercent')).toBe('number')
    expect(typeof field(firstEdge, 'p95DurationMs')).toBe('number')
    expect(Array.isArray(field(firstEdge, 'topFailReasons'))).toBeTruthy()
    expect(Array.isArray(field(firstEdge, 'sampleMessageIds'))).toBeTruthy()
  }

  // /admin/v2/messages/stats?groupBy=type → {types: [{msgType,total,failed}]}
  const typeStats = await apiGetJson(page, token, '/admin/v2/messages/stats?groupBy=type')
  expect(Array.isArray(field(typeStats, 'types'))).toBeTruthy()

  // /admin/v2/connections/stats → {buckets: ConnStatsBucket[]}（默认最近 1h 零填充时间桶）
  const connStats = await apiGetJson(page, token, '/admin/v2/connections/stats?bucket=5m')
  const buckets = field(connStats, 'buckets')
  expect(Array.isArray(buckets)).toBeTruthy()
  const firstBucket = (buckets as unknown[])[0]
  if (firstBucket !== undefined) {
    expect(typeof field(firstBucket, 'startAt')).toBe('string')
    expect(typeof field(firstBucket, 'opens')).toBe('number')
    expect(typeof field(firstBucket, 'closes')).toBe('number')
    expect(typeof field(firstBucket, 'abnormalCloses')).toBe('number')
    expect(typeof field(firstBucket, 'estimatedOpen')).toBe('number')
  }

  // payload 受控查看：缺原因 → 400 missing_reason（原因校验先于消息存在性）
  const noReason = await page.request.post('/admin/v2/messages/ghost-fr156/payload', {
    headers: authHeader(token),
    data: {},
  })
  expect(noReason.status()).toBe(400)
  expect(field((await noReason.json()) as unknown, 'code')).toBe('missing_reason')

  // payload 受控查看：带原因但消息不存在 → 404 message_not_found
  const notFound = await page.request.post('/admin/v2/messages/ghost-fr156/payload', {
    headers: authHeader(token),
    data: { reason: 'FR-156 真后端契约校验' },
  })
  expect(notFound.status()).toBe(404)
  expect(field((await notFound.json()) as unknown, 'code')).toBe('message_not_found')
})

test('拓扑页空态或真数据：消息边聚合与页面剖析指标一致渲染', async ({ page }) => {
  const token = await loginRealAdmin(page)
  await gotoPageViaNav(page, 'topology')
  await page.getByRole('tab', { name: '数据剖析' }).click()

  // 数据剖析聚合指标条常驻（链路数 / 消息总量 / 异常边）
  await expect(page.getByText('链路数')).toBeVisible()
  await expect(page.getByText('消息总量')).toBeVisible()

  // 与真端点二选一交叉验证：边聚合为空 → 表空态；非空 → 表出现首行源服务器
  const edgeStats = await apiGetJson(page, token, '/admin/v2/messages/stats?groupBy=edge')
  const edges = field(edgeStats, 'edges') as unknown[]
  if (edges.length === 0) {
    await expect(page.getByText('当前无跨服消息链路')).toBeVisible()
  } else {
    const source = field(edges[0], 'sourceServerId') as string
    await expect(page.getByText(source).first()).toBeVisible()
  }
})
