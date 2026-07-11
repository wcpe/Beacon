// 真后端 E2E（内嵌第二版管理台 apps/web）专用页面对象：侧栏导航文案 + 路由 path + 页面主区段标题。
//
// 为何与 shared/pages.ts 分离：两套 E2E 打的是不同前端——
//   · 假后端 mock 打 Legacy web/（vite mock 模式），沿用 shared/pages.ts 的旧文案 / 结构；
//   · 真后端打 go:embed 内嵌的 apps/web（第二版），导航文案与页面结构以 apps/web i18n 为准。
// 若把 apps/web 文案塞回 shared/pages.ts 会破坏假后端套用例，故真后端另立本文件。
//
// 文案真源：侧栏 NavLink 文案见 apps/web/src/i18n/nav.ts（= t('nav.xxx')）与 apps/web/src/shell/sidebar.tsx；
// 页面主标题取各页顶部 SectionHeader 的 title（渲染为 <h2>，非 Legacy 的 level-1 页眉）。

import { expect, type Page } from '@playwright/test'

// 各页：侧栏 NavLink 可见名 + 路由 path + 页面主区段 SectionHeader 的 h2 标题。
// 注意：apps/web 主标题为 SectionHeader 渲染的 <h2>，故 heading 断言不限定 level（Legacy 用 level-1）。
export const PAGES = {
  dashboard: { nav: '运维总览', path: '/dashboard', heading: '运维总览' },
  configs: { nav: '配置中心', path: '/configs', heading: '配置中心' },
  servers: { nav: '服务器', path: '/servers', heading: '服务器' },
  zones: { nav: '区服分配', path: '/zones', heading: '区服分配' },
  commands: { nav: '命令观测', path: '/commands', heading: '命令观测' },
  audits: { nav: '审计', path: '/audits', heading: '审计' },
  apiKeys: { nav: '密钥', path: '/api-keys', heading: '密钥' },
  namespaces: { nav: '命名空间', path: '/namespaces', heading: '命名空间' },
  settings: { nav: '运维设置', path: '/settings', heading: '运维设置' },
} as const

export type PageKey = keyof typeof PAGES

// 经侧栏导航到某页，并断言页面主区段标题出现（页面已加载、未白屏）。
export async function gotoPageViaNav(page: Page, key: PageKey): Promise<void> {
  const meta = PAGES[key]
  await page.getByRole('link', { name: meta.nav, exact: true }).click()
  await expect(page).toHaveURL(new RegExp(escapeRegExp(meta.path) + '$'))
  await expect(page.getByRole('heading', { name: meta.heading, exact: true })).toBeVisible()
}

// 正则转义工具（路由 path 拼进 RegExp 时用）
function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}
