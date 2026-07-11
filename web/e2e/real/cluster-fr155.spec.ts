// FR-155 集群管理三页（/servers /zones /namespaces）真后端 E2E：
// 打到真控制面（sqlite + 真 admin 令牌）验证「可达 + 真数据渲染 + 关键写闭环」。
//
// 鉴权说明：apps/web 登录鉴权已落地（FR-179），故走真实登录页 /login 用真 admin 凭据登录，
// 应用存令牌到 localStorage 并给 fetch 注入 Authorization、路由守卫据此放行。
// 数据 seed 与交叉校验走 page.request 携带同一令牌（page.request 不共享页面 localStorage）。

import { randomUUID } from 'node:crypto'
import { test, expect, type Page } from '@playwright/test'
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

// 建 BC 集群 / 大区 / 小区。注意：这三个创建端点返回原始 GORM 模型（PascalCase 字段 ID）。
async function seedCluster(
  page: Page,
  token: string,
  namespaceId: number,
  name: string,
): Promise<number> {
  const c = await apiPost<{ ID: number }>(page, token, '/admin/v2/bc-clusters', {
    namespaceId,
    name,
  })
  return c.ID
}

async function seedRegion(
  page: Page,
  token: string,
  bcClusterId: number,
  name: string,
): Promise<number> {
  const r = await apiPost<{ ID: number }>(page, token, '/admin/v2/regions', { bcClusterId, name })
  return r.ID
}

async function seedZone(
  page: Page,
  token: string,
  regionId: number,
  name: string,
): Promise<number> {
  const z = await apiPost<{ ID: number }>(page, token, '/admin/v2/zones', { regionId, name })
  return z.ID
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

// 确认（approve）一个待确认身份 → 生成未分配 server 行。
async function approveIdentity(page: Page, token: string, identityId: string): Promise<void> {
  await apiPost(page, token, `/admin/v2/agent-identities/${identityId}/approve`, {})
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
  await approveIdentity(page, token, identityId)
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
  const res = await apiPost<{ results: { ok: boolean }[] }>(
    page,
    token,
    '/admin/v2/server-assignments',
    {
      serverIds: [rowId],
      target,
      isDefaultEntry,
      reason: 'seed',
    },
  )
  expect(res.results[0].ok, '分配应逐台 ok').toBeTruthy()
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
  await page.getByRole('button', { name: '创建 命名空间' }).click()
  const createDialog = page.getByRole('dialog')
  await createDialog.getByLabel('名称').fill(name)
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()

  // 一次性明文接入 token 弹窗（token 前缀 bn_）
  const tokenDialog = page.getByRole('dialog')
  await expect(tokenDialog.getByText('命名空间 已创建')).toBeVisible()
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

// 说明：授予动作全程走 UI（详情面板 → 授予弹窗 → 真实 POST）。信任「关系面板渲染」与
// 「面板内 UI 收回」当前被后端契约不一致阻断：GET /admin/v2/namespace-trusts 返回原始
// GORM 模型（PascalCase：FromNamespaceID… 且无 fromNamespaceName），而前端 NamespaceTrustItem
// 契约与详情面板按 camelCase（tr.fromNamespaceId / fromNamespaceName）读取 → 关系恒为空、
// 收回按钮不渲染。故这里以真后端端点校验授予 + 收回闭环，UI 面板缺口在交付报告中单列。
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
  await grantDialog.getByRole('combobox', { name: '来源 命名空间' }).click()
  await page.getByRole('option', { name: fromName, exact: true }).click()
  await grantDialog.getByRole('combobox', { name: '目标 命名空间' }).click()
  await page.getByRole('option', { name: toName, exact: true }).click()
  await grantDialog.getByLabel('建立原因').fill('联调放通跨域调度')
  await grantDialog.getByRole('button', { name: '授予', exact: true }).click()
  // 授予成功后弹窗关闭
  await expect(page.getByRole('button', { name: '授予', exact: true })).toHaveCount(0)

  // 交叉校验：真后端确有该 UI 授予的生效信任（列表端点返回原始模型 PascalCase 字段）
  interface RawTrust {
    ID: number
    FromNamespaceID: number
    ToNamespaceID: number
    Status: string
  }
  const trusts = await apiGet<{ items: RawTrust[] }>(page, token, '/admin/v2/namespace-trusts')
  const created = trusts.items.find(
    (t) => t.FromNamespaceID === fromNs.id && t.ToNamespaceID === toNs.id && t.Status === 'active',
  )
  expect(created, '经 UI 授予的信任应在真后端可见且生效').toBeTruthy()

  // 收回闭环：真后端端点收回 → 状态转 revoked
  const revoke = await page.request.post(
    `/admin/v2/namespace-trusts/${String(created?.ID ?? 0)}/revoke`,
    {
      headers: authHeader(token),
      data: { reason: '业务下线不再需要跨域' },
    },
  )
  expect(revoke.ok(), `revoke → HTTP ${String(revoke.status())}`).toBeTruthy()
  const after = await apiGet<{ items: RawTrust[] }>(page, token, '/admin/v2/namespace-trusts')
  expect(after.items.find((t) => t.ID === created?.ID)?.Status).toBe('revoked')
})

