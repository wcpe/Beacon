// 假后端增删改查闭环：密钥管理（/api-keys）。
//   建一条密钥 → 列表可见（生效）→ 吊销（高摩擦手输名复述确认）→ 行状态变「已吊销」。
// mock handlers 对 POST /api-keys、DELETE /api-keys/:id 有状态实现，闭环可在内存态完整反映。

import { test, expect } from '@playwright/test'
import { loginViaDemoMode } from '../shared/auth'
import { gotoPageViaNav } from '../shared/pages'

test('密钥：创建 → 列表可见 → 吊销 → 状态变已吊销', async ({ page }) => {
  await loginViaDemoMode(page)
  await gotoPageViaNav(page, 'apiKeys')

  // 用唯一名避免与种子数据 / 多次运行冲突
  const keyName = `e2e-key-${Date.now()}`

  // ① 打开「新建密钥」对话框（按钮在第二层页眉主操作槽）
  await page.getByRole('button', { name: '新建密钥' }).click()
  const createDialog = page.getByRole('dialog')
  await expect(createDialog.getByRole('heading', { name: '新建 API 密钥' })).toBeVisible()

  // ② 填名称（角色默认 readonly，过期留空）并提交
  await createDialog.getByLabel('名称').fill(keyName)
  await createDialog.getByRole('button', { name: '创建' }).click()

  // ③ 创建后弹一次性明文展示对话框 → 点「我已保存」关闭
  const revealDialog = page.getByRole('dialog')
  await expect(revealDialog.getByRole('heading', { name: '密钥已生成：请立即保存' })).toBeVisible()
  await revealDialog.getByRole('button', { name: '我已保存' }).click()

  // ④ 列表出现该密钥行，状态为「生效」
  const row = page.getByRole('row').filter({ hasText: keyName })
  await expect(row).toBeVisible()
  await expect(row.getByText('生效')).toBeVisible()

  // ⑤ 吊销：行内「吊销」→ 二次确认（手输密钥名复述高摩擦档）
  await row.getByRole('button', { name: '吊销' }).click()
  const confirm = page.getByRole('alertdialog')
  await expect(confirm.getByRole('heading', { name: `吊销密钥「${keyName}」？` })).toBeVisible()
  // 手输密钥名才放行确认（输入框 aria-label = '输入确认'）
  await confirm.getByLabel('输入确认').fill(keyName)
  await confirm.getByRole('button', { name: '确认吊销' }).click()

  // ⑥ 行状态变「已吊销」，操作列不再有吊销按钮（闭环反映成功）
  await expect(row.getByText('已吊销')).toBeVisible()
  await expect(row.getByRole('button', { name: '吊销' })).toHaveCount(0)
})
