// 两套 E2E（假后端 / 真后端）共用的页面对象与步骤。
// 选择器优先稳定语义：getByRole('link'|'heading'|'button', {name:中文文案})；
// 中文文案来自 i18n（默认 zh-CN），见 src/i18n/locales/zh-CN.ts。

import { expect, type Page } from '@playwright/test'

// 各主要页面：侧栏导航文案（NavLink 可见名）+ 路由 path + 页面加载后第二层页眉 h1 标题文案。
// 标题取自各页 usePageHeader({title}) 实际注入值（多数 = t('xxx.title')）。
export const PAGES = {
  dashboard: { nav: '运维总览', path: '/dashboard', heading: '运维总览' },
  configs: { nav: '配置中心', path: '/configs', heading: '配置中心' },
  filePreview: { nav: '文件树预览', path: '/file-preview', heading: '文件树有效预览' },
  servers: { nav: '服务器', path: '/servers', heading: '服务器' },
  topology: { nav: '集群拓扑', path: '/topology', heading: '集群拓扑' },
  zones: { nav: '区分配', path: '/zones', heading: '区分配' },
  serviceAnalysis: { nav: '服务分析', path: '/service-analysis', heading: '服务分析' },
  commands: { nav: '命令观测', path: '/commands', heading: '命令观测' },
  audits: { nav: '审计日志', path: '/audits', heading: '审计日志' },
  alertEvents: { nav: '事件', path: '/alert-events', heading: '事件信息流' },
  settings: { nav: '运维设置', path: '/settings', heading: '运维设置' },
  apiKeys: { nav: '密钥管理', path: '/api-keys', heading: '密钥管理' },
  namespaces: { nav: '环境管理', path: '/namespaces', heading: '环境管理' },
} as const

export type PageKey = keyof typeof PAGES

// 经侧栏导航到某页，并断言第二层页眉标题出现（页面已加载、未白屏）。
export async function gotoPageViaNav(page: Page, key: PageKey): Promise<void> {
  const meta = PAGES[key]
  await page.getByRole('link', { name: meta.nav, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(escapeRegExp(meta.path) + '$'))
  await expect(page.getByRole('heading', { level: 1, name: meta.heading })).toBeVisible()
}

// 直接深链到某页（用于不便逐页点导航的场景），并断言页面标题。
export async function gotoPageByUrl(page: Page, key: PageKey): Promise<void> {
  const meta = PAGES[key]
  await page.goto(meta.path)
  await expect(page.getByRole('heading', { level: 1, name: meta.heading })).toBeVisible()
}

// 断言已进入管理台（侧栏导航可见）：登录成功的统一判据。
export async function expectLoggedIn(page: Page): Promise<void> {
  // 侧栏「配置中心」导航链常驻可见即说明已进受保护区
  await expect(page.getByRole('link', { name: PAGES.configs.nav, exact: true })).toBeVisible()
}

// 选定全局环境（第二层页眉右侧 EnvSelector，aria-label='全局环境' 的 Combobox）。
// 选定后值持久化到 localStorage、跨页保持。环境范围页（配置中心 / 看板等）需先选环境数据才加载。
// 仅在环境范围页可用（非环境页无该选择器）。
export async function selectEnvironment(page: Page, code: string): Promise<void> {
  const combo = page.getByRole('textbox', { name: '全局环境' })
  await combo.click()
  // 键入 code 过滤候选，再点中匹配项（label 形如「prod · 生产」，按 code 子串过滤命中）
  await combo.fill(code)
  await page
    .getByRole('option', { name: new RegExp(code) })
    .first()
    .click()
}

// 正则转义工具（路由 path 拼进 RegExp 时用）
function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
