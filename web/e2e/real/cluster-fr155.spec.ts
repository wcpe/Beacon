// FR-155 集群管理三页（/servers /zones /namespaces）真后端 E2E：
// 打到真控制面（sqlite + 真 admin 令牌）验证「可达 + 真数据渲染 + 关键写闭环」。
//
// 鉴权说明：apps/web 登录鉴权已落地（FR-179），故走真实登录页 /login 用真 admin 凭据登录，
// 应用存令牌到 localStorage 并给 fetch 注入 Authorization、路由守卫据此放行。
// 数据 seed 与交叉校验走 page.request 携带同一令牌（page.request 不共享页面 localStorage）。

import { randomUUID } from 'node:crypto'
import { test, expect, type Locator, type Page } from '@playwright/test'
import { loginRealAdmin } from '../shared/auth'

// ---- 携带 admin 令牌的真后端 API 辅助（seed 与交叉校验用）----

function authHeader(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

async function apiGet<T>(page: Page, token: string, path: string): Promise<T> {
  const res = await page.request.get(path, { headers: authHeader(token) })
  expect(res.ok(), `GET ${path} → HTTP ${String(res.status())}`).toBeTruthy()
  return (await res.json()) as T
}

async function apiPost<T>(page: Page, token: string, path: string, data: unknown): Promise<T> {
  const res = await page.request.post(path, { headers: authHeader(token), data })
  expect(res.ok(), `POST ${path} → HTTP ${String(res.status())}`).toBeTruthy()
  return (await res.json()) as T
}

// ---- FR-155 领域 seed 辅助（映射真实 /admin/v2 与 /beacon/v2 端点）----

interface SeededNamespace {
  id: number
  accessToken: string
}

// 创建 namespace，返回其 id 与一次性接入 token（注册 agent 需要）。
async function seedNamespace(page: Page, token: string, name: string): Promise<SeededNamespace> {
  const ns = await apiPost<{ id: number; accessToken: string }>(
    page,
    token,
    '/admin/v2/namespaces',
    {
      name,
    },
  )
  return { id: ns.id, accessToken: ns.accessToken }
}

// 建 BC 集群 / 大区 / 小区（创建端点响应已改 camelCase 对齐契约，字段为 id）。
async function seedCluster(
  page: Page,
  token: string,
  namespaceId: number,
  name: string,
): Promise<number> {
  const c = await apiPost<{ id: number }>(page, token, '/admin/v2/bc-clusters', {
    namespaceId,
    name,
  })
  return c.id
}

async function seedRegion(
  page: Page,
  token: string,
  bcClusterId: number,
  name: string,
): Promise<number> {
  const r = await apiPost<{ id: number }>(page, token, '/admin/v2/regions', { bcClusterId, name })
  return r.id
}

async function seedZone(
  page: Page,
  token: string,
  regionId: number,
  name: string,
): Promise<number> {
  const z = await apiPost<{ id: number }>(page, token, '/admin/v2/zones', { regionId, name })
  return z.id
}

// 用 namespace 接入 token 注册一个 agent，造出「待确认」身份（返回 202 Accepted）。
async function registerAgent(
  page: Page,
  accessToken: string,
  params: { identityId: string; serverId: string; kind: 'backend' | 'proxy' },
): Promise<void> {
  const res = await page.request.post('/beacon/v2/agent/register', {
    headers: { 'X-Beacon-Token': accessToken },
    data: { ...params, bootId: `boot-${params.serverId}` },
  })
  expect(res.status(), `register ${params.serverId}`).toBe(202)
}

interface ApprovalTicket {
  approvalRequestId: string
  status: string
}

interface ApprovalView {
  status: string
}

// 危险动作只提交申请；审批决定和 worker 执行由统一审批链路完成。
async function approveTicket(page: Page, token: string, ticket: ApprovalTicket): Promise<void> {
  expect(ticket.status).toBe('pending')

  const approved = await page.request.post(
    `/admin/v2/approval-requests/${ticket.approvalRequestId}/approve`,
    {
      headers: authHeader(token),
    },
  )
  expect(approved.status(), '审批决定应被接受').toBe(202)

  await expect
    .poll(async () => {
      const approval = await apiGet<ApprovalView>(
        page,
        token,
        `/admin/v2/approval-requests/${ticket.approvalRequestId}`,
      )
      return approval.status
    })
    .toBe('succeeded')
}

// 身份确认必须经申请、人工批准与 worker 执行，不允许测试直调领域写入口。
async function approveIdentity(
  page: Page,
  token: string,
  identityId: string,
  serverId: string,
): Promise<void> {
  const ticket = await apiPost<ApprovalTicket>(
    page,
    token,
    `/admin/v2/agent-identities/${identityId}/approve`,
    {
      serverId,
      reason: '真后端 E2E 确认待接入身份',
    },
  )
  await approveTicket(page, token, ticket)
}

interface ServerItemLike {
  id: number
  serverId: string
  assigned: boolean
  draining: boolean
  zoneName: string | null
  pendingZoneName: string | null
}

async function listServers(
  page: Page,
  token: string,
  namespaceId: number,
): Promise<ServerItemLike[]> {
  const list = await apiGet<{ items: ServerItemLike[] }>(
    page,
    token,
    `/admin/v2/servers?namespaceId=${String(namespaceId)}`,
  )
  return list.items
}

async function findServer(
  page: Page,
  token: string,
  namespaceId: number,
  serverId: string,
): Promise<ServerItemLike> {
  const item = (await listServers(page, token, namespaceId)).find((s) => s.serverId === serverId)
  expect(item, `未找到 server ${serverId}`).toBeTruthy()
  return item as ServerItemLike
}

// 注册 + 确认一台未分配 server，返回其 server 行 id 与生成时用的 identityId。
async function seedApprovedServer(
  page: Page,
  token: string,
  ns: SeededNamespace,
  serverId: string,
  kind: 'backend' | 'proxy' = 'backend',
): Promise<{ rowId: number; identityId: string }> {
  const identityId = randomUUID()
  await registerAgent(page, ns.accessToken, { identityId, serverId, kind })
  await approveIdentity(page, token, identityId, serverId)
  const row = await findServer(page, token, ns.id, serverId)
  return { rowId: row.id, identityId }
}

// 首次分配一台 server 到目标（小区 / 集群）。
async function assignServer(
  page: Page,
  token: string,
  rowId: number,
  target: { kind: 'zone' | 'bc_cluster'; id: number },
  isDefaultEntry = false,
): Promise<void> {
  const ticket = await apiPost<ApprovalTicket>(page, token, '/admin/v2/server-assignments', {
    serverIds: [rowId],
    target,
    isDefaultEntry,
    reason: 'seed',
  })
  await approveTicket(page, token, ticket)
}

// 短唯一后缀，隔离并行 / 多次运行的数据。
function uid(): string {
  return `${Date.now().toString(36)}${Math.floor(Math.random() * 1e4).toString(36)}`
}

// 幂等展开结构树某节点（切 namespace 后节点默认收起）：未展开则点其行按钮，直到 aria-expanded=true。
// 注意：hasText 会同时命中包含该名的所有祖先 treeitem，取 .last() 定位到名字所属的最内层节点本身。
async function ensureExpanded(page: Page, name: string): Promise<void> {
  const item = page.getByRole('treeitem').filter({ hasText: name }).last()
  await expect(item).toBeVisible()
  await expect(async () => {
    if ((await item.getAttribute('aria-expanded')) !== 'true') {
      await item.getByRole('button').first().click()
    }
    expect(await item.getAttribute('aria-expanded')).toBe('true')
  }).toPass({ timeout: 15000, intervals: [300, 600, 1000] })
}

async function fillCreateNodeDialog(dialog: Locator, name: string): Promise<void> {
  await dialog.getByLabel('业务标识').fill(name)
  await dialog.getByLabel('显示名称').fill(name)
}

// ================= 可达 + 真数据渲染 =================

test('三页接真后端可达：真实 /admin/v2 200、渲染真数据与空态、不白屏', async ({ page }) => {
  const token = await loginRealAdmin(page)

  // /servers —— 页面骨架 + 资产面板渲染；真实 servers 列表为数组（空库空态）
  await page.goto('/servers')
  await expect(page.getByRole('heading', { name: '服务器', exact: true })).toBeVisible()
  await expect(page.getByText('服务器资产')).toBeVisible()
  const servers = await apiGet<{ items: unknown[] }>(page, token, '/admin/v2/servers')
  expect(Array.isArray(servers.items)).toBeTruthy()

  // /zones —— 结构树渲染（bootstrap 的 prod 无集群 → 空态引导）
  await page.goto('/zones')
  await expect(page.getByRole('heading', { name: '区服结构树' })).toBeVisible()
  const tree = await apiGet<{ clusters: unknown[] }>(
    page,
    token,
    '/admin/v2/zone-tree?namespaceId=1',
  )
  expect(Array.isArray(tree.clusters)).toBeTruthy()

  // /namespaces —— 真数据：bootstrap 预置的 prod / test 两条
  await page.goto('/namespaces')
  await expect(page.getByRole('heading', { name: '命名空间', exact: true })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'prod' })).toBeVisible()
  await expect(page.getByRole('row').filter({ hasText: 'test' })).toBeVisible()
  const nss = await apiGet<{ total: number }>(page, token, '/admin/v2/namespaces')
  expect(nss.total).toBeGreaterThanOrEqual(2)
})