// ================= /zones 写闭环 =================

test('区服分配：新建 集群 → 大区 → 小区，结构树渲染层级（交叉校验真后端）', async ({ page }) => {
  const token = await loginRealAdmin(page)
  const suffix = uid()
  const clusterName = `bc-${suffix}`
  const regionName = `r-${suffix}`
  const zoneName = `z-${suffix}`

  await page.goto('/zones')
  // 等 namespace 选择器自动选中首个（prod），此后 create 才落到有效 namespace
  await expect(page.getByRole('combobox', { name: '命名空间' })).toContainText('prod')
  await expect(page.getByRole('heading', { name: '区服结构树' })).toBeVisible()

  // 新建集群
  await page.getByRole('button', { name: '新建集群' }).click()
  let dialog = page.getByRole('dialog')
  await dialog.getByLabel('名称').fill(clusterName)
  await dialog.getByRole('button', { name: '创建', exact: true }).click()
  await expect(page.getByText(clusterName, { exact: true })).toBeVisible()

  // 展开集群，露出其下大区行
  await ensureExpanded(page, clusterName)

  // 该集群下新建大区（集群行 trailing 按钮，name 用 exact 避免命中「行按钮」整体名）
  const clusterItem = page.getByRole('treeitem').filter({ hasText: clusterName }).first()
  await clusterItem.getByRole('button', { name: '新建大区', exact: true }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('名称').fill(regionName)
  await dialog.getByRole('button', { name: '创建', exact: true }).click()
  await expect(page.getByText(regionName, { exact: true })).toBeVisible()

  // 该大区下新建小区
  const regionItem = page.getByRole('treeitem').filter({ hasText: regionName }).first()
  await regionItem.getByRole('button', { name: '新建小区', exact: true }).click()
  dialog = page.getByRole('dialog')
  await dialog.getByLabel('名称').fill(zoneName)
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
  await assignDialog.getByRole('button', { name: '确认分配' }).click()

  await expect(page.getByText('1 台分配成功')).toBeVisible()

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
  const leaf = page.getByText(serverId, { exact: true })
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
  await rezoneDialog.getByRole('button', { name: '确认', exact: true }).click()

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
  const pendingRow = page.getByRole('row').filter({ hasText: serverId })
  await expect(pendingRow).toBeVisible()
  await pendingRow.getByRole('button', { name: '确认接入' }).click()
  // 确认弹窗（无需原因）
  const approveDialog = page.getByRole('alertdialog')
  await expect(approveDialog.getByRole('heading', { name: '确认接入服务器' })).toBeVisible()
  await approveDialog.getByRole('button', { name: '确认接入' }).click()

  // UI 闭环：确认后该待确认行从抽屉消失
  await expect(pendingRow).toHaveCount(0)

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
  await page.getByRole('textbox', { name: '搜索 serverId' }).fill(serverId)
  const row = page.getByRole('row').filter({ hasText: serverId })
  await expect(row).toBeVisible()

  // 置为排空 → 原因必填 → 确认
  await row.getByRole('button', { name: '置为排空' }).click()
  const drainDialog = page.getByRole('alertdialog')
  await expect(drainDialog.getByRole('heading', { name: '切换排空标记' })).toBeVisible()
  await drainDialog.getByLabel('原因').fill('维护窗口')
  await drainDialog.getByRole('button', { name: '置为排空' }).click()

  // 行内出现「取消排空」按钮（已进入排空态）
  await expect(row.getByRole('button', { name: '取消排空' })).toBeVisible()

  // 交叉校验：真后端 server.draining=true
  await expect
    .poll(async () => (await findServer(page, token, ns.id, serverId)).draining)
    .toBeTruthy()
})
