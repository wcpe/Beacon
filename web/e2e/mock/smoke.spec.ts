// 假后端冒烟：登录（演示模式）+ 主要页面逐个可达、不白屏。
// 覆盖看板 / 服务器 / 区域 / 配置工作台 / 文件预览 / 命令观测 / 审计 / 设置 等导航。

import { test, expect } from '@playwright/test'
import { loginViaDemoMode } from '../shared/auth'
import { PAGES, gotoPageViaNav, type PageKey } from '../shared/pages'

test.beforeEach(async ({ page }) => {
  await loginViaDemoMode(page)
})

test('演示模式登录后进入配置中心', async ({ page }) => {
  await expect(page.getByRole('heading', { level: 1, name: '配置中心' })).toBeVisible()
})

// 主要页面逐个经侧栏导航打开并断言标题（不白屏）。
const REACHABLE: PageKey[] = [
  'dashboard',
  'configs',
  'filePreview',
  'servers',
  'topology',
  'zones',
  'serviceAnalysis',
  'commands',
  'audits',
  'alertEvents',
  'settings',
  'apiKeys',
  'namespaces',
]

for (const key of REACHABLE) {
  test(`页面可达且有预期标题：${PAGES[key].nav}`, async ({ page }) => {
    await gotoPageViaNav(page, key)
  })
}