// ================= /namespaces 写闭环 =================

test('命名空间：创建 → 一次性接入 token 展示 → 列表可见（交叉校验真后端）', async ({ page }) => {
  const token = await loginRealAdmin(page)
  await page.goto('/namespaces')

  const name = `e2e-ns-${uid()}`
  await page.getByRole('button', { name: '创建命名空间' }).click()
  const createDialog = page.getByRole('dialog')
  await createDialog.getByLabel('业务标识').fill(name)
  await createDialog.getByLabel('显示名称').fill(name)
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()

  // 一次性明文接入 token 弹窗（token 前缀 bn_）
  const tokenDialog = page.getByRole('dialog')
  await expect(tokenDialog.getByText('命名空间已创建')).toBeVisible()
  await expect(tokenDialog.locator('code')).toContainText('bn_')
  await tokenDialog.getByRole('button', { name: '我已保存' }).click()

  // 列表出现该行
  await expect(page.getByRole('row').filter({ hasText: name })).toBeVisible()

  // 交叉校验：真后端确有该 namespace
  const nss = await apiGet<{ items: { name: string }[] }>(
    page,
    token,
    '/admin/v2/namespaces?pageSize=100',
  )
  expect(nss.items.some((n) => n.name === name)).toBeTruthy()
})

// 说明：授予动作全程走 UI（详情面板 → 授予弹窗 → 真实 POST）；授予后的存在性与收回闭环
// 以真后端端点校验（GET /admin/v2/namespace-trusts 已返回 camelCase 富化视图，对齐契约）。
test('命名空间：经 UI 授予单向信任（真写入）+ 端点校验收回闭环', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const suffix = uid()
  const fromName = `e2e-ta-${suffix}`
  const toName = `e2e-tb-${suffix}`
  const fromNs = await seedNamespace(page, token, fromName)
  const toNs = await seedNamespace(page, token, toName)

  await page.goto('/namespaces')
  // 选中来源 namespace 行 → 打开右侧详情面板 → 授予入口
  await page.getByRole('row').filter({ hasText: fromName }).click()
  await page.getByRole('button', { name: '授予信任' }).click()

  const grantDialog = page.getByRole('dialog')
  await grantDialog.getByRole('combobox', { name: '来源命名空间' }).click()
  await page.getByRole('option', { name: fromName, exact: true }).click()
  await grantDialog.getByRole('combobox', { name: '目标命名空间' }).click()
  await page.getByRole('option', { name: toName, exact: true }).click()
  await grantDialog.getByLabel('建立原因').fill('联调放通跨域调度')
  const approvalResponse = page.waitForResponse(
    (response) =>
      response.url().includes('/admin/v2/namespace-trusts') &&
      response.request().method() === 'POST',
  )
  await grantDialog.getByRole('button', { name: '授予', exact: true }).click()
  const ticket = (await (await approvalResponse).json()) as ApprovalTicket
  // 授予成功后弹窗关闭
  await expect(page.getByRole('button', { name: '授予', exact: true })).toHaveCount(0)
  await expect(page.getByRole('status')).toContainText('等待审批中心执行')
  await approveTicket(page, token, ticket)

  // 交叉校验：真后端确有该 UI 授予的生效信任（列表端点返回 camelCase 富化视图）
  interface TrustItem {
    id: number
    fromNamespaceId: number
    toNamespaceId: number
    status: string
  }
  const trusts = await apiGet<{ items: TrustItem[] }>(
    page,
    token,
    `/admin/v2/namespace-trusts?fromNamespaceId=${String(fromNs.id)}&toNamespaceId=${String(toNs.id)}&pageSize=100`,
  )
  const created = trusts.items.find(
    (t) => t.fromNamespaceId === fromNs.id && t.toNamespaceId === toNs.id && t.status === 'active',
  )
  expect(created, '经 UI 授予的信任应在真后端可见且生效').toBeTruthy()

  // 收回闭环：真后端端点收回 → 状态转 revoked
  const revoke = await page.request.post(
    `/admin/v2/namespace-trusts/${String(created?.id ?? 0)}/revoke`,
    {
      headers: authHeader(token),
      data: { reason: '业务下线不再需要跨域' },
    },
  )
  expect(revoke.ok(), `revoke → HTTP ${String(revoke.status())}`).toBeTruthy()
  const after = await apiGet<{ items: TrustItem[] }>(page, token, '/admin/v2/namespace-trusts')
  expect(after.items.find((t) => t.id === created?.id)?.status).toBe('revoked')
})

