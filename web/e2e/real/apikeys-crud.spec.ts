// 真后端增删改查闭环：密钥管理（/api-keys）打到真控制面 + sqlite。
//   建一条密钥（真 POST /admin/v1/api-keys，真生成明文）→ 列表可见 → 吊销（真 DELETE）→ 状态变已吊销。

import { test, expect } from '@playwright/test'
import { loginWithCredentials } from '../shared/auth'
import { gotoPageViaNav } from '../shared/pages'
import { REAL_ADMIN_USERNAME, REAL_ADMIN_PASSWORD } from '../../playwright.config'

test('真后端密钥：创建 → 列表可见 → 吊销 → 状态变已吊销', async ({ page }) => {
  await loginWithCredentials(page, REAL_ADMIN_USERNAME, REAL_ADMIN_PASSWORD)
  await gotoPageViaNav(page, 'apiKeys')

  const keyName = `e2e-real-key-${Date.now()}`

  // ① 新建密钥对话框
  await page.getByRole('button', { name: '新建密钥' }).click()
  const createDialog = page.getByRole('dialog')
  await expect(createDialog.getByRole('heading', { name: '新建 API 密钥' })).toBeVisible()
  await createDialog.getByLabel('名称').fill(keyName)
  await createDialog.getByRole('button', { name: '创建' }).click()

  // ② 一次性明文展示 → 我已保存
  const revealDialog = page.getByRole('dialog')
  await expect(revealDialog.getByRole('heading', { name: '密钥已生成：请立即保存' })).toBeVisible()
  await revealDialog.getByRole('button', { name: '我已保存' }).click()

  // ③ 列表出现该密钥行，状态为「生效」
  const row = page.getByRole('row').filter({ hasText: keyName })
  await expect(row).toBeVisible()
  await expect(row.getByText('生效')).toBeVisible()

  // ④ 吊销（手输密钥名复述高摩擦档）
  await row.getByRole('button', { name: '吊销' }).click()
  const confirm = page.getByRole('alertdialog')
  await expect(confirm.getByRole('heading', { name: `吊销密钥「${keyName}」？` })).toBeVisible()
  await confirm.getByLabel('输入确认').fill(keyName)
  await confirm.getByRole('button', { name: '确认吊销' }).click()

  // ⑤ 行状态变「已吊销」
  await expect(row.getByText('已吊销')).toBeVisible()
})
