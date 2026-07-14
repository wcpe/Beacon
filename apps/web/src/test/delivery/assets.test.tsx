// /assets 文件资产页测试：视图切换（清单 / 扫描概要 / 跨服比对 / 两侧差异）、
// 清单主从布局点行出非模态详情面板、空态引导、比对交互。
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
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

  // FR-164：敏感路径规则编辑（非结构性小面板）——打开弹窗、载入默认规则、编辑保存
  it('敏感路径规则编辑：载入默认规则并保存', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('button', { name: '敏感路径规则' }))
    const dialog = await screen.findByRole('dialog')
    // 载入默认清单（含 agent 身份目录规则）
    const textarea = await within(dialog).findByLabelText(
      '规则清单（每行一个 glob，如 **/*.pem、plugins/Beacon/**）',
    )
    expect((textarea as HTMLTextAreaElement).value).toContain('plugins/Beacon/**')
    // 追加一条并保存 → 成功提示
    await user.type(textarea, '\nplugins/Custom/**')
    await user.click(within(dialog).getByRole('button', { name: '保存规则' }))
    expect(await within(dialog).findByText('已保存敏感路径规则')).toBeInTheDocument()
  }, 20_000)

  // FR-164：两侧 diff 命中敏感规则 → 403 后填原因带原因重试（前端 403→原因输入→重试逻辑）
  it('两侧 diff 敏感命中 403 后填原因重试', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    // 覆盖 diff 端点：无 reason → 403 asset_sensitive_path；带 reason → 一致，锁定前端交互不依赖 mock 清单命中
    server.use(
      http.post('/admin/v2/assets/diff', async ({ request }) => {
        const body = (await request.json()) as { reason?: string }
        if (!body.reason) {
          return HttpResponse.json(
            { code: 'asset_sensitive_path', message: '命中敏感路径规则，diff 必须填写原因', sensitive: true, traceId: 't' },
            { status: 403 },
          )
        }
        return HttpResponse.json({ identical: true })
      }),
    )
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '两侧差异' }))
    const region = await screen.findByRole('region', { name: '两侧差异' })
    await user.type(within(region).getByLabelText('左侧子服'), 'lobby-1')
    await user.type(within(region).getByLabelText('右侧子服'), 'lobby-2')
    await user.type(within(region).getByLabelText('文件路径'), 'plugins/Beacon/config.yml')
    await user.click(within(region).getByRole('button', { name: '比对差异' }))

    // 403 → 原因输入出现
    const reasonInput = await within(region).findByLabelText('diff 原因')
    await user.type(reasonInput, '核对经济配置差异')
    await user.click(within(region).getByRole('button', { name: '带原因比对' }))
    // 带原因重试 → identical 提示
    expect(await within(region).findByText('两侧内容一致（哈希相同）')).toBeInTheDocument()
  }, 20_000)
})
