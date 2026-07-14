// /configs 配置中心接真增强用例：五层全显与空层首次贡献、编辑保存（409 冲突）、实时校验
// 与 schema 违例阻断、diff 三描述符、回退 / 撤销（固定「不影响线上」提示）、回收站恢复 +
// 彻底删除、敏感值脱敏与占位符提示（回填后无变化被拒）、有效预览目标选择器、元数据编辑。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ConfigsPage from '../../pages/configs'
import { fetchConfigFiles, fetchConfigVersions, saveConfigVersion } from '../../api/delivery-configs'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

type User = ReturnType<typeof userEvent.setup>

/** 点击文件行打开右侧详情面板（默认作用域概览 Tab） */
async function openDetail(user: User, name: string): Promise<void> {
  const nameCell = await screen.findByText(name)
  const row = nameCell.closest('tr')
  if (!row) {
    throw new Error(`未找到文件行：${name}`)
  }
  await user.click(row)
  await screen.findByRole('tab', { name: '作用域概览' })
}

/** 查询 Essentials 文件 id（out-of-band 构造数据用） */
async function essentialsFileId(): Promise<number> {
  const files = await fetchConfigFiles({ namespaceId: 1, keyword: 'Essentials' })
  const file = files.items.find((f) => f.name === 'plugins/Essentials/config.yml')
  if (!file) {
    throw new Error('fixture 缺失 Essentials 配置文件')
  }
  return file.id
}

describe('/configs 作用域五层与空层首次贡献', () => {
  it('五层全显（无贡献层有标识），空层经实体选择完成首次贡献', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Essentials/config.yml')

    // 五层组头齐备（含无贡献层；「命名空间」等词在页面他处也出现，按 findAll 断言存在），
    // Essentials 的 bc_cluster / region 层无贡献
    for (const label of ['命名空间', 'BC 集群', '大区', '小区']) {
      expect((await screen.findAllByText(label)).length).toBeGreaterThanOrEqual(1)
    }
    expect(screen.getAllByText('本层无贡献').length).toBeGreaterThanOrEqual(2)

    // 首个「添加本层配置」= bc_cluster 层（namespace 已有链不显示添加）
    await user.click(screen.getAllByRole('button', { name: '添加本层配置' })[0])
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('添加本层配置：BC 集群')).toBeInTheDocument()

    // 结构树 Combobox 选 bc-main
    await user.click(within(dialog).getByRole('textbox', { name: '选择作用域实体' }))
    await user.click(await screen.findByRole('option', { name: 'bc-main' }))
    await user.click(within(dialog).getByRole('button', { name: '开始编辑' }))

    // 空白编辑器（首版无基线）：填内容保存
    const editor = await screen.findByRole('dialog')
    expect(within(editor).getByText('bc_cluster / bc-main')).toBeInTheDocument()
    await user.type(within(editor).getByLabelText('内容'), 'economy-enabled: false')
    await user.click(within(editor).getByRole('button', { name: '保存新版本' }))

    // 保存后 bc_cluster 层出现 bc-main 贡献行
    expect(await screen.findByText('bc-main')).toBeInTheDocument()
  })
})

describe('/configs 编辑保存', () => {
  it('基线过期保存返回 409 并内联冲突提示', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Essentials/config.yml')

    // 打开 namespace 层编辑器（basedOn = 当前 head）
    await user.click(screen.getAllByRole('button', { name: '编辑本层' })[0])
    const editor = await screen.findByRole('dialog')
    await within(editor).findByLabelText('内容')

    // out-of-band 推进 head，使编辑器基线过期
    const fileId = await essentialsFileId()
    const versions = await fetchConfigVersions(fileId, { scopeLevel: 'namespace', scopeRefId: 1, page: 1, pageSize: 1 })
    await saveConfigVersion(fileId, {
      scopeLevel: 'namespace',
      scopeRefId: 1,
      content: 'teleport-cooldown: 8\nmotd: 并发修改',
      basedOnVersionId: versions.items[0].versionId,
    })

    await user.click(within(editor).getByRole('button', { name: '保存新版本' }))
    expect(await screen.findByText('基线已过期，请返回重新加载后再保存')).toBeInTheDocument()
  })

  it('实时校验逐条展示 schema 违例，保存被 400 阻断', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Essentials/config.yml')

    await user.click(screen.getAllByRole('button', { name: '编辑本层' })[0])
    const editor = await screen.findByRole('dialog')
    const content = within(editor).getByLabelText('内容')
    await user.clear(content)
    await user.type(content, 'teleport-cooldown: -5')

    // debounce 500ms 后实时校验逐条 {path,message} 内联展示
    expect(await screen.findByText('校验不通过')).toBeInTheDocument()
    expect(await screen.findByText('teleport-cooldown：不得小于 0')).toBeInTheDocument()

    // 保存被 CONFIG_SCHEMA_VIOLATION 阻断（错误含逐条路径与原因）
    await user.click(within(editor).getByRole('button', { name: '保存新版本' }))
    expect(await screen.findByText(/schema 校验不通过：teleport-cooldown 不得小于 0/)).toBeInTheDocument()
  })
})

