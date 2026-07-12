// FR-157 可观测三页（/commands /audits /alert-events）真后端贯通 E2E：
// ① 三页经侧栏导航可达（真数据或空态优雅渲染、无「加载失败」）；
// ② 以 page.request 携带 Bearer 令牌直打 /admin/v1 三端点交叉校验契约形状
//   （page/size 分页、响应 total+items、字段 camelCase 对齐 packages/contracts）；
// ③ 互跳一条链真点击验证：审计详情 →「在命令观测中查看」→ /commands?serverId=X，
//    落位页 serverId 筛选以 URL 参数初始化（FR-157 核心缺口回归）。
//
// 注意：真后端为 sqlite 单实例、用例间共享库，故先 seed 一个 namespace（其 namespace.create
// 专项审计产出确定的审计行，targetRef = namespace 名），断言不假定库为空。

import { randomUUID } from 'node:crypto'
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

// 断言分页信封形状：total 为数字、items 为数组，返回 items
function expectPagedEnvelope(body: unknown, label: string): unknown[] {
  expect(typeof field(body, 'total'), `${label}.total 应为 number`).toBe('number')
  const items = field(body, 'items')
  expect(Array.isArray(items), `${label}.items 应为数组`).toBeTruthy()
  return items as unknown[]
}

// seed 一个 namespace（写路径产出一条 namespace.create 审计，targetRef = 名称）
async function seedNamespace(page: Page, token: string, name: string): Promise<void> {
  const res = await page.request.post('/admin/v2/namespaces', {
    headers: authHeader(token),
    data: { name },
  })
  expect(res.ok(), `POST /admin/v2/namespaces → HTTP ${String(res.status())}`).toBeTruthy()
}

test('可观测三页经侧栏导航可达且无加载失败', async ({ page }) => {
  await loginRealAdmin(page)

  await gotoPageViaNav(page, 'commands')
  await expect(page.getByText('命令总数')).toBeVisible()
  await expect(page.getByText('命令历史')).toBeVisible()
  await expect(page.getByText('加载失败')).toHaveCount(0)

  await gotoPageViaNav(page, 'audits')
  await expect(page.getByText('审计总数')).toBeVisible()
  await expect(page.getByText('审计记录')).toBeVisible()
  await expect(page.getByText('加载失败')).toHaveCount(0)

  await gotoPageViaNav(page, 'alertEvents')
  await expect(page.getByText('告警总数')).toBeVisible()
  await expect(page.getByText('加载失败')).toHaveCount(0)
})

test('契约交叉校验：/admin/v1 三端点 total+items 信封与 camelCase 字段', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const nsName = `e2e-fr157a-${randomUUID().slice(0, 8)}`
  await seedNamespace(page, token, nsName)

  // /admin/v1/commands：信封 + 元数据字段（空库允许 0 条；有条目则字段齐备且绝不含 payload）
  const commands = await apiGetJson(page, token, '/admin/v1/commands?page=1&size=5')
  const commandItems = expectPagedEnvelope(commands, 'commands')
  for (const item of commandItems) {
    expect(typeof field(item, 'commandId')).toBe('number')
    expect(typeof field(item, 'serverId')).toBe('string')
    expect(typeof field(item, 'status')).toBe('string')
    expect(typeof field(item, 'ageSeconds')).toBe('number')
    expect(field(item, 'payload'), 'commands 元数据绝不含 payload').toBeUndefined()
  }

  // /admin/v1/audits：seed 后至少 1 条（namespace.create），全字段类型断言
  const audits = await apiGetJson(page, token, '/admin/v1/audits?page=1&size=5')
  const auditItems = expectPagedEnvelope(audits, 'audits')
  expect(auditItems.length, 'seed 后审计应至少 1 条').toBeGreaterThan(0)
  const first = auditItems[0]
  expect(typeof field(first, 'id')).toBe('number')
  expect(typeof field(first, 'namespace')).toBe('string')
  expect(typeof field(first, 'operator')).toBe('string')
  expect(typeof field(first, 'action')).toBe('string')
  expect(typeof field(first, 'targetType')).toBe('string')
  expect(typeof field(first, 'targetRef')).toBe('string')
  expect(typeof field(first, 'detail')).toBe('string')
  expect(['ok', 'fail']).toContain(field(first, 'result'))
  expect(typeof field(first, 'clientIp')).toBe('string')
  expect(typeof field(first, 'createdAt')).toBe('string')

  // 服务端过滤契约：action + targetRef 组合命中 seed 行
  const filtered = await apiGetJson(
    page,
    token,
    `/admin/v1/audits?action=namespace.create&targetRef=${encodeURIComponent(nsName)}`,
  )
  const filteredItems = expectPagedEnvelope(filtered, 'audits(filtered)')
  expect(filteredItems.length, 'action+targetRef 过滤应命中 seed 审计').toBeGreaterThan(0)
  expect(field(filteredItems[0], 'targetRef')).toBe(nsName)

  // /admin/v1/alert-events：信封 + P5b 处理工作流字段（空库允许 0 条；有条目则含 status/handled*）
  const alerts = await apiGetJson(page, token, '/admin/v1/alert-events?page=1&size=5')
  const alertItems = expectPagedEnvelope(alerts, 'alert-events')
  for (const item of alertItems) {
    expect(typeof field(item, 'id')).toBe('number')
    expect(['open', 'acknowledged', 'resolved']).toContain(field(item, 'status'))
    // 处理三字段键必在（未处理为 null，字段名 camelCase）
    expect('handledBy' in (item as Record<string, unknown>)).toBeTruthy()
    expect('handledAt' in (item as Record<string, unknown>)).toBeTruthy()
    expect('handleNote' in (item as Record<string, unknown>)).toBeTruthy()
  }
})

test('互跳链真点击：审计详情 → 命令观测，serverId 筛选按 URL 初始化', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const nsName = `e2e-fr157b-${randomUUID().slice(0, 8)}`
  await seedNamespace(page, token, nsName)

  // 审计页找到 seed 行（namespace.create，targetRef = nsName，最新故在第 1 页）并点开详情
  await gotoPageViaNav(page, 'audits')
  await page.getByRole('row').filter({ hasText: nsName }).first().click()
  await expect(page.getByText('审计详情')).toBeVisible()

  // 点互跳链接 → 落 /commands?serverId=<targetRef>
  await page.getByRole('link', { name: '在命令观测中查看' }).click()
  await expect(page).toHaveURL(new RegExp(`/commands\\?serverId=${nsName}$`))
  await expect(page.getByRole('heading', { name: '命令观测', exact: true })).toBeVisible()

  // 落位页 serverId 筛选以 URL 参数初始化（FR-157 贯通核心断言）
  await expect(page.getByLabel('搜索 serverId')).toHaveValue(nsName)
  // 该 serverId 无命令 → 历史列表空态（服务端过滤确被驱动）
  await expect(page.getByText('当前筛选条件下无命令记录')).toBeVisible()
})
