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

// 真后端令牌头注入：调真实登录端点取 HMAC 令牌，注入到浏览器后续所有请求头。
//
// 为何需要：内嵌的第二版权威前端 apps/web 目前尚未内建登录页与令牌附着逻辑
//（发布产物里既无 /login 页，也不给 fetch 带 Authorization），因此常规「填表单登录」
// 流程（loginWithCredentials）对它不适用。此处以测试侧手段注入真实 admin 令牌，
// 让内嵌 app 的真实请求能通过控制面鉴权、打到真后端渲染真数据——不改任何生产源码。
// 返回令牌，供测试内以 page.request 携带鉴权做数据 seed 与交叉校验。
export async function attachRealAdminAuth(page: Page): Promise<string> {
  const res = await page.request.post('/admin/v1/auth/login', {
    data: { username: REAL_ADMIN_USERNAME, password: REAL_ADMIN_PASSWORD },
  })
  if (!res.ok()) {
    throw new Error(`真 admin 登录失败：HTTP ${String(res.status())}`)
  }
  const body = (await res.json()) as { token: string }
  await page.setExtraHTTPHeaders({ Authorization: `Bearer ${body.token}` })
  return body.token
}
