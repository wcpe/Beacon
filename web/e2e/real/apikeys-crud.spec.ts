// 真后端增删改查闭环：密钥页（apps/web /api-keys）打到真控制面 + sqlite。
//   建一条密钥（真 POST /admin/v1/api-keys，真生成明文）→ 列表可见 → 点行开右侧详情面板 → 吊销（真 DELETE）→ 状态变已吊销。
// 说明：apps/web 密钥列表行内不带操作按钮，吊销 / 重置入口在点行后展开的右侧非模态详情面板里（与 Legacy 的行内按钮不同）。

import { test, expect } from '@playwright/test'
import { loginRealAdmin } from '../shared/auth'
import { gotoPageViaNav } from './pages'

test('真后端密钥：创建 → 列表可见 → 详情面板吊销 → 状态变已吊销', async ({ page }) => {
  await loginRealAdmin(page)
  await gotoPageViaNav(page, 'apiKeys')

  const keyName = `e2e-real-key-${Date.now()}`

  // ① 打开「创建密钥」对话框
  await page.getByRole('button', { name: '创建密钥' }).click()
  const createDialog = page.getByRole('dialog')
  await expect(createDialog.getByRole('heading', { name: '创建 API 密钥' })).toBeVisible()
  await createDialog.getByLabel('名称').fill(keyName)
  await createDialog.getByRole('button', { name: '创建', exact: true }).click()

  // ② 一次性明文展示 → 我已保存
  const revealDialog = page.getByRole('dialog')
  await expect(revealDialog.getByRole('heading', { name: '密钥已创建' })).toBeVisible()
  await revealDialog.getByRole('button', { name: '我已保存' }).click()

  // ③ 列表出现该密钥行，状态为「生效」
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