// ================= /zones 写闭环 =================

test('区服分配：新建 集群 → 大区 → 小区，结构树渲染层级（交叉校验真后端）', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const suffix = uid()
  const clusterName = `bc-${suffix}`
  const regionName = `r-${suffix}`
  const zoneName = `z-${suffix}`

  await page.goto('/zones')
  // 显式选择 bootstrap 的 prod，避免前序 E2E 残留的页面状态改变当前命名空间。
  await page.getByRole('combobox', { name: '命名空间' }).click()
  await page.getByRole('option', { name: 'prod', exact: true }).click()
  await expect(page.getByRole('heading', { name: '区服结构树' })).toBeVisible()

  // 新建集群
  await page.getByRole('button', { name: '新建集群' }).click()
  let dialog = page.getByRole('dialog')
  await fillCreateNodeDialog(dialog, clusterName)
  await dialog.getByRole('button', { name: '创建', exact: true }).click()
  // 展开集群，露出其下大区行
  await ensureExpanded(page, clusterName)

  // 该集群下新建大区（集群行 trailing 按钮，name 用 exact 避免命中「行按钮」整体名）
  const clusterItem = page.getByRole('treeitem').filter({ hasText: clusterName }).first()
  await clusterItem.getByRole('button', { name: '新建大区', exact: true }).click()
  dialog = page.getByRole('dialog')
  await fillCreateNodeDialog(dialog, regionName)
  await dialog.getByRole('button', { name: '创建', exact: true }).click()
  // 该大区下新建小区
  const regionItem = page.getByRole('treeitem').filter({ hasText: regionName }).first()
  await regionItem.getByRole('button', { name: '新建小区', exact: true }).click()
  dialog = page.getByRole('dialog')
  await fillCreateNodeDialog(dialog, zoneName)
  await dialog.getByRole('button', { name: '创建', exact: true }).click()

  // 交叉校验：真后端结构树含 集群 → 大区 → 小区 层级
  interface Tree {
    clusters: { name: string; regions: { name: string; zones: { name: string }[] }[] }[]
  }
  await expect
    .poll(async () => {
      const tree = await apiGet<Tree>(page, token, '/admin/v2/zone-tree?namespaceId=1')
      const cluster = tree.clusters.find((c) => c.name === clusterName)
      const region = cluster?.regions.find((r) => r.name === regionName)
      return region?.zones.some((z) => z.name === zoneName) ?? false
    })
    .toBeTruthy()
})

