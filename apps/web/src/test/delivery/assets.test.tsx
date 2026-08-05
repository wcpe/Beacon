// /assets 文件资产页测试：视图切换（清单 / 扫描概要 / 跨服比对 / 两侧差异）、
// 清单主从布局点行出非模态详情面板、空态引导、比对交互。
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AssetsPage from '../../pages/assets'
import { approveApproval, fetchApprovalDetail } from '../../api/approvals'
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

    // 详情为固定层抽屉（非 role=dialog），主表不 reflow
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

  it('单文件查看调用真实专用申请与一次消费接口，正文不进入页面', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    let approved = false
    let consumed = false
    let serverId = ''
    let path = ''
    const requestId = 'apr_real_asset_preview'
    const approval = () => ({
      id: 10002, requestId, operationKey: 'agent.command.fs_browse', operationKind: 'agent.command.fs_browse', resourceType: 'file_asset', resourceId: 'prod/lobby-1/hash',
      riskLevel: 'high', status: approved ? 'succeeded' : 'pending', requestReason: '核对线上配置', safeSummary: '脱敏摘要', frozenPayloadSha256: 'b'.repeat(64),
      requesterType: 'human', requesterId: 'admin', deciderType: approved ? 'human' : null, deciderId: approved ? 'admin' : null,
      approvedBy: approved ? 'admin' : null, rejectReason: null, decisionReason: null, expiresAt: null, version: approved ? 2 : 1,
      resultRef: approved ? 'agent-sensitive-operation:command:42:grant:sag_real_asset_preview' : null,
      sensitiveAccessGrant: approved ? { grantId: 'sag_real_asset_preview' } : undefined,
    })
    server.use(
      http.post('*/admin/v2/assets/preview/approval-requests', async ({ request }) => {
        const body = await request.json() as { serverId: string; path: string; reason: string }
        serverId = body.serverId
        path = body.path
        expect(body.reason).toBe('核对线上配置')
        expect(request.headers.get('Idempotency-Key')).not.toBeNull()
        return HttpResponse.json({ requestId, status: 'pending' }, { status: 202 })
      }),
      http.get(`*/admin/v2/approval-requests/${requestId}`, () => HttpResponse.json(approval())),
      http.post(`*/admin/v2/approval-requests/${requestId}/approve`, () => {
        approved = true
        return HttpResponse.json(approval(), { status: 202 })
      }),
      http.post('*/admin/v2/assets/preview/grants/sag_real_asset_preview/consume', async ({ request }) => {
        expect(await request.json()).toEqual({ commandId: 42 })
        consumed = true
        return HttpResponse.json({ content: '不得渲染的文件正文', truncated: false, binary: false, sha256: 'b'.repeat(64), size: 20 })
      }),
    )
    renderPage(<AssetsPage />)

    const row = (await screen.findAllByText('lobby-1'))[0].closest('tr')
    if (!row) throw new Error('未找到资产清单行')
    await user.click(row)
    await user.click(await screen.findByRole('button', { name: '申请查看文件内容' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), '核对线上配置')
    await user.click(within(dialog).getByRole('button', { name: '提交审批' }))
    expect((await within(dialog).findByRole('link', { name: '前往审批中心' })).getAttribute('href')).toBe(`/approvals/${requestId}`)
    expect(serverId).toBe('lobby-1')
    expect(path).not.toBe('')
    await approveApproval(requestId)
    await user.click(await within(dialog).findByRole('button', { name: '刷新审批状态' }))
    await user.click(await within(dialog).findByRole('button', { name: '一次性消费授权' }))
    expect(await within(dialog).findByText('授权已消费；敏感正文未被页面保存或展示。')).toBeInTheDocument()
    expect(within(dialog).queryByText('不得渲染的文件正文')).not.toBeInTheDocument()
    expect(consumed).toBe(true)
    expect((await fetchApprovalDetail(requestId)).sensitiveAccessGrant?.grantId).toBe('sag_real_asset_preview')
  }, 20_000)

  it('两侧差异分别提审，任一侧未批准时不消费，双侧就绪后仅展示元数据摘要', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    let leftReady = false
    let rightReady = false
    let pairBody: unknown = null
    let consumed = false
    const approval = (requestId: string, ready: boolean) => {
      const commandId = requestId === 'apr_left' ? 41 : 42
      return {
      id: requestId, requestId, operationKey: 'agent.command.fs_browse', operationKind: 'agent.command.fs_browse', resourceType: 'file_asset', resourceId: requestId,
      riskLevel: 'high', status: ready ? 'succeeded' : 'pending', requestReason: '核对两服经济配置差异', safeSummary: '脱敏摘要', frozenPayloadSha256: 'b'.repeat(64),
      requesterType: 'human', requesterId: 'admin', deciderType: ready ? 'human' : null, deciderId: ready ? 'admin' : null,
      approvedBy: ready ? 'admin' : null, rejectReason: null, decisionReason: null, expiresAt: null, version: ready ? 2 : 1,
      resultRef: ready ? `agent-sensitive-operation:command:${String(commandId)}:grant:sag_${requestId}` : null,
      sensitiveAccessGrant: ready ? { grantId: `sag_${requestId}` } : undefined,
      }
    }
    server.use(
      http.post('*/admin/v2/assets/pair-read/approval-requests', async ({ request }) => {
        pairBody = await request.json()
        return HttpResponse.json({ leftRequestId: 'apr_left', rightRequestId: 'apr_right', status: 'pending' }, { status: 202 })
      }),
      http.get('*/admin/v2/approval-requests/apr_left', () => HttpResponse.json(approval('apr_left', leftReady))),
      http.get('*/admin/v2/approval-requests/apr_right', () => HttpResponse.json(approval('apr_right', rightReady))),
      http.post('*/admin/v2/assets/pair-read/grants/sag_apr_left/consume', async ({ request }) => {
        expect(await request.json()).toEqual({ commandId: 41 })
        consumed = true
        return HttpResponse.json({
          identical: false,
          changed: true,
          unsupported: false,
          left: { serverId: 'lobby-1', path: 'plugins/Beacon/config.yml', sha256: 'a'.repeat(64), size: 12 },
          right: { serverId: 'lobby-2', path: 'plugins/Beacon/config.yml', sha256: 'b'.repeat(64), size: 13 },
          content: '不得进入页面的正文',
        })
      }),
    )
    renderPage(<AssetsPage />)

    await user.click(await screen.findByRole('tab', { name: '两侧差异' }))
    const region = await screen.findByRole('region', { name: '两侧差异' })
    await user.type(within(region).getByLabelText('左侧子服'), 'lobby-1')
    await user.type(within(region).getByLabelText('右侧子服'), 'lobby-2')
    await user.type(within(region).getByLabelText('文件路径'), 'plugins/Beacon/config.yml')
    await user.type(within(region).getByLabelText('diff 原因'), '核对两服经济配置差异')
    await user.click(within(region).getByRole('button', { name: '申请两侧审批' }))

    expect(pairBody).toEqual({
      left: { serverId: 'lobby-1', path: 'plugins/Beacon/config.yml' },
      right: { serverId: 'lobby-2', path: 'plugins/Beacon/config.yml' },
      reason: '核对两服经济配置差异',
    })
    expect((await within(region).findAllByText('等待审批或 Agent 回传')).length).toBe(2)
    expect(within(region).queryByRole('button', { name: '原子消费两侧授权并查看差异摘要' })).not.toBeInTheDocument()

    leftReady = true
    rightReady = true
    await user.click(within(region).getByRole('button', { name: '刷新审批状态' }))
    await user.click(await within(region).findByRole('button', { name: '原子消费两侧授权并查看差异摘要' }))

    expect(await within(region).findByText('两侧文件存在差异')).toBeInTheDocument()
    expect(within(region).getByText('仅显示服务端生成的路径、哈希和大小摘要；文件正文不会进入页面。')).toBeInTheDocument()
    expect(within(region).queryByText('不得进入页面的正文')).not.toBeInTheDocument()
    expect(consumed).toBe(true)
  }, 20_000)
})
