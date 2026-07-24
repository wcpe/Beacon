// FR-157（调度决策与健康快照下钻）真后端 E2E：
// ① 下钻入口可达——dashboard 调度概览「前往服务分析」→ /service-analysis，「调度决策 / 健康快照」
//    板块切换常驻，子视图呈真数据或优雅空态、全页无「加载失败」；
// ② 契约交叉校验——以 page.request 携带 Bearer 令牌直打 /admin/v2/sched-decisions（from/to 毫秒必填）
//    与 /admin/v2/health/snapshots，断言响应 camelCase 字段对齐 contracts；
// ③ ?view=decisions 直达调度决策板块（dashboard 下钻定位）。
//
// 注意：真后端为 sqlite 单实例、用例间共享库（含其他 spec 的 seed 数据），故页面断言用
// 「空态文案 或 真数据元素」二选一的稳健形式，不假定库一定为空。

import { test, expect, type Page } from '@playwright/test'
import { loginRealAdmin } from '../shared/auth'

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

test('下钻入口可达：dashboard 调度概览 → 服务分析，调度决策 / 健康快照子视图真数据或空态', async ({
  page,
}) => {
  await loginRealAdmin(page)

  // dashboard 调度概览的「前往服务分析」下钻入口
  await page.getByRole('link', { name: '前往服务分析' }).click()
  await expect(page).toHaveURL(/\/service-analysis$/)
  await expect(page.getByRole('heading', { name: '服务分析', exact: true })).toBeVisible()

  // 板块切换常驻（未选服也可直达调度决策）
  await page.getByRole('tab', { name: '调度决策' }).click()
  await expect(page.getByRole('tab', { name: '调度决策' })).toHaveAttribute('aria-selected', 'true')
  // 筛选控件齐备（时间窗必选默认近 1h）
  await expect(page.getByLabel('时间范围')).toBeVisible()
  await expect(page.getByLabel('搜索 serverId（发起方或选中）')).toBeVisible()
  // 列表：空态文案 或 真数据（原因摘要列表头随表渲染常驻）
  await expect(
    page.getByText('当前时间窗与筛选条件下无调度决策').or(page.getByText('原因摘要')).first(),
  ).toBeVisible()

  // 健康快照板块：未选服显选服引导（不报错）
  await page.getByRole('tab', { name: '健康快照' }).click()
  await expect(page.getByText('至少选择一台在线子服回放健康快照')).toBeVisible()

  // 全页不得出现「加载失败」（各板块均为真数据 / 空态，无错误分支）
  await expect(page.getByText(/加载失败/)).toHaveCount(0)
})

test('契约交叉校验：sched-decisions 与 health/snapshots 端点响应形状对齐 contracts', async ({
  page,
}) => {
  const token = await loginRealAdmin(page)
  const to = Date.now()
  const from = to - 3_600_000

  // /admin/v2/sched-decisions → Paged<SchedDecisionItem>（from/to 毫秒必填；有行时抽查首行字段）
  const list = await apiGetJson(
    page,
    token,
    `/admin/v2/sched-decisions?from=${String(from)}&to=${String(to)}&pageSize=5`,
  )
  expect(typeof field(list, 'total')).toBe('number')
  const items = field(list, 'items')
  expect(Array.isArray(items)).toBeTruthy()
  const first = (items as unknown[])[0]
  if (first !== undefined) {
    expect(typeof field(first, 'traceId')).toBe('string')
    expect(typeof field(first, 'tsMs')).toBe('number')
    expect(typeof field(first, 'namespaceId')).toBe('number')
    expect(typeof field(first, 'crossNamespace')).toBe('boolean')
    expect(typeof field(first, 'requesterServerId')).toBe('string')
    expect(typeof field(first, 'zoneName')).toBe('string')
    expect(typeof field(first, 'strategy')).toBe('string')
    expect(typeof field(first, 'source')).toBe('string')
    expect(typeof field(first, 'candidateCount')).toBe('number')
    expect(typeof field(first, 'excludedCount')).toBe('number')
    expect(typeof field(first, 'chosenScore')).toBe('number')
    expect(typeof field(first, 'durationMs')).toBe('number')

    // 详情端点：同 traceId 返回逐台排除明细数组（excluded）
    const traceId = field(first, 'traceId') as string
    const detail = await apiGetJson(page, token, `/admin/v2/sched-decisions/${traceId}`)
    expect(field(detail, 'traceId')).toBe(traceId)
    expect(Array.isArray(field(detail, 'excluded'))).toBeTruthy()
  }

  // from/to 缺省 → 400（必填约束，前端下钻始终携带时间窗的前提）
  const badRange = await page.request.get('/admin/v2/sched-decisions', {
    headers: authHeader(token),
  })
  expect(badRange.status()).toBe(400)

  // 未知 traceId → 404（详情面板错误分支的前提）
  const detail404 = await page.request.get('/admin/v2/sched-decisions/ghost-trace-fr157', {
    headers: authHeader(token),
  })
  expect(detail404.status()).toBe(404)

  // /admin/v2/health/snapshots → HealthSnapshotsResponse（{items: []} 包装；未知服为空数组，
  // 有行时抽查首行字段）
  const snapshots = await apiGetJson(
    page,
    token,
    `/admin/v2/health/snapshots?serverId=ghost-fr157&from=${String(from)}&to=${String(to)}`,
  )
  const points = field(snapshots, 'items')
  expect(Array.isArray(points)).toBeTruthy()
  const point = (points as unknown[])[0]
  if (point !== undefined) {
    expect(typeof field(point, 'tsMs')).toBe('number')
    expect(typeof field(point, 'score')).toBe('number')
    expect(typeof field(point, 'level')).toBe('string')
    expect(typeof field(point, 'schedulable')).toBe('boolean')
    expect(Array.isArray(field(point, 'reasons'))).toBeTruthy()
    expect(typeof field(point, 'weightsRev')).toBe('number')
  }
})

test('?view=decisions 直达调度决策板块（dashboard 下钻定位）', async ({ page }) => {
  await loginRealAdmin(page)
  await page.goto('/service-analysis?view=decisions')

  await expect(page.getByRole('heading', { name: '服务分析', exact: true })).toBeVisible()
  await expect(page.getByRole('tab', { name: '调度决策' })).toHaveAttribute('aria-selected', 'true')
  // 子视图内容出现（空态 或 真数据），且无加载失败
  await expect(
    page.getByText('当前时间窗与筛选条件下无调度决策').or(page.getByText('原因摘要')).first(),
  ).toBeVisible()
  await expect(page.getByText(/加载失败/)).toHaveCount(0)
})
