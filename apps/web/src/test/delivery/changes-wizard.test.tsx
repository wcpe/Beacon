// /changes 引导创建五步向导测试：三条路径（纯文件 / 纯配置 / 混合）走通成单、
// 空态任务卡带预选类型进向导、步骤校验（未选模板源不能下一步）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ChangesPage from '../../pages/changes'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

// 向导为多步重流程（jsdom 下逐步交互 + 多次网络往返），全量并行跑套件时默认 5s 会脆弱超时
const WIZARD_TEST_TIMEOUT = 20_000

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

/** 打开向导（等列表就绪保证 namespace 已选定） */
async function openWizard(user: ReturnType<typeof userEvent.setup>): Promise<HTMLElement> {
  await screen.findByText('大厅插件升级 v2.4')
  await user.click(screen.getByRole('button', { name: '引导创建' }))
  return await screen.findByRole('dialog')
}

/** 第 2 步：选模板源并完成扫描 */
async function pickSourceAndScan(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement): Promise<void> {
  await user.click(await within(dialog).findByRole('radio', { name: /lobby-2/ }))
  await user.click(within(dialog).getByRole('button', { name: /扫描差异/ }))
  // 差异清单出现（语义色计数徽标）
  await within(dialog).findByText(/新增 \d+/)
  expect(within(dialog).getByText(/删除 \d+/)).toBeInTheDocument()
}

/** 第 3 步：勾选一个配置文件并等版本解析完成 */
async function pickConfigFile(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement): Promise<void> {
  const checkbox = await within(dialog).findByRole('checkbox', { name: 'plugins/Essentials/config.yml' })
  await user.click(checkbox)
  await within(dialog).findByText('已选 1 个配置文件')
}

/** 第 4 步 → 第 5 步：默认全量 + 分批推进，直接下一步并等影响面就绪 */
async function throughScopeToReview(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement): Promise<void> {
  await within(dialog).findByText('交付范围')
  await user.click(within(dialog).getByRole('button', { name: '下一步' }))
  // 第 5 步：标题已填默认值 + 影响面汇总出现
  await within(dialog).findByLabelText('变更单标题')
  await within(dialog).findByText('目标总数')
}

/** 提交审批并断言成单：向导关闭、详情面板打开、状态为待审批 */
async function submitAndAssert(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement): Promise<void> {
  await user.click(within(dialog).getByRole('button', { name: '提交审批' }))
  await waitFor(() => {
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })
  // 成单后选中该单打开右侧详情面板（非模态）
  await screen.findByRole('button', { name: '返回列表' })
  expect((await screen.findAllByText('待审批')).length).toBeGreaterThan(0)
}

