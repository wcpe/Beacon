// FR-154（本期部分：健康与调度概览、服务分析接真）真后端 E2E：
// /dashboard 五卡打真端点渲染（各卡真数据或优雅空态、全页无「加载失败」）、
// FlowOverview 对后端尚未提供的连接流端点显中性占位不报错；/service-analysis 可达 + 空态；
// 并以 page.request 携带 Bearer 令牌直打 /admin/v2 端点交叉校验契约形状（camelCase 对齐 contracts）。
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

test('运维总览：健康 / 状态墙 / 调度概览打真端点渲染，真数据或空态优雅、全页无加载失败', async ({
  page,
}) => {
  await loginRealAdmin(page)
  await expect(page.getByRole('heading', { name: '运维总览', exact: true })).toBeVisible()

  // 五卡区块标题齐备（卡骨架渲染，未白屏）
  await expect(page.getByText('集群健康总览')).toBeVisible()
  await expect(page.getByText('服务器状态墙')).toBeVisible()
  await expect(page.getByText('玩家流 / 连接流')).toBeVisible()
  await expect(page.getByText('告警概览')).toBeVisible()
  await expect(page.getByText('调度概览')).toBeVisible()

  // FlowOverview：连接流端点真后端尚未提供 → 中性占位而非误导性错误
  //（请求层默认重试 3 次后才落错分支，放宽等待时间）
  await expect(page.getByText('连接流数据暂未开放（随后续版本提供）')).toBeVisible({
    timeout: 20_000,
  })

  // 健康总览：空态引导 或 KPI 卡行（库里有无服务器均可）
  await expect(
    page
      .getByText('暂无健康数据，接入服务器后展示集群健康总览')
      .or(page.getByText('可调度服务器'))
      .first(),
  ).toBeVisible()

  // 状态墙：空态 或 表头（健康分列）
  await expect(
    page.getByText('暂无服务器，接入后展示状态墙').or(page.getByText('健康分')).first(),
  ).toBeVisible()

  // 调度概览：空态 或 成功率大数字
  await expect(
    page.getByText('当前时间窗内无调度决策').or(page.getByText('成功率')).first(),
  ).toBeVisible()

  // 告警概览：真端点 /admin/v1/alert-events 已存在（零改消费），下钻链接常驻
  await expect(page.getByRole('link', { name: '查看告警事件' })).toBeVisible()

  // 全页不得出现「加载失败」（各卡均为真数据 / 空态 / 占位，无错误分支）
  await expect(page.getByText(/加载失败/)).toHaveCount(0)
})

