// 假后端文件树预览（FR-45）：打开文件树预览页 → 选服 → 查看某文件有效配置预览。

import { test, expect } from '@playwright/test'
import { loginViaDemoMode } from '../shared/auth'
import { gotoPageViaNav } from '../shared/pages'

test('文件树预览：选服 → 渲染有效文件树', async ({ page }) => {
  await loginViaDemoMode(page)
  await gotoPageViaNav(page, 'filePreview')

  // 进入页默认空态提示「选择服务器查看有效文件树」
  await expect(page.getByText('选择服务器查看有效文件树')).toBeVisible()

  // 预览目标为原生 <select>（占位「选择服务器」+ 若干实例 serverId 选项）。
  // 选第一个真实实例选项（index 1，index 0 为占位空值）。
  const targetSelect = page.locator('select').first()
  await expect(targetSelect).toBeVisible()
  await targetSelect.selectOption({ index: 1 })

  // 选定后右侧渲染有效文件树：mock 固定返回 plugins/game/config.yml 文件项
  await expect(page.getByText('plugins/game/config.yml')).toBeVisible()
})
