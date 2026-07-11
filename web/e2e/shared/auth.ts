// 两套 E2E 的登录步骤。
//   - 假后端：走登录页「演示模式（免后端体验）」按钮，开启 mock 并以演示身份进入。
//   - 真后端：用真 admin 凭据走登录表单。

import { expect, type Page } from '@playwright/test'
import { expectLoggedIn } from './pages'
import { REAL_ADMIN_USERNAME, REAL_ADMIN_PASSWORD } from '../../playwright.config'

// 假后端登录：打开登录页 → 点「演示模式」→ 进入配置中心。
export async function loginViaDemoMode(page: Page): Promise<void> {
  await page.goto('/login')
  // 演示模式按钮：文案为 login.demoMode = '演示模式（免后端体验）'
  await page.getByRole('button', { name: /演示模式/ }).click()
  // 演示模式直接 navigate 到目标页（默认 /configs），断言已进入管理台
  await expect(page).toHaveURL(/\/configs$/)
  await expectLoggedIn(page)
}

// 真后端登录：填账号 / 口令 → 提交 → 进入配置中心。
export async function loginWithCredentials(
  page: Page,
  username: string,
  password: string,
): Promise<void> {
  await page.goto('/login')
  // 账号 / 口令输入框：Label「账号」「口令」关联（htmlFor=l-username / l-password）
  await page.getByLabel('账号').fill(username)
  await page.getByLabel('口令').fill(password)
  // 登录按钮：login.submit = '登录'（与标题「Beacon 管理台」区分用 button 角色）
  await page.getByRole('button', { name: '登录', exact: true }).click()
  await expect(page).toHaveURL(/\/configs$/)
  await expectLoggedIn(page)
}

// 第二版管理台 apps/web 真登录（FR-179 落地后）：走真实登录页 /login 填真 admin 凭据提交，
// 应用把 HMAC 令牌存 localStorage 并给后续 fetch 注入 Authorization、路由守卫据此放行。
// 返回令牌，供测试内以 page.request 携带鉴权做数据 seed 与交叉校验（page.request 与页面同源
// 但不共享 localStorage，故仍需显式带头）。
//
// 取代旧的「测试侧注入令牌头」workaround：FR-179 前 apps/web 无登录页 / 无令牌附着，只能靠
// setExtraHTTPHeaders 注头；现登录闭环已建，直接走真实登录流程，且路由守卫要求 localStorage
// 有令牌（仅注头不再够）。
export async function loginRealAdmin(page: Page): Promise<string> {
  await page.goto('/login')
  await page.getByLabel('用户名').fill(REAL_ADMIN_USERNAME)
  await page.getByLabel('口令').fill(REAL_ADMIN_PASSWORD)
  await page.getByRole('button', { name: '登录', exact: true }).click()
  // 登录成功默认回跳首页「/」→ 重定向到运维总览 /dashboard
  await expect(page).toHaveURL(/\/dashboard$/)
  const token = await page.evaluate(() => localStorage.getItem('beacon.token'))
  if (token === null || token === '') {
    throw new Error('登录成功后未在 localStorage 取得令牌')
  }
  return token
}