test('区服分配：未分配子服首次落小区，分配结果可见（交叉校验真后端）', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const suffix = uid()
  const nsName = `e2e-asn-${suffix}`
  const clusterName = `bc-${suffix}`
  const regionName = `r-${suffix}`
  const zoneName = `z-${suffix}`
  const serverId = `lobby-${suffix}`

  const ns = await seedNamespace(page, token, nsName)
  const clusterId = await seedCluster(page, token, ns.id, clusterName)
  const regionId = await seedRegion(page, token, clusterId, regionName)
  await seedZone(page, token, regionId, zoneName)
  await seedApprovedServer(page, token, ns, serverId, 'backend')

  await page.goto('/zones')
  // 切到我的 namespace
  await page.getByRole('combobox', { name: '命名空间' }).click()
  await page.getByRole('option', { name: nsName, exact: true }).click()

  // 打开「未分配」窄栏
  await page.getByRole('button', { name: /未分配/ }).click()
  await expect(page.getByRole('checkbox', { name: `选择 ${serverId}` })).toBeVisible()
  await page.getByRole('checkbox', { name: `选择 ${serverId}` }).check()

  // 分配到… → 目标树里逐层展开选中小区 → 确认分配
  await page.getByRole('button', { name: '分配到…' }).click()
  const assignDialog = page.getByRole('dialog')
  await assignDialog.getByRole('button', { name: clusterName }).click()
  await assignDialog.getByRole('button', { name: regionName }).click()
  await assignDialog.getByRole('treeitem', { name: zoneName }).click()
  await assignDialog.getByLabel('申请原因').fill('真后端 E2E 首次分配')
  const approvalResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname === '/admin/v2/server-assignments',
  )
  await assignDialog.getByRole('button', { name: '确认分配' }).click()
  const ticket = (await (await approvalResponse).json()) as ApprovalTicket
  await expect(assignDialog.getByRole('status')).toContainText('等待审批中心执行')
  await approveTicket(page, token, ticket)
  await page.reload()

  // 交叉校验：真后端 server 已落到该小区
  const server = await findServer(page, token, ns.id, serverId)
  expect(server.assigned).toBeTruthy()
  expect(server.zoneName).toBe(zoneName)
})