describe('/configs diff 三描述符', () => {
  it('层 head vs 历史版本、有效结果 vs 历史版本均可对比', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Essentials/config.yml')
    await user.click(screen.getByRole('tab', { name: '差异对比' }))

    // 左侧：层 head（namespace）
    await user.click(await screen.findByRole('combobox', { name: '左侧选择层' }))
    await user.click(await screen.findByRole('option', { name: 'namespace / prod' }))

    // 右侧：历史版本（namespace 链 v1）
    await user.click(screen.getByRole('combobox', { name: '右侧描述类型' }))
    await user.click(await screen.findByRole('option', { name: '历史版本' }))
    await user.click(await screen.findByRole('combobox', { name: '右侧选择层' }))
    await user.click(await screen.findByRole('option', { name: 'namespace / prod' }))
    await user.click(await screen.findByRole('combobox', { name: '右侧选择版本' }))
    await user.click(await screen.findByRole('option', { name: /^v1 · / }))

    await user.click(screen.getByRole('button', { name: '对比' }))
    // namespace head=v2（cooldown 3）vs v1（cooldown 5）
    expect(await screen.findByText('变更')).toBeInTheDocument()
    expect(await screen.findByText(/teleport-cooldown: 3 → 5/)).toBeInTheDocument()

    // 左侧改「有效结果」（仅命名空间基线目标），与 v1 对比同样成立
    await user.click(screen.getByRole('combobox', { name: '左侧描述类型' }))
    await user.click(await screen.findByRole('option', { name: '有效结果' }))
    await user.click(screen.getByRole('button', { name: '对比' }))
    expect(await screen.findByText(/teleport-cooldown: 3 → 5/)).toBeInTheDocument()
  })
})

describe('/configs 版本链回退与撤销层贡献', () => {
  it('回退历史版本生成新版本，确认框固定提示不影响线上', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Essentials/config.yml')
    await user.click(screen.getByRole('tab', { name: '版本链' }))

    // 默认链 = namespace，行出现 v2 / v1
    const v1Cell = await screen.findByText('v1')
    const row = v1Cell.closest('tr')
    if (!row) {
      throw new Error('未找到 v1 行')
    }
    await user.click(within(row).getByRole('button', { name: '回退到此版本' }))

    // 固定提示 + 原因必填
    expect(await screen.findByText('此操作不影响线上，生效需走变更单')).toBeInTheDocument()
    await user.type(screen.getByLabelText('原因'), '回退演练')
    await user.click(screen.getByRole('button', { name: '确认回退' }))

    // 链内追加 v3
    expect(await screen.findByText('v3')).toBeInTheDocument()
  })

  it('撤销层贡献生成 removal 版本，该层标「已撤销贡献」', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Essentials/config.yml')

    const zoneCell = await screen.findByText('area-1')
    const row = zoneCell.closest('div')
    if (!row) {
      throw new Error('未找到 zone 贡献行')
    }
    await user.click(within(row).getByRole('button', { name: '撤销本层贡献' }))

    expect(await screen.findByText('此操作不影响线上，生效需走变更单')).toBeInTheDocument()
    await user.type(screen.getByLabelText('原因'), '一区配置回收')
    await user.click(screen.getByRole('button', { name: '确认撤销' }))

    expect(await screen.findByText('已撤销贡献')).toBeInTheDocument()
  })
})

describe('/configs 回收站', () => {
  it('删除 → 回收站恢复；彻底删除后从回收站消失', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    // 删除 Essentials（移入回收站）
    const nameCell = await screen.findByText('plugins/Essentials/config.yml')
    const listRow = nameCell.closest('tr')
    if (!listRow) {
      throw new Error('未找到文件行')
    }
    await user.click(within(listRow).getByRole('button', { name: '删除' }))
    await user.click(await screen.findByRole('button', { name: '确认删除' }))
    await waitFor(() => {
      expect(screen.queryByText('plugins/Essentials/config.yml')).not.toBeInTheDocument()
    })

    // 回收站：恢复 Essentials
    await user.click(screen.getByRole('button', { name: '回收站' }))
    const trashedCell = await screen.findByText('plugins/Essentials/config.yml')
    const trashedRow = trashedCell.closest('tr')
    if (!trashedRow) {
      throw new Error('未找到回收站行')
    }
    await user.click(within(trashedRow).getByRole('button', { name: '恢复' }))
    await waitFor(() => {
      expect(screen.queryByText('plugins/Essentials/config.yml')).not.toBeInTheDocument()
    })

    // 彻底删除 OldShop（原因必填），回收站清空
    const oldShopCell = await screen.findByText('plugins/OldShop/config.yml')
    const oldShopRow = oldShopCell.closest('tr')
    if (!oldShopRow) {
      throw new Error('未找到 OldShop 行')
    }
    await user.click(within(oldShopRow).getByRole('button', { name: '彻底删除' }))
    await user.type(await screen.findByLabelText('原因'), '插件已下线留档确认')
    await user.click(screen.getByRole('button', { name: '确认彻底删除' }))
    expect(await screen.findByText('回收站为空')).toBeInTheDocument()

    // 返回列表：恢复的 Essentials 回到常规列表
    await user.click(screen.getByRole('button', { name: '返回列表' }))
    expect(await screen.findByText('plugins/Essentials/config.yml')).toBeInTheDocument()
  })
})

