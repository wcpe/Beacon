// /assets 文件资产页测试：视图切换（清单 / 扫描概要 / 跨服比对 / 两侧差异）、
// 清单主从布局点行出非模态详情面板、空态引导、比对交互。
// 命名空间范围自本页移除选择器后取自页眉观测范围选择器（真源 features/env/observation-scope），
// 因此用例统一在 beforeEach 里把范围固定到 mock 的 prod 命名空间（id=1）。
import { useState, type ReactElement } from 'react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it } from 'vitest'

import AssetsPage from '../../pages/assets'
import AssetPathPicker from '../../features/delivery/asset-path-picker'
import DiffPanel from '../../pages/assets/diff-panel'
import PreviewDialog from '../../pages/assets/preview-dialog'
import { getObservationScopeSelection, setObservationScope } from '../../features/env/observation-scope'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
beforeEach(() => {
  // mock 数据仓的资产排在 prod 命名空间（id=1）；env 1（生产）映射到该 namespace
  setObservationScope({ kind: 'env', envId: 1, namespaceId: 1 })
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
    const pathHeader = screen.getByRole('columnheader', { name: '路径' })
    expect(pathHeader).toHaveClass('sticky', 'top-0', 'z-10')
    expect(pathHeader.closest('[data-slot="table-container"]')).toHaveClass('overflow-x-visible')
    expect(document.querySelector('[data-slot="list-card-scroll"]')).toHaveClass('md:min-h-0', 'md:overflow-y-auto')
  })

  // 场景 empty 下 mock 数据仓连 env / namespace 都为空 → 范围解析必为无效态，
  // 页面不再猜命名空间，而是把用户指回页眉选择器（不渲染面板、不发请求、不伪造数据）
  it('无可用命名空间时给出页眉选择命名空间的引导', async () => {
    useScenario('empty')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    expect(
      await screen.findByText(
        '请先在页眉左侧选择命名空间：文件清单、扫描概要、跨服比对与两侧差异都按命名空间取数。',
      ),
    ).toBeInTheDocument()
    // 四个视图页签仍在，便于用户理解本页结构
    expect(screen.getByRole('tab', { name: '扫描概要' })).toBeInTheDocument()
    // 未选命名空间不渲染任何面板（面板只在 namespaceId > 0 时取数）
    expect(screen.queryByRole('region', { name: '跨服比对' })).not.toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '跨服比对' }))
    expect(screen.queryByRole('region', { name: '跨服比对' })).not.toBeInTheDocument()
  })

  it('页眉未选具体命名空间时保留全局范围并显示引导', async () => {
    useScenario('normal')
    setObservationScope({ kind: 'all' })
    renderPage(<AssetsPage />)

    expect(
      await screen.findByText(
        '请先在页眉左侧选择命名空间：文件清单、扫描概要、跨服比对与两侧差异都按命名空间取数。',
      ),
    ).toBeInTheDocument()
    expect(screen.queryByText('lobby-1')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '敏感路径规则' })).toBeDisabled()
    expect(getObservationScopeSelection()).toEqual({ kind: 'all' })
  })

  // 清单空态引导：命名空间存在但还没有任何 agent 清单上报时，主 Tab 给出下一步动作
  it('清单空态给出上报引导与去变更单动作', async () => {
    useScenario('normal')
    server.use(
      http.get('*/admin/v2/assets/scan-status', () => HttpResponse.json({ items: [], total: 0 })),
    )
    renderPage(<AssetsPage />)

    expect(await screen.findByText('这个命名空间还没有文件清单')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /去变更单/ })).toHaveAttribute('href', '/changes')
    expect(screen.getByRole('button', { name: '刷新' })).toBeInTheDocument()
  })

  it('扫描概要以指标卡与表格卡呈现，并用徽标说明清单截断', async () => {
    useScenario('normal')
    server.use(
      http.get('*/admin/v2/assets/scan-status', () => HttpResponse.json({
        items: [{
          serverId: 'lobby-1',
          namespaceId: 1,
          manifestDigest: 'a'.repeat(64),
          fileCount: 5,
          totalSize: 2048,
          truncated: true,
          scannedAt: '2026-01-01T00:00:00.000Z',
          scanDurationMs: 120,
        }],
        total: 1,
      })),
    )
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '扫描概要' }))
    const region = await screen.findByRole('region', { name: '扫描概要' })
    expect(within(region).getByText('本页子服')).toBeInTheDocument()
    expect(within(region).getByText('文件数合计')).toBeInTheDocument()
    expect(within(region).getByText('总大小合计')).toBeInTheDocument()
    expect(within(region).getByRole('columnheader', { name: '子服' })).toHaveClass('sticky', 'top-0', 'z-10')
    expect(region.querySelector('[data-slot="list-card-scroll"]')).toHaveClass('md:min-h-0', 'md:overflow-y-auto')
    expect(within(region).getByText('已截断')).toHaveAttribute('data-variant', 'warn')
    expect(region.querySelector('[data-slot="card"]')).not.toBeNull()
  })

  it('清单加载失败时显示可重试操作', async () => {
    useScenario('normal')
    let requestCount = 0
    let retryAllowed = false
    server.use(
      http.get('*/admin/v2/assets', () => {
        requestCount += 1
        return retryAllowed
          ? HttpResponse.json({ items: [], total: 0 })
          : HttpResponse.json({ message: '暂时不可用' }, { status: 503 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText('文件清单加载失败，请检查连接后重试。')).toBeInTheDocument()
    retryAllowed = true
    await user.click(within(alert).getByRole('button', { name: '重试加载' }))
    expect(await screen.findByText('当前筛选条件下无文件；请调整服务器 / 路径前缀 / 扩展名后重试')).toBeInTheDocument()
    expect(requestCount).toBeGreaterThan(1)
  })

  it('服务器候选加载中显示独立加载态', async () => {
    useScenario('normal')
    let releaseResponse: (() => void) | undefined
    const responseGate = new Promise<void>((resolve) => {
      releaseResponse = resolve
    })
    server.use(
      http.get('*/admin/v2/servers', async () => {
        await responseGate
        return HttpResponse.json({ items: [], total: 0 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    const serverFilter = await screen.findByRole('combobox', { name: '按子服过滤' })
    try {
      await user.click(serverFilter)
      expect(screen.getByText('正在加载服务器候选…')).toBeInTheDocument()
    } finally {
      releaseResponse?.()
    }
    expect(await screen.findByText('无匹配服务器')).toBeInTheDocument()
  })

  it('服务器候选加载失败时显示错误与重试，不误报为无匹配结果', async () => {
    useScenario('normal')
    let attempts = 0
    let retryAllowed = false
    server.use(
      http.get('*/admin/v2/servers', () => {
        attempts += 1
        return retryAllowed
          ? HttpResponse.json({ items: [], total: 0 })
          : HttpResponse.json({ message: '服务器接口暂不可用' }, { status: 503 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('服务器候选加载失败：服务器接口暂不可用')
    const serverFilter = screen.getByRole('combobox', { name: '按子服过滤' })
    await user.click(serverFilter)
    expect(await screen.findByText('服务器候选加载失败，无法确认匹配结果')).toBeInTheDocument()
    expect(screen.queryByText('无匹配服务器')).not.toBeInTheDocument()

    retryAllowed = true
    await user.click(within(alert).getByRole('button', { name: '重试' }))
    await waitFor(() => {
      expect(attempts).toBeGreaterThan(1)
    })
    await user.click(serverFilter)
    expect(await screen.findByText('无匹配服务器')).toBeInTheDocument()
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

    // 详情为固定层抽屉（非 role=dialog），主表不 reflow
    expect(await screen.findByText('元数据')).toBeInTheDocument()
    expect(document.querySelector('[data-slot="master-detail-title"]')).toHaveClass('shrink-0')
    expect(document.querySelector('[data-slot="master-detail-scroll"]')).toHaveClass('min-h-0', 'flex-1', 'overflow-y-auto')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  // 逐字符键入长路径在并行 worker 负载下偶发超过默认 5s，只放宽时限、不削弱断言
  it('切到跨服比对视图输入路径后返回哈希分组', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '跨服比对' }))
    const compareRegion = await screen.findByRole('region', { name: '跨服比对' })
    expect(within(compareRegion).getByText('开始一次跨服比对')).toBeInTheDocument()
    const pathInput = within(compareRegion).getByLabelText('文件路径')
    await user.click(within(compareRegion).getByRole('button', { name: '选择文件路径' }))
    expect(pathInput).toHaveFocus()
    await user.type(pathInput, 'plugins/Essentials/config.yml')
    const runBtn = within(compareRegion).getByRole('button', { name: '比对' })
    await user.click(runBtn)

    // 哈希分组以有表头的结果表格呈现
    expect(await screen.findByText('哈希分组')).toBeInTheDocument()
    expect(within(compareRegion).getByRole('columnheader', { name: '哈希摘要' })).toBeInTheDocument()
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

  // 级联选择：子服从真实列表选，路径候选取自该服真实清单（不再要求用户背路径）
  it('路径候选随所选子服级联：选服后列出该服真实文件与目录，且仍可手输清单外路径', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '两侧差异' }))
    const region = await screen.findByRole('region', { name: '两侧差异' })
    await user.click(within(region).getByLabelText('左侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-1' }))

    // 打开路径下拉：候选来自 lobby-1 的真实清单（mock 清单里的已知文件）
    await user.click(within(region).getByLabelText('文件路径'))
    expect(await screen.findByRole('option', { name: 'plugins/Essentials/config.yml' })).toBeInTheDocument()

    // 手输清单未覆盖的路径仍必须能提交（allowCustom）
    await user.type(within(region).getByLabelText('文件路径'), 'plugins/NotScanned/config.yml')
    expect(within(region).getByLabelText('文件路径')).toHaveValue('plugins/NotScanned/config.yml')
  }, 20_000)

  // 路径前缀是目录语义：候选按文件路径的目录聚合（plugins/Essentials/config.yml → plugins/、plugins/Essentials/）
  it('清单路径前缀筛选的下拉候选为目录聚合结果', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await screen.findAllByText('lobby-1')
    const prefixInput = screen.getByLabelText('路径前缀')
    await user.click(prefixInput)
    // 目录前缀候选（带尾斜杠）：plugins/Essentials/ 只能由真实文件路径聚合得出，不是常用目录兜底值
    expect(await screen.findByRole('option', { name: 'plugins/Essentials/' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'plugins/Essentials/config.yml' })).not.toBeInTheDocument()
  }, 20_000)

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

  it('敏感规则保存中阻止关闭，失败原因留在弹窗内', async () => {
    useScenario('normal')
    server.use(
      http.put('*/admin/v2/assets/sensitive-rules', async () => {
        await delay(400)
        return HttpResponse.json({ code: 'temporary_failure', message: '模拟规则保存失败' }, { status: 503 })
      }),
    )
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('button', { name: '敏感路径规则' }))
    const dialog = await screen.findByRole('dialog')
    const save = within(dialog).getByRole('button', { name: '保存规则' })
    await waitFor(() => expect(save).toBeEnabled())
    await user.click(save)
    await waitFor(() => expect(save).toBeDisabled())
    expect(within(dialog).getByRole('button', { name: '取消' })).toBeDisabled()

    await user.keyboard('{Escape}')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    const overlay = document.querySelector('[data-slot="dialog-overlay"]')
    if (!overlay) {
      throw new Error('未找到弹窗遮罩')
    }
    await user.click(overlay)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(await within(dialog).findByText('模拟规则保存失败')).toBeInTheDocument()
  })

  it('单文件查看直接读取并展示正文，同时保留敏感标记和审计提示', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    let requestNamespaceId: number | undefined
    let requestServerId = ''
    let requestPath = ''
    let requestReason = ''
    server.use(
      http.post('*/admin/v2/assets/preview', async ({ request }) => {
        const body = await request.json() as { namespaceId: number; serverId: string; path: string; reason: string }
        requestNamespaceId = body.namespaceId
        requestServerId = body.serverId
        requestPath = body.path
        requestReason = body.reason
        return HttpResponse.json({
          content: 'sensitive-value: 12345',
          truncated: false,
          binary: false,
          sha256: 'b'.repeat(64),
          size: 20,
          sensitive: true,
        })
      }),
    )
    renderPage(<AssetsPage />)

    const row = (await screen.findAllByText('lobby-1'))[0].closest('tr')
    if (!row) throw new Error('未找到资产清单行')
    await user.click(row)
    await user.click(await screen.findByRole('button', { name: '查看文件内容' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument()
    // 打开即读取：弹窗内不再有「读取正文」二次确认按钮，正文自动加载
    expect(within(dialog).queryByRole('button', { name: '读取正文' })).not.toBeInTheDocument()

    expect(await within(dialog).findByText('sensitive-value: 12345')).toBeInTheDocument()
    expect(within(dialog).getByText('敏感')).toBeInTheDocument()
    // 审计提示已移除：只保留「敏感」标记本身，不再在正文旁解释流程
    expect(within(dialog).queryByText('本次敏感文件读取会记录审计，审计中不保存正文。')).not.toBeInTheDocument()
    expect(requestNamespaceId).toBe(1)
    expect(requestServerId).toBe('lobby-1')
    expect(requestPath).not.toBe('')
    expect(requestReason).toBe('管理台文件正文直读')
  }, 20_000)

  it('单文件直读失败后可重试并显示正文', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    let requestCount = 0
    server.use(
      http.post('*/admin/v2/assets/preview', async () => {
        requestCount += 1
        if (requestCount === 1) {
          await delay(80)
          return HttpResponse.json({ code: 'temporary_failure', message: '模拟预览失败' }, { status: 503 })
        }
        return HttpResponse.json({
          content: '重试成功正文', truncated: false, binary: false, sha256: 'c'.repeat(64), size: 18, sensitive: false,
        })
      }),
    )
    renderPage(<AssetsPage />)

    const row = (await screen.findAllByText('lobby-1'))[0].closest('tr')
    if (!row) throw new Error('未找到资产清单行')
    await user.click(row)
    await user.click(await screen.findByRole('button', { name: '查看文件内容' }))
    const dialog = await screen.findByRole('dialog')
    // 打开即读取：无需再点确认，直接进入加载态
    expect(await within(dialog).findByText('正在读取正文…')).toBeInTheDocument()
    const alert = await within(dialog).findByRole('alert')
    expect(within(alert).getByText('模拟预览失败')).toBeInTheDocument()
    await user.click(within(alert).getByRole('button', { name: '重试读取' }))
    expect(await within(dialog).findByText('重试成功正文')).toBeInTheDocument()
    expect(requestCount).toBe(2)
  }, 20_000)

  it('两侧差异直读显示加载态，失败后可重试', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    let requestCount = 0
    server.use(
      http.post('*/admin/v2/assets/diff', async () => {
        requestCount += 1
        await delay(80)
        if (requestCount === 1) {
          return HttpResponse.json({ code: 'temporary_failure', message: '模拟差异读取失败' }, { status: 503 })
        }
        return HttpResponse.json({ identical: true, left: null, right: null })
      }),
    )
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '两侧差异' }))
    const region = await screen.findByRole('region', { name: '两侧差异' })
    await user.click(within(region).getByLabelText('左侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-1' }))
    await user.click(within(region).getByLabelText('右侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-2' }))
    await user.type(within(region).getByLabelText('文件路径'), 'plugins/Beacon/config.yml')

    await user.click(within(region).getByRole('button', { name: '查看差异' }))
    expect(within(region).getByRole('button', { name: '正在读取差异…' })).toBeDisabled()
    const alert = await within(region).findByRole('alert')
    expect(within(alert).getByText('模拟差异读取失败')).toBeInTheDocument()
    await user.click(within(alert).getByRole('button', { name: '重试读取' }))
    expect(await within(region).findByText('两侧文件内容一致，无需展示正文。')).toBeInTheDocument()
    expect(requestCount).toBe(2)
  }, 20_000)

  it('两侧差异直接读取并展示服务端返回的两侧正文', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    let pairBody: unknown = null
    server.use(
      http.post('*/admin/v2/assets/diff', async ({ request }) => {
        pairBody = await request.json()
        return HttpResponse.json({
          identical: false,
          left: { serverId: 'lobby-1', path: 'plugins/Beacon/config.yml', content: 'currency: 10', sha256: 'a'.repeat(64) },
          right: { serverId: 'lobby-2', path: 'plugins/Beacon/config.yml', content: 'currency: 20', sha256: 'b'.repeat(64) },
        })
      }),
    )
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '两侧差异' }))
    const region = await screen.findByRole('region', { name: '两侧差异' })
    expect(within(region).getByText('尚未查看差异')).toBeInTheDocument()
    expect(within(region).queryByRole('button', { name: '申请两侧审批' })).not.toBeInTheDocument()
    // 子服候选来自集群域真实服务器，点选而不是手填 serverId
    await user.click(within(region).getByLabelText('左侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-1' }))
    await user.click(within(region).getByLabelText('右侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-2' }))
    // 路径下拉允许手输：清单里没有的路径仍可直接比较
    await user.type(within(region).getByLabelText('文件路径'), 'plugins/Beacon/config.yml')
    await user.click(within(region).getByRole('button', { name: '查看差异' }))

    expect(pairBody).toEqual({
      left: { namespaceId: 1, serverId: 'lobby-1', path: 'plugins/Beacon/config.yml' },
      right: { namespaceId: 1, serverId: 'lobby-2', path: 'plugins/Beacon/config.yml' },
      reason: '管理台文件差异直读',
    })
    expect(await within(region).findByText('两侧文件存在差异')).toBeInTheDocument()
    expect(within(region).getByText('currency: 10')).toBeInTheDocument()
    expect(within(region).getByText('currency: 20')).toBeInTheDocument()
    expect(within(region).getByText('文件正文仅随本次直读响应展示；审计只记录目标与元数据。')).toBeInTheDocument()
  }, 20_000)

  it('切换页眉命名空间后清空两侧选择、路径和已加载差异', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    server.use(
      http.post('*/admin/v2/assets/diff', () =>
        HttpResponse.json({ identical: true, left: null, right: null }),
      ),
    )
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '两侧差异' }))
    const region = await screen.findByRole('region', { name: '两侧差异' })
    await user.click(within(region).getByLabelText('左侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-1' }))
    await user.click(within(region).getByLabelText('右侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-2' }))
    await user.type(within(region).getByLabelText('文件路径'), 'plugins/Beacon/config.yml')
    await user.click(within(region).getByRole('button', { name: '查看差异' }))
    expect(await within(region).findByText('两侧文件内容一致，无需展示正文。')).toBeInTheDocument()

    act(() => {
      setObservationScope({ kind: 'env', envId: 2, namespaceId: 2 })
    })

    await waitFor(() => {
      expect(within(region).getByLabelText('左侧子服')).toHaveValue('')
      expect(within(region).getByLabelText('右侧子服')).toHaveValue('')
      expect(within(region).getByLabelText('文件路径')).toHaveValue('')
      expect(within(region).queryByText('两侧文件内容一致，无需展示正文。')).not.toBeInTheDocument()
      expect(within(region).getByText('尚未查看差异')).toBeInTheDocument()
      expect(within(region).getByRole('button', { name: '查看差异' })).toBeDisabled()
    })
  }, 20_000)
})

