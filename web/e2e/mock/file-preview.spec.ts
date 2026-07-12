// 假后端文件树预览（FR-45）：打开文件树预览页 → 选服 → 查看某文件有效配置预览。

import { test, expect } from '@playwright/test'
import { loginViaDemoMode } from '../shared/auth'
import { gotoPageViaNav } from '../shared/pages'

test('文件树预览：选服 → 渲染有效文件树', async ({ page }) => {
  await loginViaDemoMode(page)
  await gotoPageViaNav(page, 'filePreview')

  // 进入页默认空态提示「选择服务器查看有效文件树」
  await expect(page.getByText('选择服务器查看有效文件树')).toBeVisible()

  // 预览目标为 Combobox（占位「选择服务器」的输入框，点开列出实例候选 role=option）。
  const targetCombo = page.getByPlaceholder('选择服务器', { exact: true })
  await expect(targetCombo).toBeVisible()
  await targetCombo.click()
  // 点选第一个实例候选（label 形如「server-01 (main/az1)」）
  await page.getByRole('option').first().click()

  // 选定后右侧渲染有效文件树：mock 固定返回 plugins/game/config.yml 文件项
  await expect(page.getByText('plugins/game/config.yml')).toBeVisible()
})
