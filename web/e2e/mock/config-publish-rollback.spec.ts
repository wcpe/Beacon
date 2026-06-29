// 假后端配置发布 / 回滚闭环（配置中心双面板工作台 FR-111/112）。
//
// 工作台为「按覆盖层发布 → 热推到受影响在线服」语义：
//   选中受管文件 → 顶部状态栏「发布选中 N 项」→ 发布面板（含版本 vN→vN+1 + 影响面 + 拓印审核门）
//   → 发布并热推（toast 已发布）→ 顶部「撤回上一步」回滚该发布（toast 已撤回）。
// 这是工作台真实的发布 / 回滚（撤回）闭环；版本变化在发布面板「将发布」区以 vN→vN+1 直观可见。

import { test, expect } from '@playwright/test'
import { loginViaDemoMode } from '../shared/auth'
import { gotoPageViaNav, selectEnvironment } from '../shared/pages'

test('配置：选中受管文件 → 发布（看到版本 vN→vN+1）→ 撤回回滚', async ({ page }) => {
  await loginViaDemoMode(page)
  await gotoPageViaNav(page, 'configs')

  // 配置中心为环境范围页：先选环境（prod），受管树才按环境加载（否则 namespace 空、查询禁用）。
  await selectEnvironment(page, 'prod')

  // 受管树为 plugins → 嵌套文件夹 → 文件（默认仅根展开）。在左「受管配置」面板内逐层展开露出文件。
  // 左面板为第一个 min-w-[320px] 卡片（右「服务器」面板为第二个），按此 scope 避免与右面板同名项冲突。
  const leftPanel = page.locator('div[class*="min-w-[320px]"]').first()
  // 受管文件 config.yml 仅受管侧有（服务器侧为 spawn.yml/motd.yml/… 无 config.yml），左面板内定位无歧义。
  const fileCheckbox = leftPanel.getByRole('checkbox', { name: 'config.yml' })
  // 折叠态文件夹行：可拖行 + chevron-right 箭头；展开态为 chevron-down。
  const collapsedFolder = leftPanel.locator('div[class*="cursor-grab"]', {
    has: page.locator('svg.lucide-chevron-right'),
  })
  // 先等受管树按 prod 真正加载出可展开的嵌套折叠文件夹（selectEnvironment 仅切环境、不等查询落定，
  // 受管树 listFiles 查询异步返回；不等会在 0 行时空转致后续断言超时）。
  await expect(collapsedFolder.first()).toBeVisible()
  // 逐层展开露出文件复选框（受管树线性嵌套 plugins→…→文件）：每轮点开「当前第一个折叠文件夹」、
  // 留出展开/异步加载时间，按最新状态重判，最多 30 轮。**软重试**——并行 CPU 紧张下单次点击偶尔
  // 不生效时，下一轮对同一折叠文件夹再点；不在中途因一次点击丢失即硬失败（消除该用例的脆性）。
  for (let i = 0; i < 30 && (await fileCheckbox.count()) === 0; i++) {
    if ((await collapsedFolder.count()) > 0) {
      await collapsedFolder.first().click()
    }
    await page.waitForTimeout(250)
  }
  await expect(fileCheckbox.first()).toBeVisible({ timeout: 10_000 })
  await fileCheckbox.first().check()

  // 受管侧文件被选中后顶部固定状态栏出现「发布选中 N 项」按钮，点开发布面板。
  const publishSelectedBtn = page.getByRole('button', { name: /发布选中 \d+ 项/ })
  await expect(publishSelectedBtn).toBeVisible()
  await publishSelectedBtn.click()

  // 发布面板：标题 + 「将发布」区显示版本变化 vN → vN+1（看到版本变化）
  await expect(page.getByText('发布 + 影响面')).toBeVisible()
  await expect(page.getByText('将发布', { exact: true })).toBeVisible()
  // 版本号变化文案形如 v1 → v2（PublishPanel 固定 fromVersion=1, toVersion=2）
  await expect(page.getByText(/v\d+\s*→/).first()).toBeVisible()

  // 若有差异（影响面有受影响在线服），需勾「我已审阅全部 diff」才放行发布。
  const reviewLabel = page.getByText('我已审阅全部 diff')
  if (await reviewLabel.count()) {
    await reviewLabel.click()
  }

  // 点「发布并热推（N 台）」确认发布
  await page.getByRole('button', { name: /发布并热推/ }).click()

  // 发布成功 toast：'已发布 N 项并热推到 M 台在线服…'
  await expect(page.getByText(/已发布 \d+ 项并热推/)).toBeVisible()

  // ===== 回滚：顶部「撤回上一步」撤回刚才的发布 =====
  const undoBtn = page.getByRole('button', { name: '撤回上一步' })
  await expect(undoBtn).toBeEnabled()
  await undoBtn.click()

  // 撤回成功 toast：'已撤回：…'（回到发布前状态）
  await expect(page.getByText(/已撤回[：:]/)).toBeVisible()
})
