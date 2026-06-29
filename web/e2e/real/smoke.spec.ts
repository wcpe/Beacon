// 真后端关键链路：用真 admin 凭据登录 + 主要页面可达（不白屏）。
// 控制面由 playwright webServer 以 sqlite + 固定凭据起（见 playwright.config.ts real 项目）。

import { test, expect } from '@playwright/test'
import { loginWithCredentials } from '../shared/auth'
import { gotoPageViaNav, type PageKey } from '../shared/pages'
import { REAL_ADMIN_USERNAME, REAL_ADMIN_PASSWORD } from '../../playwright.config'

test.beforeEach(async ({ page }) => {
  await loginWithCredentials(page, REAL_ADMIN_USERNAME, REAL_ADMIN_PASSWORD)
})

test('真凭据登录后进入配置中心', async ({ page }) => {
  await expect(page.getByRole('heading', { level: 1, name: '配置中心' })).toBeVisible()
})

// 真后端下逐个导航主要页面并断言标题（空库下页面应正常渲染、不白屏）。
const REACHABLE: PageKey[] = [
  'dashboard',
  'configs',
  'servers',
  'zones',
  'commands',
  'audits',
  'apiKeys',
  'namespaces',
  'settings',
]

for (const key of REACHABLE) {
  test(`真后端页面可达：${key}`, async ({ page }) => {
    await gotoPageViaNav(page, key)
  })
}
