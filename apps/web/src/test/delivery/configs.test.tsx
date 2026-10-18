// /configs 配置中心页测试：常规列表渲染、空态引导、新建写闭环、进入详情切「有效配置」看合并内容。
// 命名空间范围自本页移除选择器后取自页眉观测范围选择器（真源 features/env/observation-scope），
// 因此用例统一在 beforeEach 里把范围固定到 mock 的 prod 命名空间（id=1）。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import ConfigsPage from '../../pages/configs'
import EnvFilter from '../../shell/env-filter'
import { getObservationScopeSelection, setObservationScope } from '../../features/env/observation-scope'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
beforeEach(() => {
  // mock 数据仓的配置文件都在 prod 命名空间（id=1）；env 1（生产）映射到该 namespace
  setObservationScope({ kind: 'env', envId: 1, namespaceId: 1 })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

describe('/configs 配置中心页', () => {
  it('常规态渲染配置文件列表', async () => {
    useScenario('normal')
    renderPage(<ConfigsPage />)

    // 页头：大标题 + 一行解释（原 warn 提示条的文案已并入页头说明）
    expect(await screen.findByRole('heading', { level: 1, name: '配置中心' })).toBeInTheDocument()
    expect(
      screen.getByText(/按作用域管理配置文件版本与覆盖关系/),
    ).toBeInTheDocument()
    // 「前往变更单」去向入口从原 warn 提示条搬到页头操作区
    expect(screen.getByRole('link', { name: '前往变更单' })).toHaveAttribute('href', '/changes')
    // 出现已知配置文件
    expect(await screen.findByText('plugins/Essentials/config.yml')).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '格式' })).toHaveClass('sticky', 'top-0', 'z-10')
    expect(document.querySelector('[data-slot="list-card-scroll"]')).toHaveClass('md:min-h-0', 'md:overflow-y-auto')
  }, 20_000)

  // 空态引导只在「命名空间存在但一个配置文件都没有」时出现（empty 场景连命名空间都没有，
  // 那时根本没有可创建的主体，所以用常规场景把列表接口打成空来验证引导本身）
  it('空态给出新建引导文案', async () => {
    useScenario('normal')
    server.use(http.get('*/admin/v2/config-files', () => HttpResponse.json({ items: [], total: 0 })))
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    // 空态引导：标题 + 直接打开新建弹窗的动作（不再是「暂无数据」式空表格）
    expect(await screen.findByText('还没有配置文件')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '新建第一个配置文件' }))
    expect(await screen.findByRole('dialog')).toBeInTheDocument()
  })

  it('新建配置文件后列表出现该名（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    // 等列表就绪后点「新建配置文件」
    await screen.findByText('plugins/Essentials/config.yml')
    await user.click(screen.getByRole('button', { name: '新建配置文件' }))

    // 格式下拉显示当前预选值，并支持键入筛选后选择
    const dialog = await screen.findByRole('dialog')
    const format = within(dialog).getByRole('textbox', { name: '格式' })
    expect(format).toHaveValue('yaml')
    await user.click(format)
    await user.type(format, 'properties')
    await user.click(await screen.findByRole('option', { name: 'properties' }))
    expect(format).toHaveValue('properties')

    // 弹窗内填名称并创建
    const nameInput = within(dialog).getByLabelText('文件名')
    await user.type(nameInput, 'plugins/NewPlugin/config.properties')
    expect(within(dialog).getByRole('button', { name: '创建' })).toBeEnabled()
    await user.keyboard('{Enter}')

    // 列表中出现新文件名且格式映射正确
    const createdName = await screen.findByText('plugins/NewPlugin/config.properties')
    const createdRow = createdName.closest('tr')
    if (!createdRow) {
      throw new Error('未找到新配置文件所在行')
    }
    expect(within(createdRow).getByText('properties')).toBeInTheDocument()
  }, 20_000)

  it('新建配置提交中禁止关闭，失败后保留内联错误', async () => {
    useScenario('normal')
    server.use(
      http.post('*/admin/v2/config-files', async () => {
        await delay(400)
        return HttpResponse.json({ code: 'temporary_failure', message: '模拟配置创建失败' }, { status: 503 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    await screen.findByText('plugins/Essentials/config.yml')
    await user.click(screen.getByRole('button', { name: '新建配置文件' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('文件名'), 'plugins/NewPlugin/config.yml')
    const submit = within(dialog).getByRole('button', { name: '创建' })
    await user.click(submit)
    await waitFor(() => expect(submit).toBeDisabled())
    expect(within(dialog).getByRole('button', { name: '取消' })).toBeDisabled()

    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    const overlay = document.querySelector('[data-slot="dialog-overlay"]')
    if (!overlay) {
      throw new Error('未找到弹窗遮罩')
    }
    await user.click(overlay)
    expect(screen.getByRole('dialog')).toBeInTheDocument()

    expect(await within(dialog).findByText('模拟配置创建失败')).toBeInTheDocument()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('配置移入回收站失败时保留确认框并展示错误', async () => {
    useScenario('normal')
    server.use(
      http.delete('*/admin/v2/config-files/:id', async () => {
        await delay(400)
        return HttpResponse.json({ code: 'temporary_failure', message: '模拟删除失败' }, { status: 503 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    const nameCell = await screen.findByText('plugins/Essentials/config.yml')
    const row = nameCell.closest('tr')
    if (!row) {
      throw new Error('未找到配置文件所在行')
    }
    await user.click(within(row).getByRole('button', { name: '删除' }))
    const dialog = await screen.findByRole('alertdialog')
    const buttons = within(dialog).getAllByRole('button')
    const confirm = buttons[buttons.length - 1]
    await user.click(confirm)
    await waitFor(() => expect(confirm).toBeDisabled())
    expect(buttons[0]).toBeDisabled()
    await user.keyboard('{Escape}')
    expect(screen.getByRole('alertdialog')).toBeInTheDocument()
    expect(await within(dialog).findByText('模拟删除失败')).toBeInTheDocument()
  })

  it('点行打开右侧非模态详情面板并切「有效配置」看到合并内容', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    // 点第一个文件所在行打开详情面板（非模态，不产生 dialog 遮罩）
    const nameCell = await screen.findByText('plugins/Essentials/config.yml')
    const row = nameCell.closest('tr')
    if (!row) {
      throw new Error('未找到配置文件所在行')
    }
    await user.click(row)

    // 详情为固定层抽屉（非 dialog）；作用域概览 Tab 可见
    expect(await screen.findByRole('tab', { name: '作用域概览' })).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()

    // 切到「有效配置」Tab
    await user.click(await screen.findByRole('tab', { name: '有效配置' }))

    // 合并内容区出现（默认按 命名空间 合并，含 economy-enabled 键）
    await waitFor(() => {
      expect(screen.getAllByText(/economy-enabled/).length).toBeGreaterThan(0)
    })
  })

  it('行内前置基础字段可见（格式 / 贡献层数 / 有效哈希）', async () => {
    useScenario('normal')
    renderPage(<ConfigsPage />)

    // 列表就绪后，表头出现前置字段列（格式 / 贡献层数 / 有效哈希 / 更新时间）
    await screen.findByText('plugins/Essentials/config.yml')
    expect(screen.getByRole('columnheader', { name: '格式' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '贡献层数' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '有效哈希' })).toBeInTheDocument()
  })

  it('keyword 搜索过滤配置文件', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    await screen.findByText('plugins/Essentials/config.yml')
    const keywordInput = screen.getByLabelText('搜索文件名')
    await user.type(keywordInput, 'Economy')

    // 过滤后 Economy 在、Essentials 不在
    expect(await screen.findByText('plugins/Economy/config.yml')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('plugins/Essentials/config.yml')).not.toBeInTheDocument()
    })
  })

  // 交付域默认命名空间：页眉停在「全部命名空间」时页面自动补第一个可用命名空间并写回页眉范围，
  // 用户进页面就能用，不再被「请先选命名空间」挡住（默认选中而不是引导）
  it('页眉未选命名空间时自动选中第一个可用命名空间，列表与写操作即刻可用', async () => {
    useScenario('normal')
    setObservationScope({ kind: 'all' })
    renderPage(<ConfigsPage />)

    // 自动取到 mock 的 prod 命名空间（env 1 的首个 namespace，id=1）→ 列表直接可用
    expect(await screen.findByText('plugins/Essentials/config.yml')).toBeInTheDocument()
    expect(screen.queryByText('请先在页眉左侧选择命名空间')).not.toBeInTheDocument()
    // 写操作不再被禁用
    expect(screen.getByRole('button', { name: '新建配置文件' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '回收站' })).toBeEnabled()
    // 默认值写回全局观测范围：页眉选择器读同一份快照，不会「页眉全部 / 页面第一个命名空间」错位
    await waitFor(() => {
      expect(getObservationScopeSelection()).toMatchObject({ namespaceId: 1 })
    })
  })

  // 页眉与页面必须显示同一个命名空间：默认值是写回全局观测范围的，页眉选择器读同一份快照，
  // 因此不会出现「页眉显示全部、页面却按第一个命名空间取数」的错位
  it('自动补默认命名空间后页眉选择器同步显示该命名空间', async () => {
    useScenario('normal')
    setObservationScope({ kind: 'all' })
    renderPage(
      <>
        <EnvFilter />
        <ConfigsPage />
      </>,
    )

    // 页眉命名空间选择器（初始为「全部命名空间」）在默认值生效后变为 mock 的 prod 命名空间
    const namespaceTrigger = await screen.findByRole('button', { name: '观测命名空间' })
    await waitFor(() => {
      expect(within(namespaceTrigger).getByText('prod')).toBeInTheDocument()
    })
    // 页面同时按该命名空间取数（同一份范围的两种呈现）
    expect(await screen.findByText('plugins/Essentials/config.yml')).toBeInTheDocument()
  })

  // 范围解析中不得闪出「请先选命名空间」：加载态给骨架占位，引导只在确实没有命名空间时出现
  it('env 选项加载中显示骨架而不是命名空间引导', async () => {
    useScenario('normal')
    setObservationScope({ kind: 'all' })
    // 拖住 env 选项响应，让「解析中」这一帧可被断言
    server.use(
      http.get('*/admin/v2/envs', async () => {
        await delay(300)
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    renderPage(<ConfigsPage />)

    // 解析中：骨架在、引导不在（用户不会先被告知「去页眉选命名空间」再看到内容）
    expect(document.querySelector('[data-slot="delivery-scope-pending"]')).not.toBeNull()
    expect(screen.queryByText('请先在页眉左侧选择命名空间')).not.toBeInTheDocument()
    // 加载结束仍无可用命名空间 → 才落到引导
    expect(await screen.findByText('请先在页眉左侧选择命名空间')).toBeInTheDocument()
  })

  // 兜底：确实没有任何可用命名空间（env 列表为空）时仍引导去页眉选择，并禁写、不伪造数据
  it('无可用命名空间时给出引导并禁用写操作', async () => {
    useScenario('normal')
    setObservationScope({ kind: 'all' })
    // 没有任何 env / namespace 映射：默认值无从补齐，页面必须回到引导而非硬猜
    server.use(http.get('*/admin/v2/envs', () => HttpResponse.json({ items: [], total: 0 })))
    renderPage(<ConfigsPage />)

    // 内容区引导（不再有页面级命名空间选择器）
    expect(await screen.findByText('请先在页眉左侧选择命名空间')).toBeInTheDocument()
    expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
    // 写操作入口禁用：新建 / 回收站（回收站含恢复 / 彻底删除）
    expect(screen.getByRole('button', { name: '新建配置文件' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '回收站' })).toBeDisabled()
    // 不渲染列表（列表按命名空间取数，不伪造「全部命名空间」的数据）
    expect(screen.queryByText('plugins/Essentials/config.yml')).not.toBeInTheDocument()
    // 只读去向入口仍可用
    expect(screen.getByRole('link', { name: '前往变更单' })).toBeEnabled()
  })
})
