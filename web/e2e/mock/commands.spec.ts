// 假后端命令观测（FR-104）：命令观测页 KPI / 列表渲染。
// 页内三视图（segmented tab）：实时（默认，含队列表格）/ 历史（查询表格）/ 分析（KPI + 趋势）。

import { test, expect } from '@playwright/test'
import { loginViaDemoMode } from '../shared/auth'
import { gotoPageViaNav } from '../shared/pages'

test('命令观测：实时队列 / 历史 / 分析 KPI 渲染', async ({ page }) => {
  await loginViaDemoMode(page)
  await gotoPageViaNav(page, 'commands')

  // 默认「实时」视图选中，实时队列区块与表格渲染
  await expect(page.getByRole('tab', { name: '实时' })).toHaveAttribute('aria-selected', 'true')
  await expect(page.getByText('实时队列')).toBeVisible()
  // 队列表格列头渲染
  await expect(page.getByRole('columnheader', { name: '命令 ID' })).toBeVisible()

  // 切到「历史」视图：历史查询区块标题渲染
  await page.getByRole('tab', { name: '历史' }).click()
  await expect(page.getByText('历史查询')).toBeVisible()

  // 切到「分析」视图：KPI 卡片「命令总数」+ 命令量趋势渲染
  await page.getByRole('tab', { name: '分析' }).click()
  await expect(page.getByText('命令总数')).toBeVisible()
  await expect(page.getByText('命令量趋势')).toBeVisible()
})