describe('/changes 引导创建向导', () => {
  it('纯文件路径：选模板源扫差异 → 范围批次 → 预览提交成单', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const dialog = await openWizard(user)
    // 第 1 步默认预选「更新插件 / 服务端文件」
    expect(within(dialog).getByRole('button', { name: /更新插件 \/ 服务端文件/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    await pickSourceAndScan(user, dialog)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    // 纯文件跳过「选配置变更」，直接到范围与批次
    await throughScopeToReview(user, dialog)
    // 默认标题带模板源
    expect(within(dialog).getByLabelText('变更单标题')).toHaveValue('文件更新（模板源 lobby-2）')
    await submitAndAssert(user, dialog)
  })

  it('模板源步：差异行可点开预览文件内容（懒加载）', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    await pickSourceAndScan(user, dialog)

    // 差异行「预览」→ 懒加载文件内容出现（内联展开，不叠模态）
    const previews = await within(dialog).findAllByRole('button', { name: '预览' })
    await user.click(previews[0])
    expect((await within(dialog).findAllByText(/max-players/)).length).toBeGreaterThan(0)
  })

  it('纯配置路径：跳过模板源，选配置版本 → 成单', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: /更新配置文件/ }))
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    // 直接进入第 3 步（选模板源被跳过）
    await pickConfigFile(user, dialog)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    await throughScopeToReview(user, dialog)
    expect(within(dialog).getByLabelText('变更单标题')).toHaveValue('配置更新（1 个文件）')
    await submitAndAssert(user, dialog)
  })

  it('混合路径：模板源差异 + 配置版本绑成一单提交', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: /混合交付/ }))
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    await pickSourceAndScan(user, dialog)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    await pickConfigFile(user, dialog)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    await throughScopeToReview(user, dialog)
    // 简单模式概要句同时提到文件与配置载荷
    expect(within(dialog).getByText(/个文件变更与 1 项配置变更/)).toBeInTheDocument()
    await submitAndAssert(user, dialog)
  })

  it('空态任务卡带预选类型直接进向导', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('empty')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 空态引导：点「更新配置文件」任务卡
    await screen.findByText('还没有变更单')
    await user.click(screen.getByRole('button', { name: /更新配置文件/ }))

    // 向导打开且第 1 步已预选该类型
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('button', { name: /更新配置文件/ })).toHaveAttribute('aria-pressed', 'true')

    // 下一步直接进入选配置变更（纯配置跳过选模板源）
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    expect(await within(dialog).findByText(/从配置中心选择要下发的配置文件/)).toBeInTheDocument()
  })

  it('步骤校验：未选模板源 / 未扫描差异不能下一步', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    // 第 2 步：未选模板源 → 下一步禁用
    await within(dialog).findByLabelText('搜索模板源')
    expect(within(dialog).getByRole('button', { name: '下一步' })).toBeDisabled()

    // 选了模板源但未扫描 → 仍禁用
    await user.click(await within(dialog).findByRole('radio', { name: /lobby-2/ }))
    expect(within(dialog).getByRole('button', { name: '下一步' })).toBeDisabled()

    // 扫描完成 → 放行
    await user.click(within(dialog).getByRole('button', { name: /扫描差异/ }))
    await within(dialog).findByText(/新增 \d+/)
    expect(within(dialog).getByRole('button', { name: '下一步' })).toBeEnabled()
  })

  it('配置步：Shift 连选区间、全选 / 清空、预览出版本 diff', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: /更新配置文件/ }))
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    // 列表就绪（normal 场景 namespace 1 有 4 个配置文件）
    await within(dialog).findByRole('checkbox', { name: 'plugins/Essentials/config.yml' })

    // 点选第 1 行，再按住 Shift 点第 3 行 → 区间 3 项全选中
    await user.click(within(dialog).getByRole('checkbox', { name: 'plugins/Essentials/config.yml' }))
    await within(dialog).findByText('已选 1 个配置文件')
    await user.keyboard('{Shift>}')
    await user.click(within(dialog).getByRole('checkbox', { name: 'plugins/Quests/config.json' }))
    await user.keyboard('{/Shift}')
    await within(dialog).findByText('已选 3 个配置文件')
    expect(within(dialog).getByRole('checkbox', { name: 'plugins/Economy/config.yml' })).toBeChecked()

    // 全选 → 4 项；清空 → 0 项
    await user.click(within(dialog).getByRole('button', { name: '全选' }))
    await within(dialog).findByText('已选 4 个配置文件')
    await user.click(within(dialog).getByRole('button', { name: '清空' }))
    await within(dialog).findByText('已选 0 个配置文件')

    // 预览：Essentials 首层 namespace 链有 v1→v2，行级 diff 双栏出现新旧内容
    await user.click(within(dialog).getAllByRole('button', { name: '预览' })[0])
    await within(dialog).findByText(/将从 v1 更新到 v2/)
    await within(dialog).findByText('teleport-cooldown: 5')
    expect(within(dialog).getByText('teleport-cooldown: 3')).toBeInTheDocument()
    expect(within(dialog).getByText('当前版本 v1')).toBeInTheDocument()
    expect(within(dialog).getByText('目标版本 v2')).toBeInTheDocument()

    // 收起预览后 diff 消失（展开不叠模态、不打断选择流）
    await user.click(within(dialog).getByRole('button', { name: '收起' }))
    expect(within(dialog).queryByText('teleport-cooldown: 5')).not.toBeInTheDocument()
  })

  it('范围步：小区候选搜索过滤、全选 / 反选 / 清空与 Shift 连选', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 走纯配置路径最快到第 4 步
    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: /更新配置文件/ }))
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    await pickConfigFile(user, dialog)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    await within(dialog).findByText('交付范围')

    // 切「按小区」：4 个小区候选就绪
    await user.click(within(dialog).getByRole('button', { name: /按小区/ }))
    await within(dialog).findByRole('checkbox', { name: '华东大区 / area-1' })

    // 全选 → 4 个；反选 → 0 个
    await user.click(within(dialog).getByRole('button', { name: '全选' }))
    await within(dialog).findByText('已选 4 个')
    await user.click(within(dialog).getByRole('button', { name: '反选' }))
    await within(dialog).findByText('已选 0 个')

    // Shift 连选：点第 1 个再 Shift 点第 3 个 → 区间 3 个选中
    await user.click(within(dialog).getByRole('checkbox', { name: '华东大区 / area-1' }))
    await user.keyboard('{Shift>}')
    await user.click(within(dialog).getByRole('checkbox', { name: '华南大区 / area-3' }))
    await user.keyboard('{/Shift}')
    await within(dialog).findByText('已选 3 个')
    expect(within(dialog).getByRole('checkbox', { name: '华东大区 / area-2' })).toBeChecked()

    // 搜索过滤：只剩匹配项，反选仅作用于可见项（不可见的已选保留）
    await user.type(within(dialog).getByLabelText('搜索候选'), 'survival')
    expect(within(dialog).queryByRole('checkbox', { name: '华东大区 / area-1' })).not.toBeInTheDocument()
    await within(dialog).findByRole('checkbox', { name: '华南大区 / survival-1' })
    await user.click(within(dialog).getByRole('button', { name: '反选' }))
    await within(dialog).findByText('已选 4 个')

    // 清空清掉全部（含不可见已选）
    await user.click(within(dialog).getByRole('button', { name: '清空' }))
    await within(dialog).findByText('已选 0 个')
  })

  it('批次编排：推荐批次一键应用，台数总和不符即红字并禁下一步', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 纯配置路径到第 4 步，单服模式选 2 台（目标数确定为 2）
    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: /更新配置文件/ }))
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    await pickConfigFile(user, dialog)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    await within(dialog).findByText('交付范围')
    await user.click(within(dialog).getByRole('button', { name: /^单服/ }))
    await user.click(await within(dialog).findByRole('checkbox', { name: 'lobby-1' }))
    await user.click(within(dialog).getByRole('checkbox', { name: 'lobby-2' }))
    await within(dialog).findByText('预计目标 2 台（按当前范围估算）')

    // 一键推荐：2 台 → 按台数均分两批 [1,1]，每行显示实际与累计
    await user.click(within(dialog).getByRole('button', { name: /应用推荐批次/ }))
    expect(within(dialog).getByLabelText('第 1 批数量')).toHaveValue(1)
    expect(within(dialog).getByLabelText('第 2 批数量')).toHaveValue(1)
    expect(within(dialog).getByText('实际 1 台 · 累计 2 台')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '下一步' })).toBeEnabled()

    // 改首批为 5 → 合计 6 比目标多 4 → 红字 + 禁下一步
    const firstRow = within(dialog).getByLabelText('第 1 批数量')
    await user.clear(firstRow)
    await user.type(firstRow, '5')
    await within(dialog).findByText('台数合计 6 台，比目标多 4 台')
    expect(within(dialog).getByRole('button', { name: '下一步' })).toBeDisabled()

    // 删除第 2 批 → 合计 5 仍多 3 台；改回 2 → 校验通过放行
    await user.click(within(dialog).getByRole('button', { name: '删除第 2 批' }))
    await within(dialog).findByText('台数合计 5 台，比目标多 3 台')
    await user.clear(within(dialog).getByLabelText('第 1 批数量'))
    await user.type(within(dialog).getByLabelText('第 1 批数量'), '2')
    expect(within(dialog).queryByText(/台数合计/)).not.toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '下一步' })).toBeEnabled()

    // 增加批次默认补齐缺口后校验仍通过（2 台已满 → 新行给最小量 1 → 超出，先删掉验证增删闭环）
    await user.click(within(dialog).getByRole('button', { name: /增加批次/ }))
    await within(dialog).findByText('台数合计 3 台，比目标多 1 台')
    await user.click(within(dialog).getByRole('button', { name: '删除第 2 批' }))
    expect(within(dialog).getByRole('button', { name: '下一步' })).toBeEnabled()
  })

  it('第五步：简单 / 详细概要切换，详细模式出两个完整预览控件', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    // 纯配置路径到第 5 步（全量范围 + 默认推荐批次）
    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: /更新配置文件/ }))
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    await pickConfigFile(user, dialog)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))
    await throughScopeToReview(user, dialog)

    // 默认简单模式：人话概要句 + KPI 大字
    expect(within(dialog).getByRole('button', { name: '简单' })).toHaveAttribute('aria-pressed', 'true')
    expect(within(dialog).getByText(/将向全命名空间共 \d+ 台服务器分 \d+ 批推送 1 项配置变更，生效方式：热重载。/)).toBeInTheDocument()
    expect(within(dialog).getByText('批次数')).toBeInTheDocument()

    // 切详细：变更内容预览（配置清单 + 版本区间）+ 完整编排预览四分区
    await user.click(within(dialog).getByRole('button', { name: '详细' }))
    await within(dialog).findByText('配置变更清单（1 项）')
    expect(within(dialog).getByText('plugins/Essentials/config.yml')).toBeInTheDocument()
    expect(within(dialog).getByText('v1 → v2')).toBeInTheDocument()
    expect(within(dialog).getByText('目标范围')).toBeInTheDocument()
    expect(within(dialog).getByText('批次规划')).toBeInTheDocument()
    expect(within(dialog).getByText('生效方式')).toBeInTheDocument()
    expect(within(dialog).getByText('影响面汇总')).toBeInTheDocument()

    // 配置行展开差异：行级 diff 出新旧内容
    await user.click(within(dialog).getByRole('button', { name: /展开差异/ }))
    await within(dialog).findByText('teleport-cooldown: 5')
    expect(within(dialog).getByText('teleport-cooldown: 3')).toBeInTheDocument()

    // 切回简单：概要句复现、预览控件收起
    await user.click(within(dialog).getByRole('button', { name: '简单' }))
    expect(within(dialog).getByText(/将向全命名空间/)).toBeInTheDocument()
    expect(within(dialog).queryByText('配置变更清单（1 项）')).not.toBeInTheDocument()
  })

  it('模板源列表搜索即输即滤，选中态跨筛选保留', { timeout: WIZARD_TEST_TIMEOUT }, async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ChangesPage />)

    const dialog = await openWizard(user)
    await user.click(within(dialog).getByRole('button', { name: '下一步' }))

    // 初始候选包含 lobby / game 系列
    await within(dialog).findByRole('radio', { name: /lobby-1/ })
    expect(within(dialog).getByRole('radio', { name: /game-1/ })).toBeInTheDocument()

    // 按 serverId 过滤：只剩 lobby 系列
    await user.type(within(dialog).getByLabelText('搜索模板源'), 'lobby')
    expect(within(dialog).getByRole('radio', { name: /lobby-2/ })).toBeInTheDocument()
    expect(within(dialog).queryByRole('radio', { name: /game-1/ })).not.toBeInTheDocument()

    // 选中后清掉关键字换按小区名过滤，选中态保留（已选提示常显）
    await user.click(within(dialog).getByRole('radio', { name: /lobby-2/ }))
    await user.clear(within(dialog).getByLabelText('搜索模板源'))
    await user.type(within(dialog).getByLabelText('搜索模板源'), 'area-2')
    expect(within(dialog).queryByRole('radio', { name: /lobby-2/ })).not.toBeInTheDocument()
    expect(within(dialog).getByRole('radio', { name: /game-3/ })).toBeInTheDocument()
    expect(within(dialog).getByText('已选：lobby-2')).toBeInTheDocument()
  })
})
