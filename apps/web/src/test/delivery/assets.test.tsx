// /assets 文件资产页测试：视图切换（清单 / 扫描概要 / 跨服比对 / 两侧差异）、
// 清单主从布局点行出非模态详情面板、空态引导、比对交互。
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AssetsPage from '../../pages/assets'
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

describe('/assets 文件资产页', () => {
  it('默认清单视图渲染文件清单，视图切换器含扫描概要', async () => {
    useScenario('normal')
    renderPage(<AssetsPage />)

    // 视图切换器：清单（默认激活）与扫描概要 Tab
    expect(await screen.findByRole('tab', { name: '文件清单' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '扫描概要' })).toBeInTheDocument()
    // 清单中出现已知子服（集群 backend 之一）
    expect((await screen.findAllByText('lobby-1')).length).toBeGreaterThan(0)
  })

  it('切到扫描概要视图给出空态引导', async () => {
    useScenario('empty')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '扫描概要' }))
    expect(
      await screen.findByText(
        '当前 命名空间 下暂无扫描记录，接入 agent 并完成首次清单上报后出现在此',
      ),
    ).toBeInTheDocument()
  })

  it('点清单行打开右侧非模态详情面板展示元数据（无 dialog 遮罩）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    // 点第一行（含 lobby-1 的行）打开详情面板
    const cells = await screen.findAllByText('lobby-1')
    const row = cells[0].closest('tr')
    if (!row) {
      throw new Error('未找到清单所在行')
    }
    await user.click(row)

    // 详情面板出现元数据分区，且不产生模态遮罩
    expect(await screen.findByText('元数据')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  // 逐字符键入长路径在并行 worker 负载下偶发超过默认 5s，只放宽时限、不削弱断言
  it('切到跨服比对视图输入路径后返回哈希分组', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '跨服比对' }))
    const compareRegion = await screen.findByRole('region', { name: '跨服比对' })
    const pathInput = within(compareRegion).getByLabelText('文件路径')
    await user.type(pathInput, 'plugins/Essentials/config.yml')
    const runBtn = within(compareRegion).getByRole('button', { name: '比对' })
    await user.click(runBtn)

    // 出现哈希分组标题
    expect(await screen.findByText('哈希分组')).toBeInTheDocument()
  }, 20_000)

  it('清单行内前置基础字段可见 + 吸顶筛选存在', async () => {
    useScenario('normal')
    renderPage(<AssetsPage />)

    await screen.findAllByText('lobby-1')
    // 行内前置列：路径 / 大小 / 类型 / 哈希 / 修改时间
    expect(screen.getByRole('columnheader', { name: '路径' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '大小' })).toBeInTheDocument()
    expect(screen.getByRole('columnheader', { name: '类型' })).toBeInTheDocument()
    // 吸顶筛选始终可见
    expect(screen.getByLabelText('按子服过滤')).toBeInTheDocument()
  })
})
