// 两套 E2E 的登录步骤。
//   - 假后端：走登录页「演示模式（免后端体验）」按钮，开启 mock 并以演示身份进入。
//   - 真后端：用真 admin 凭据走登录表单。

import { expect, type Page } from '@playwright/test'
import { expectLoggedIn } from './pages'

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
