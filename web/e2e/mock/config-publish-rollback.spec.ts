// 假后端配置发布 / 回滚闭环（配置中心重设计页 /configs，四层覆盖链，纯前端 mock 驱动）。
//
// 页面语义：进入页默认选中受管文件 plugins/Essentials/config.yml（mock 初始 v3）：
//   工作区「发布」→ 发布弹层（危险确认勾选门）→「发布并热更」→ 版本升为 v4；
//   右栏「版本 · 回滚」对上一版本「diff/回滚」→ 编辑器 diff 标签「回滚到 v3」
//   → 回滚弹层（审阅 diff 勾选 + 两段确认）→ 读 v3 内容发为新版本 v5。
// 版本变化在右栏「版本 · 回滚」区以「vN 当前」直观可见。

import { test, expect, type Page } from '@playwright/test'
import { loginViaDemoMode } from '../shared/auth'
import { gotoPageViaNav, selectEnvironment } from '../shared/pages'

// 右栏「版本 · 回滚」区的当前版本行（行内恰为「vN」+「当前」两个 span，整行文本 vN当前）
function currentVersionRow(page: Page, v: number) {
  return page.locator('div').filter({ hasText: new RegExp(`^v${v}当前$`) })
}

test('配置：发布（看到版本 vN→vN+1）→ 回滚到上一版本', async ({ page }) => {
  await loginViaDemoMode(page)
  await gotoPageViaNav(page, 'configs')

  // 配置中心为环境范围页：先选全局环境（prod），与真实运维进入路径一致
  await selectEnvironment(page, 'prod')

  // 默认选中受管文件 config.yml（mock 初始 v3）：右栏「版本 · 回滚」显示「v3 当前」
  await expect(currentVersionRow(page, 3)).toBeVisible()

  // ===== 发布：工作区「发布」→ 弹层内勾危险确认 → 「发布并热更」 =====
  await page.getByRole('button', { name: '发布', exact: true }).click()
  await expect(page.getByText('发布 · config.yml')).toBeVisible()
  // 危险操作勾选门：未勾选时「发布并热更」禁用，勾选后放行
  const publishBtn = page.getByRole('button', { name: '发布并热更' })
  await expect(publishBtn).toBeDisabled()
  await page.getByRole('checkbox', { name: /我确认将 config\.yml 发布到/ }).check()
  await publishBtn.click()

  // 发布成功 toast + 版本 v3 → v4（看到版本变化）
  await expect(page.getByText('已发布并热更')).toBeVisible()
  await expect(currentVersionRow(page, 4)).toBeVisible()

  // toast 悬浮右下角会拦截其下方按钮的点击（且鼠标悬停会暂停其自动消失），先移开鼠标等它消失
  await page.mouse.move(0, 0)
  await expect(page.getByText('已发布并热更')).toBeHidden()

  // ===== 回滚：右栏对上一版本 v3「diff/回滚」→ diff 标签「回滚到 v3」→ 弹层两段确认 =====
  await page.getByRole('button', { name: 'diff/回滚' }).first().click()
  await page.getByRole('button', { name: '回滚到 v3', exact: true }).click()
  // 回滚弹层：勾「我已审阅以上 diff…」→ 「回滚…」 → 「再次确认：回滚到 v3」
  await page.getByRole('checkbox', { name: /我已审阅以上 diff/ }).check()
  await page.getByRole('button', { name: '回滚…', exact: true }).click()
  await page.getByRole('button', { name: '再次确认：回滚到 v3' }).click()

  // 回滚 = 读 v3 内容发为新版本：当前版本再 +1 到 v5（回到发布前内容）
  await expect(currentVersionRow(page, 5)).toBeVisible()
})