test('契约交叉校验：metrics/health/sched 管理端点响应形状对齐 contracts', async ({ page }) => {
  const token = await loginRealAdmin(page)

  // /admin/v2/metrics/summary → MetricsSummary
  const summary = await apiGetJson(page, token, '/admin/v2/metrics/summary')
  expect(typeof field(summary, 'generatedAt')).toBe('string')
  const byKind = field(summary, 'byKind')
  expect(typeof field(field(byKind, 'proxy'), 'total')).toBe('number')
  expect(typeof field(field(byKind, 'proxy'), 'online')).toBe('number')
  expect(typeof field(field(byKind, 'backend'), 'total')).toBe('number')
  expect(typeof field(summary, 'playersOnline')).toBe('number')
  expect(typeof field(summary, 'avgTps')).toBe('number')
  expect(typeof field(summary, 'avgCpuPct')).toBe('number')
  const dist = field(summary, 'levelDistribution')
  expect(typeof field(dist, 'healthy')).toBe('number')
  expect(typeof field(dist, 'degraded')).toBe('number')
  expect(typeof field(dist, 'unhealthy')).toBe('number')
  const schedulable = field(summary, 'schedulable')
  expect(typeof field(schedulable, 'yes')).toBe('number')
  expect(typeof field(schedulable, 'no')).toBe('number')

  // /admin/v2/health → Paged<HealthItem>（有行时抽查首行字段）
  const health = await apiGetJson(page, token, '/admin/v2/health?pageSize=12')
  expect(typeof field(health, 'total')).toBe('number')
  const items = field(health, 'items')
  expect(Array.isArray(items)).toBeTruthy()
  const first = (items as unknown[])[0]
  if (first !== undefined) {
    expect(typeof field(first, 'serverId')).toBe('string')
    expect(typeof field(first, 'kind')).toBe('string')
    expect(typeof field(first, 'score')).toBe('number')
    expect(typeof field(first, 'level')).toBe('string')
    expect(typeof field(first, 'schedulable')).toBe('boolean')
    expect(Array.isArray(field(first, 'reasons'))).toBeTruthy()
    expect(typeof field(first, 'sampledAtMs')).toBe('number')
  }

  // /admin/v2/sched-decisions/summary → SchedDecisionSummary
  const sched = await apiGetJson(page, token, '/admin/v2/sched-decisions/summary?window=1h')
  expect(typeof field(sched, 'window')).toBe('string')
  expect(typeof field(sched, 'total')).toBe('number')
  expect(typeof field(sched, 'successCount')).toBe('number')
  expect(typeof field(sched, 'successRatePercent')).toBe('number')
  expect(Array.isArray(field(sched, 'failReasonTop'))).toBeTruthy()
  expect(typeof field(sched, 'localFallbackPercent')).toBe('number')

  // /admin/v2/health/snapshots → HealthSnapshotsResponse（{items: []} 包装，未知服为空数组）
  const snapshots = await apiGetJson(page, token, '/admin/v2/health/snapshots?serverId=ghost-fr154')
  expect(Array.isArray(field(snapshots, 'items'))).toBeTruthy()

  // /admin/v2/metrics/series → MetricsSeriesResponse（serverId 必填；未知服返回空 points）
  const series = await apiGetJson(page, token, '/admin/v2/metrics/series?serverId=ghost-fr154')
  expect(typeof field(series, 'stepSec')).toBe('number')
  const entries = field(series, 'series')
  expect(Array.isArray(entries)).toBeTruthy()
  const entry = (entries as unknown[])[0]
  expect(field(entry, 'serverId')).toBe('ghost-fr154')
  expect(Array.isArray(field(entry, 'points'))).toBeTruthy()

  // /admin/v2/health/{serverId} 未知服 → 404（ComparePanel 逐台详情失败仅缺行、不判错）
  const detail404 = await page.request.get('/admin/v2/health/ghost-fr154', {
    headers: authHeader(token),
  })
  expect(detail404.status()).toBe(404)

  // /admin/v1/alert-events → {total, items}（AlertOverview 零改消费的前提）
  const alerts = await apiGetJson(page, token, '/admin/v1/alert-events?page=1&size=100')
  expect(typeof field(alerts, 'total')).toBe('number')
  expect(Array.isArray(field(alerts, 'items'))).toBeTruthy()

  // /admin/v2/connections/stats：后端尚未提供（SPA 回退返回 HTML）→ FlowOverview 占位的前提。
  // 该端点交付后本断言应随 FlowOverview 接真一并更新。
  const connStats = await page.request.get('/admin/v2/connections/stats', {
    headers: authHeader(token),
  })
  const contentType = connStats.headers()['content-type'] ?? ''
  expect(
    connStats.status() === 404 || contentType.includes('text/html'),
    `connections/stats 应为未提供（404 或 SPA HTML），实际 HTTP ${String(connStats.status())} ${contentType}`,
  ).toBeTruthy()
})

test('服务分析：页面可达、选择列渲染真数据或空态、无加载失败', async ({ page }) => {
  await loginRealAdmin(page)
  await gotoPageViaNav(page, 'serviceAnalysis')

  // 左侧选择列标题 + 未选服时的引导提示
  await expect(page.getByText('选择服务器（可多选对比）')).toBeVisible()
  await expect(page.getByText('至少选择一台在线子服查看指标时序')).toBeVisible()

  // 选择列：空态 或 在线子服清单（复用集群 seed 数据时可能非空）
  await expect(
    page
      .getByText('暂无在线子服可供分析，接入并分配子服后展示指标时序')
      .or(page.getByRole('checkbox').first())
      .first(),
  ).toBeVisible()

  // 页面无「加载失败」（服务器清单打真 /admin/v2/servers）
  await expect(page.getByText(/加载失败/)).toHaveCount(0)
})