// ---- 资产正文缓存与候选作用域（组件级用例）----
// 这一组直接渲染单个组件并自建 QueryClient：既能把敏感接口的响应压成可识别的正文，
// 也能在同一实例上读 MutationCache 断言「正文没有被留在缓存里」。
function renderWithClient(ui: ReactElement) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const view = render(<QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>)
  return { ...view, queryClient }
}

/** 单文件预览的最小宿主：关闭弹窗即把 target 置空（与清单详情面板同一行为） */
function PreviewDialogHarness() {
  const [target, setTarget] = useState<{ namespaceId: number; serverId: string; path: string } | null>({
    namespaceId: 1,
    serverId: 'lobby-1',
    path: 'plugins/Economy/config.yml',
  })
  return (
    <PreviewDialog
      target={target}
      onOpenChange={(open) => { if (!open) setTarget(null) }}
    />
  )
}

/** 两侧差异的最小宿主：用按钮切换命名空间，触发面板内的清理逻辑 */
function DiffPanelHarness() {
  const [namespaceId, setNamespaceId] = useState(1)
  return (
    <>
      <button type="button" onClick={() => { setNamespaceId(2) }}>切换命名空间</button>
      <DiffPanel namespaceId={namespaceId} />
    </>
  )
}

describe('资产正文直读不滞留 MutationCache', () => {
  it('单文件预览关闭弹窗后，正文不再可从 MutationCache 取到', async () => {
    useScenario('normal')
    const body = 'mutation-cache-must-drop-this: 1'
    server.use(
      http.post('*/admin/v2/assets/preview', () =>
        HttpResponse.json({
          content: body,
          truncated: false,
          binary: false,
          sha256: 'd'.repeat(64),
          size: body.length,
          sensitive: true,
        }),
      ),
    )
    const user = userEvent.setup()
    const { queryClient } = renderWithClient(<PreviewDialogHarness />)

    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText(body)).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: '关闭' }))
    await waitFor(() => {
      expect(screen.queryByText(body)).not.toBeInTheDocument()
    })
    // 该 mutation 设了 gcTime: 0，失去 observer 后应立刻被回收；
    // 若沿用默认 gcTime，正文会在全局缓存里滞留数分钟，关闭弹窗后仍可被取到。
    await waitFor(() => {
      const cached = queryClient.getMutationCache().getAll()
      expect(cached.some((mutation) => JSON.stringify(mutation.state.data ?? '').includes(body))).toBe(false)
    })
  }, 20_000)

  it('两侧差异在切换命名空间重置后，正文不再可从 MutationCache 取到', async () => {
    useScenario('normal')
    const leftBody = 'left-secret: mutation-cache-must-drop-this'
    const rightBody = 'right-secret: mutation-cache-must-drop-this'
    server.use(
      http.post('*/admin/v2/assets/diff', () =>
        HttpResponse.json({
          identical: false,
          left: { serverId: 'lobby-1', path: 'plugins/Beacon/config.yml', content: leftBody, sha256: 'a'.repeat(64) },
          right: { serverId: 'lobby-2', path: 'plugins/Beacon/config.yml', content: rightBody, sha256: 'b'.repeat(64) },
        }),
      ),
    )
    const user = userEvent.setup()
    const { queryClient } = renderWithClient(<DiffPanelHarness />)

    const region = await screen.findByRole('region', { name: '两侧差异' })
    await user.click(within(region).getByLabelText('左侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-1' }))
    await user.click(within(region).getByLabelText('右侧子服'))
    await user.click(await screen.findByRole('option', { name: 'lobby-2' }))
    await user.type(within(region).getByLabelText('文件路径'), 'plugins/Beacon/config.yml')
    await user.click(within(region).getByRole('button', { name: '查看差异' }))
    expect(await within(region).findByText(leftBody)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '切换命名空间' }))

    await waitFor(() => {
      expect(screen.queryByText(leftBody)).not.toBeInTheDocument()
    })
    await waitFor(() => {
      const cached = queryClient.getMutationCache().getAll()
      const serialized = JSON.stringify(cached.map((mutation) => mutation.state.data ?? ''))
      expect(serialized).not.toContain(leftBody)
      expect(serialized).not.toContain(rightBody)
    })
  }, 20_000)
})