describe('/configs 敏感值体验', () => {
  it('有效预览脱敏；编辑器显示占位符说明；占位符回填后无变化保存被拒', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Economy/config.yml')

    // 有效预览（namespace 基线）：嵌套敏感值已脱敏、不见明文
    await user.click(screen.getByRole('tab', { name: '有效配置' }))
    expect(await screen.findByText(/__BEACON_MASKED__/)).toBeInTheDocument()
    expect(screen.queryByText(/prod-secret-233/)).not.toBeInTheDocument()

    // 编辑器：占位符说明条 + 初始内容含占位符
    await user.click(screen.getByRole('tab', { name: '作用域概览' }))
    await user.click(screen.getAllByRole('button', { name: '编辑本层' })[0])
    const editor = await screen.findByRole('dialog')
    expect(within(editor).getByText(/不可再查看明文/)).toBeInTheDocument()
    const content = within(editor).getByLabelText<HTMLTextAreaElement>('内容')
    expect(content.value).toContain('__BEACON_MASKED__')

    // 保持占位符原样保存：回填旧明文后与 head 相同 → CONFIG_NO_CHANGE 拒绝（证明回填生效）
    await user.click(within(editor).getByRole('button', { name: '保存新版本' }))
    expect(await screen.findByText(/内容与当前 head 相同/)).toBeInTheDocument()
  })
})

describe('/configs 有效预览目标选择器', () => {
  it('按服务器目标（服务端搜索）查询，展示删键执行层与图例', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Essentials/config.yml')
    await user.click(screen.getByRole('tab', { name: '有效配置' }))

    // 目标类型切「按服务器」→ 服务端搜索选 lobby-1
    await user.click(await screen.findByRole('combobox', { name: '目标类型' }))
    await user.click(await screen.findByRole('option', { name: '按服务器' }))
    await user.type(screen.getByLabelText('搜索 serverId'), 'lobby')
    await user.click(await screen.findByRole('option', { name: 'lobby-1' }))
    await user.click(screen.getByRole('button', { name: '查询' }))

    // 应用目标标签 + 来源图例 + server 层 null 删键（motd）与执行层
    expect(await screen.findByText('当前目标：服务器 / lobby-1')).toBeInTheDocument()
    expect(screen.getByText('来源图例')).toBeInTheDocument()
    expect(await screen.findByText('motd')).toBeInTheDocument()
    expect(screen.getByText('删除于')).toBeInTheDocument()
  })
})

describe('/configs 文件元数据编辑', () => {
  it('描述 PATCH 即改；敏感路径变更需原因二次确认后生效', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)
    await openDetail(user, 'plugins/Quests/config.json')

    // 改描述：保存直接生效
    await user.click(screen.getByRole('button', { name: '编辑元数据' }))
    let dialog = await screen.findByRole('dialog')
    const desc = within(dialog).getByLabelText('描述')
    await user.clear(desc)
    await user.type(desc, '任务配置新说明')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))
    expect(await screen.findByText('任务配置新说明')).toBeInTheDocument()

    // 改敏感路径：出变更提示 → 保存进入原因确认 → 确认后头部出现敏感键徽标
    await user.click(screen.getByRole('button', { name: '编辑元数据' }))
    dialog = await screen.findByRole('dialog')
    await user.type(
      within(dialog).getByLabelText('敏感键路径（每行一个，精确路径，如 database.password）'),
      'rewards.daily',
    )
    expect(within(dialog).getByText('敏感路径有变更：保存时需二次确认并填写原因')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('确认修改敏感键路径')).toBeInTheDocument()
    await user.type(screen.getByLabelText('原因'), '接入奖励数值保密要求')
    await user.click(screen.getByRole('button', { name: '确认修改' }))

    expect(await screen.findByText('敏感键 1')).toBeInTheDocument()
  })
})
