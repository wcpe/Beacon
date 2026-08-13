// 真后端增删改查闭环：密钥页（apps/web /api-keys）打到真控制面 + sqlite。
//   提交创建审批 → 批准并执行 → 原申请人一次性领取 → 列表可见 → 点行开右侧详情面板 → 吊销（真 DELETE）→ 状态变已吊销。
// 说明：apps/web 密钥列表行内不带操作按钮，吊销 / 重置入口在点行后展开的右侧非模态详情面板里（与 Legacy 的行内按钮不同）。

import { test, expect, type Page } from '@playwright/test'
import { loginRealAdmin } from '../shared/auth'
import { gotoPageViaNav } from './pages'

function authHeader(token: string): Record<string, string> {
  return { Authorization: `Bearer ${token}` }
}

async function approveAndWait(page: Page, token: string, requestId: string): Promise<void> {
  const approved = await page.request.post(`/admin/v2/approval-requests/${requestId}/approve`, {
    headers: authHeader(token),
  })
  expect(approved.status()).toBe(202)
  await expect
    .poll(async () => {
      const response = await page.request.get(`/admin/v2/approval-requests/${requestId}`, { headers: authHeader(token) })
      expect(response.ok()).toBeTruthy()
      return ((await response.json()) as { status: string }).status
    })
    .toBe('succeeded')
}

test('真后端密钥：创建 → 列表可见 → 详情面板吊销 → 状态变已吊销', async ({ page }) => {
  const token = await loginRealAdmin(page)
  await gotoPageViaNav(page, 'apiKeys')

  const keyName = `e2e-real-key-${Date.now()}`

  // ① 打开「创建密钥」对话框
  await page.getByRole('button', { name: '创建密钥' }).click()
  const createDialog = page.getByRole('dialog')
  await expect(createDialog.getByRole('heading', { name: '创建 API 密钥' })).toBeVisible()
  await createDialog.getByLabel('名称').fill(keyName)
  await createDialog.getByLabel('审批原因').fill('真后端 E2E 创建测试密钥')
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()

  // ② 创建仅产生审批票据；批准并执行后由原申请人一次性领取明文。
  await expect(page.getByText('API 密钥审批申请已创建，审批执行完成后可由原申请人一次性领取明文。')).toBeVisible()
  const approvalLink = page.getByRole('link', { name: '前往审批中心查看进度' })
  const approvalHref = await approvalLink.getAttribute('href')
  expect(approvalHref).toMatch(/^\/approvals\?requestId=.+/)
  const requestId = new URL(`http://localhost${approvalHref ?? ''}`).searchParams.get('requestId')
  expect(requestId).not.toBeNull()
  await approveAndWait(page, token, requestId ?? '')

  const redeemed = await page.request.post(`/admin/v2/approval-requests/${requestId ?? ''}/credential-secret/redeem`, {
    headers: authHeader(token),
  })
  expect(redeemed.status()).toBe(200)
  expect(typeof ((await redeemed.json()) as { secret: string }).secret).toBe('string')

  // ③ 审批 worker 是页外异步执行者，刷新后列表出现该密钥行。
  await page.reload()
  const row = page.getByRole('row').filter({ hasText: keyName })
  await expect(row).toBeVisible()
  await expect(row.getByText('生效')).toBeVisible()

  // ④ 点行展开右侧详情面板 → 面板内「吊销」（行内无操作按钮，入口在详情面板）
  await row.click()
  await page.getByRole('button', { name: '吊销', exact: true }).click()

  // ⑤ 破坏性二次确认（标题含密钥名，无手输复述档）→ 确认吊销
  const confirm = page.getByRole('alertdialog')
  await expect(confirm.getByRole('heading', { name: `吊销密钥「${keyName}」？` })).toBeVisible()
  await confirm.getByRole('button', { name: '确认吊销' }).click()

  // ⑥ 行状态变「已吊销」
  await expect(row.getByText('已吊销')).toBeVisible()
})