test('区服分配：已分配子服右键改派 → 解绑重确认（走换区工单，非后台直改）', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const suffix = uid()
  const nsName = `e2e-rz-${suffix}`
  const clusterName = `bc-${suffix}`
  const regionName = `r-${suffix}`
  const zoneAName = `za-${suffix}`
  const zoneBName = `zb-${suffix}`
  const serverId = `lobby-${suffix}`

  const ns = await seedNamespace(page, token, nsName)
  const clusterId = await seedCluster(page, token, ns.id, clusterName)
  const regionId = await seedRegion(page, token, clusterId, regionName)
  const zoneAId = await seedZone(page, token, regionId, zoneAName)
  await seedZone(page, token, regionId, zoneBName)
  const { rowId } = await seedApprovedServer(page, token, ns, serverId, 'backend')
  await assignServer(page, token, rowId, { kind: 'zone', id: zoneAId })

  await page.goto('/zones')
  await page.getByRole('combobox', { name: '命名空间' }).click()
  await page.getByRole('option', { name: nsName, exact: true }).click()

  // 逐层展开 集群 → 大区 → 小区 A，露出已分配子服叶子（切 namespace 后不自动展开）
  await ensureExpanded(page, clusterName)
  await ensureExpanded(page, regionName)
  await ensureExpanded(page, zoneAName)
  const leaf = page.getByRole('listitem').filter({ hasText: serverId })
  await expect(leaf).toBeVisible()

  // 右键叶子 → 上下文菜单「改派到…」→ 点选式改派弹窗
  await leaf.click({ button: 'right' })
  await page.getByRole('menuitem', { name: '改派到…' }).click()
  const rezoneDialog = page.getByRole('dialog')
  await expect(rezoneDialog.getByRole('heading', { name: '确认换区改派' })).toBeVisible()
  await rezoneDialog.getByRole('button', { name: clusterName }).click()
  await rezoneDialog.getByRole('button', { name: regionName }).click()
  await rezoneDialog.getByRole('treeitem', { name: zoneBName }).click()
  await rezoneDialog.getByLabel('换区原因').fill('扩容换区')
  const approvalResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname === '/admin/v2/server-rezones',
  )
  await rezoneDialog.getByRole('button', { name: '确认', exact: true }).click()
  const ticket = (await (await approvalResponse).json()) as ApprovalTicket
  await expect(page.getByRole('status')).toContainText('等待审批中心执行')
  await approveTicket(page, token, ticket)

  // 交叉校验：换区工单发起后解绑清归属 + 写预填目标 zoneB（非后台直接改区）
  await expect
    .poll(async () => {
      const server = await findServer(page, token, ns.id, serverId)
      return server.assigned === false && server.pendingZoneName === zoneBName
    })
    .toBeTruthy()
  // 身份重入 pending（等待 agent 重确认落区），佐证「解绑重确认」而非直改
  const pending = await apiGet<{ items: { serverId: string }[] }>(
    page,
    token,
    `/admin/v2/agent-identities?namespaceId=${String(ns.id)}&status=pending`,
  )
  expect(pending.items.some((i) => i.serverId === serverId)).toBeTruthy()
})