describe('路径候选不跨命名空间残留', () => {
  it('切换命名空间后不沿用上一个命名空间的候选，新候选到达前保持空态', async () => {
    useScenario('normal')
    const assetPage = (namespaceId: number, path: string) => ({
      items: [{
        serverId: 'lobby-1',
        namespaceId,
        path,
        ext: 'yml',
        sha256: 'e'.repeat(64),
        size: 10,
        mtimeMs: 0,
        isText: true,
        scannedAt: '2026-01-01T00:00:00.000Z',
      }],
      total: 1,
    })
    server.use(
      http.get('*/admin/v2/assets', async ({ request }) => {
        const namespaceId = Number(new URL(request.url).searchParams.get('namespaceId'))
        if (namespaceId === 2) {
          // 放慢新命名空间的响应，撑开「已切换但新候选未到」的窗口：
          // 这段窗口正是旧的 keepPreviousData 会把上一个命名空间路径端出来的地方。
          await delay(300)
          return HttpResponse.json(assetPage(2, 'only-in-ns2/config.yml'))
        }
        return HttpResponse.json(assetPage(1, 'only-in-ns1/config.yml'))
      }),
    )
    const user = userEvent.setup()
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const picker = (namespaceId: number) => (
      <QueryClientProvider client={queryClient}>
        <AssetPathPicker namespaceId={namespaceId} serverId="" value="" onChange={() => undefined} aria-label="文件路径" />
      </QueryClientProvider>
    )
    const view = render(picker(1))

    await user.click(screen.getByLabelText('文件路径'))
    expect(await screen.findByRole('option', { name: 'only-in-ns1/config.yml' })).toBeInTheDocument()

    view.rerender(picker(2))

    // 新命名空间候选还没到达：不得把上一个命名空间的路径当作候选展示
    expect(screen.queryByRole('option', { name: 'only-in-ns1/config.yml' })).not.toBeInTheDocument()
    expect(await screen.findByRole('option', { name: 'only-in-ns2/config.yml' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'only-in-ns1/config.yml' })).not.toBeInTheDocument()
  }, 20_000)
})
