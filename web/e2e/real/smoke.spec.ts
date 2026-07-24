// 真后端关键链路：用真 admin 凭据经 apps/web 登录页 /login 登录 + 主要页面可达（不白屏）。
// 控制面由 playwright webServer 以 sqlite + 固定凭据起（见 playwright.config.ts real 项目），
// 内嵌第二版管理台 apps/web（go:embed）。登录与页面文案以 apps/web i18n 为准，见 ./pages。

import { test, expect } from '@playwright/test'
import { loginRealAdmin } from '../shared/auth'
import { gotoPageViaNav, type PageKey } from './pages'

test.beforeEach(async ({ page }) => {
  await loginRealAdmin(page)
})

test('真 admin 登录后进入运维总览', async ({ page }) => {
  await expect(page.getByRole('heading', { name: '运维总览', exact: true })).toBeVisible()
})

// 真后端下逐个经侧栏导航主要页面并断言标题（空库下页面应正常渲染、不白屏）。
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