// ================= /servers 写闭环 =================

test('服务器：注册待确认 → 确认接入 → 身份转 active、进入资产列表（交叉校验真后端）', async ({
  page,
}) => {
  const token = await loginRealAdmin(page)
  const suffix = uid()
  const ns = await seedNamespace(page, token, `e2e-srv-${suffix}`)
  const serverId = `srv-${suffix}`
  const identityId = randomUUID()
  await registerAgent(page, ns.accessToken, { identityId, serverId, kind: 'backend' })

  await page.goto('/servers')
  // 打开待确认抽屉
  await page.getByRole('button', { name: /注册待确认/ }).click()
  // 待确认行必须限定在抽屉作用域内：确认接入后该 server 会进入资产表（同样含 serverId），
  // 若用全页 getByRole('row') 过滤，reload 后会命中资产行导致「待确认行应消失」恒假失败。
  const pendingSheet = page.locator('[data-slot="sheet-content"]')
  await expect(pendingSheet).toBeVisible()
  const pendingRow = pendingSheet.getByRole('row').filter({ hasText: serverId })
  await expect(pendingRow).toBeVisible()
  await pendingRow.getByRole('button', { name: '确认接入' }).click()
  // 确认弹窗（无需原因）
  const approveDialog = page.getByRole('alertdialog')
  await expect(approveDialog.getByRole('heading', { name: '确认接入服务器' })).toBeVisible()
  await approveDialog.getByLabel('原因').fill('真后端 E2E 确认接入')
  const approvalResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname === `/admin/v2/agent-identities/${identityId}/approve`,
  )
  await approveDialog.getByRole('button', { name: '确认接入' }).click()
  const ticket = (await (await approvalResponse).json()) as ApprovalTicket
  await approveTicket(page, token, ticket)

  // worker 完成后刷新资产视图：重新打开待确认抽屉，该行不应再出现（作用域限定在抽屉内，
  // 不把「已进入资产表」误判成待确认残留）。
  await page.reload()
  await page.getByRole('button', { name: /注册待确认/ }).click()
  await expect(pendingSheet).toBeVisible()
  await expect(pendingSheet.getByRole('row').filter({ hasText: serverId })).toHaveCount(0)

  // 交叉校验：身份转 active、server 已进入真后端资产列表
  await expect
    .poll(async () => {
      const detail = await apiGet<{ status: string }>(
        page,
        token,
        `/admin/v2/agent-identities/${identityId}`,
      )
      return detail.status
    })
    .toBe('active')
  await findServer(page, token, ns.id, serverId)
})

test('服务器：已分配子服切换排空标记写闭环（交叉校验真后端）', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const suffix = uid()
  const nsName = `e2e-drn-${suffix}`
  const serverId = `lobby-${suffix}`
  const ns = await seedNamespace(page, token, nsName)
  const clusterId = await seedCluster(page, token, ns.id, `bc-${suffix}`)
  const regionId = await seedRegion(page, token, clusterId, `r-${suffix}`)
  const zoneId = await seedZone(page, token, regionId, `z-${suffix}`)
  const { rowId } = await seedApprovedServer(page, token, ns, serverId, 'backend')
  await assignServer(page, token, rowId, { kind: 'zone', id: zoneId })

  await page.goto('/servers')
  await page.getByRole('textbox', { name: '搜索服务器 ID 或显示名称' }).fill(serverId)
  const row = page.getByRole('row').filter({ hasText: serverId })
  await expect(row).toBeVisible()

  // 行操作菜单中置为排空 → 原因必填 → 确认
  await row.getByRole('button', { name: '操作' }).click()
  await page.getByRole('menuitem', { name: '置为排空' }).click()
  const drainDialog = page.getByRole('alertdialog')
  await expect(drainDialog.getByRole('heading', { name: '切换排空标记' })).toBeVisible()
  await drainDialog.getByLabel('原因').fill('维护窗口')
  const directResponse = page.waitForResponse(
    (response) =>
      response.url().includes(`/admin/v2/servers/${serverId}/draining`) &&
      response.request().method() === 'PUT',
  )
  await drainDialog.getByRole('button', { name: '置为排空' }).click()
  const direct = await directResponse
  expect(direct.status(), '置为排空是直接止损动作').toBe(200)
  expect(((await direct.json()) as ServerItemLike).draining).toBeTruthy()
  await page.reload()

  // 进入排空态后，恢复调度必须经审批。
  const refreshedRow = page.getByRole('row').filter({ hasText: serverId })
  await refreshedRow.getByRole('button', { name: '操作' }).click()
  await page.getByRole('menuitem', { name: '取消排空' }).click()
  const restoreDialog = page.getByRole('alertdialog')
  await restoreDialog.getByLabel('原因').fill('维护完成')
  const approvalResponse = page.waitForResponse(
    (response) =>
      response.url().includes(`/admin/v2/servers/${serverId}/draining`) &&
      response.request().method() === 'PUT',
  )
  await restoreDialog.getByRole('button', { name: '取消排空' }).click()
  const ticket = (await (await approvalResponse).json()) as ApprovalTicket
  await expect(page.getByRole('status')).toContainText('等待审批中心执行')
  await approveTicket(page, token, ticket)
  await page.reload()

  // 交叉校验：worker 批准后恢复调度。
  await expect
    .poll(async () => (await findServer(page, token, ns.id, serverId)).draining)
    .toBeFalsy()
})
