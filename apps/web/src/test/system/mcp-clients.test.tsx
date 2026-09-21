// /mcp-clients 页面测试：常规渲染、空态、配置卡（启用 / 未启用）、
// 创建提审（幂等键 + 一次性明文）、轮换、吊销直执、详情面板。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { http, HttpResponse } from 'msw'

import McpClientsPage from '../../pages/mcp-clients'
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

describe('/mcp-clients MCP 客户端页', () => {
  it('常规态渲染客户端列表与 profile', async () => {
    useScenario('normal')
    renderPage(<McpClientsPage />)

    expect(await screen.findByText('巡检观测端')).toBeInTheDocument()
    expect(await screen.findByText('内网自动化平台')).toBeInTheDocument()
    // profile 与状态都有中文标签（不止原始枚举值）
    expect(screen.getAllByText('只读').length).toBeGreaterThan(0)
    expect(screen.getAllByText('自动化').length).toBeGreaterThan(0)
    expect(screen.getAllByText('已吊销').length).toBeGreaterThan(0)
  })

  it('空态给出创建引导，且配置卡显示未启用指引', async () => {
    useScenario('empty')
    renderPage(<McpClientsPage />)

    expect(
      await screen.findByText('暂无 MCP 客户端，点击「申请创建客户端」接入第一个外部 Agent'),
    ).toBeInTheDocument()
    // empty 场景的配置为未启用：展示配置指引而非报错
    expect(
      await screen.findByText(
        'MCP 入口未启用，外部 Agent 无法连接。在配置文件设置 mcp.enabled 并完成公网基址与可信反代配置后重启控制面。',
      ),
    ).toBeInTheDocument()
  })

  it('配置卡展示部署事实与启动项提示', async () => {
    useScenario('normal')
    renderPage(<McpClientsPage />)

    expect(await screen.findByText('MCP 入口配置')).toBeInTheDocument()
    expect(await screen.findByText('https://beacon.example.com')).toBeInTheDocument()
    expect(await screen.findByText('反向代理')).toBeInTheDocument()
    // 启动项提示常驻，避免误以为可在页面修改
    expect(
      screen.getByText('这些是启动项：修改需编辑配置文件并重启控制面，管理台不提供写入。'),
    ).toBeInTheDocument()
    // 文档入口存在
    expect(screen.getByRole('link', { name: /查看部署文档/ })).toBeInTheDocument()
  })

  it('配置端点失败时展示错误态而非静默', async () => {
    useScenario('normal')
    server.use(http.get('/admin/v2/mcp/config', () => HttpResponse.json({ code: 'boom', message: '配置读取失败' }, { status: 500 })))
    renderPage(<McpClientsPage />)

    // 列表仍可渲染（两卡独立）
    expect(await screen.findByText('巡检观测端')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getByText(/配置读取失败/)).toBeInTheDocument()
    })
  })

  it('申请创建客户端：带幂等键、提交原因、展示一次性明文', async () => {
    useScenario('normal')
    server.use(
      http.post('/admin/v2/mcp-clients', async ({ request }) => {
        expect(request.headers.get('Idempotency-Key')).not.toBeNull()
        expect((await request.json()) as Record<string, unknown>).toMatchObject({
          displayName: '新接入平台',
          profile: 'automation',
          reason: '用于内网自动化',
        })
        return HttpResponse.json(
          {
            approvalRequestId: 'apr_mcp_create',
            clientId: 'mcp_new',
            clientSecret: 'mcs_plaintext_once',
            status: 'pending',
          },
          { status: 202 },
        )
      }),
    )
    const user = userEvent.setup()
    renderPage(<McpClientsPage />)

    await screen.findByText('巡检观测端')
    await user.click(screen.getByRole('button', { name: '申请创建客户端' }))

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('名称'), '新接入平台')
    // profile 选择（默认 observer，此处显式改选 automation）
    await user.click(within(dialog).getByRole('combobox', { name: 'profile' }))
    await user.click(await screen.findByRole('option', { name: '自动化' }))
    await user.type(within(dialog).getByLabelText('审批原因'), '用于内网自动化')
    await user.click(within(dialog).getByRole('button', { name: '提交申请' }))

    // 一次性明文弹窗：secret 只此一次可见
    expect(await screen.findByText('客户端已创建，请保存 secret')).toBeInTheDocument()
    expect(screen.getByText('mcs_plaintext_once')).toBeInTheDocument()
  })

  it('提审响应缺失明文时给出重新申请提示（不静默）', async () => {
    useScenario('normal')
    server.use(
      http.post('/admin/v2/mcp-clients', () =>
        // 幂等重放：服务端不再返回 clientSecret
        HttpResponse.json({ approvalRequestId: 'apr_replay', clientId: 'mcp_replay', status: 'pending' }, { status: 202 }),
      ),
    )
    const user = userEvent.setup()
    renderPage(<McpClientsPage />)

    await screen.findByText('巡检观测端')
    await user.click(screen.getByRole('button', { name: '申请创建客户端' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('名称'), '重复提交')
    await user.type(within(dialog).getByLabelText('审批原因'), '重试')
    await user.click(within(dialog).getByRole('button', { name: '提交申请' }))

    expect(
      await screen.findByText('该申请为重复提交，服务端未生成新的明文 secret。请重新申请轮换以获取新明文。'),
    ).toBeInTheDocument()
  })

  it('点击行展开非模态详情面板并显示 profile 能力说明', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<McpClientsPage />)

    expect(screen.queryByText('客户端详情')).not.toBeInTheDocument()
    await user.click(await screen.findByText('巡检观测端'))

    expect(await screen.findByText('客户端详情')).toBeInTheDocument()
    // 非模态：不应出现 role=dialog
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(
      screen.getByText('只能调用只读观测工具，不能发起任何变更申请。'),
    ).toBeInTheDocument()
    // 生效态提供轮换与吊销
    expect(screen.getByRole('button', { name: '轮换 secret' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '立即吊销' })).toBeInTheDocument()
  })

  it('已吊销客户端只提供重新启用', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<McpClientsPage />)

    await user.click(await screen.findByText('内网自动化平台'))
    await screen.findByText('客户端详情')

    expect(screen.getByRole('button', { name: '重新启用' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '轮换 secret' })).not.toBeInTheDocument()
  })

  it('轮换需要填写原因并带幂等键，成功后展示新明文', async () => {
    useScenario('normal')
    server.use(
      http.post('/admin/v2/mcp-clients/:clientId/rotate', async ({ request }) => {
        expect(request.headers.get('Idempotency-Key')).not.toBeNull()
        expect((await request.json()) as Record<string, unknown>).toMatchObject({ reason: '凭据疑似泄露' })
        return HttpResponse.json(
          {
            approvalRequestId: 'apr_rotate',
            clientId: 'mcp_7f3a91c4e8b25d60a1f9',
            clientSecret: 'mcs_rotated_once',
            status: 'pending',
          },
          { status: 202 },
        )
      }),
    )
    const user = userEvent.setup()
    renderPage(<McpClientsPage />)

    await user.click(await screen.findByText('巡检观测端'))
    await user.click(await screen.findByRole('button', { name: '轮换 secret' }))

    const dialog = await screen.findByRole('dialog')
    // 未填原因时提交按钮禁用
    expect(within(dialog).getByRole('button', { name: '提交申请' })).toBeDisabled()
    await user.type(within(dialog).getByLabelText('审批原因'), '凭据疑似泄露')
    await user.click(within(dialog).getByRole('button', { name: '提交申请' }))

    expect(await screen.findByText('secret 已轮换，请保存新 secret')).toBeInTheDocument()
    expect(screen.getByText('mcs_rotated_once')).toBeInTheDocument()
  })

  it('吊销为直执：二次确认后立即生效且不等待审批', async () => {
    useScenario('normal')
    server.use(
      http.post('/admin/v2/mcp-clients/:clientId/revoke', () => HttpResponse.json({ ok: true })),
    )
    const user = userEvent.setup()
    renderPage(<McpClientsPage />)

    await user.click(await screen.findByText('巡检观测端'))
    await user.click(await screen.findByRole('button', { name: '立即吊销' }))

    const dialog = await screen.findByRole('alertdialog')
    expect(
      within(dialog).getByText(
        '吊销是止损动作，立即执行且不需要审批。该客户端已签发的 token 全部即时失效，且无法再换取新 token。',
      ),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: '确认吊销' }))

    // 吊销后详情收起，列表出现第二个「已吊销」
    await waitFor(() => {
      expect(screen.queryByText('客户端详情')).not.toBeInTheDocument()
    })
  })

  it('错误场景展示列表加载失败', async () => {
    useScenario('error')
    renderPage(<McpClientsPage />)

    // 列表卡与配置卡各自独立取数，error 场景下两处都失败 → 各出现一次错误文案。
    // 断言精确条数（而非 >0），才能真正证明"两卡独立降级"。
    await waitFor(() => {
      expect(screen.getAllByText(/模拟内部错误/)).toHaveLength(2)
    })
  })

  it('超大场景渲染全部客户端且列表自区滚动（不无限增高）', async () => {
    useScenario('huge')
    renderPage(<McpClientsPage />)

    // 60 条全部渲染（列表在 ListCard 的自区滚动容器内，页面本身不无限增高）
    await waitFor(() => {
      expect(screen.getByText('外部集成 01')).toBeInTheDocument()
    })
    expect(screen.getByText('外部集成 60')).toBeInTheDocument()
    expect(screen.getAllByRole('row').length).toBeGreaterThan(50)
    // 汇总条给出上界事实，便于运维判断量级
    expect(screen.getByText('客户端总数')).toBeInTheDocument()
  })

  it('列表失败但配置正常时，配置卡不受影响（独立降级）', async () => {
    useScenario('normal')
    server.use(
      http.get('/admin/v2/mcp-clients', () =>
        HttpResponse.json({ code: 'boom', message: '客户端列表加载失败' }, { status: 500 }),
      ),
    )
    renderPage(<McpClientsPage />)

    // 列表报错，但配置卡正常展示部署事实
    await waitFor(() => {
      expect(screen.getByText(/客户端列表加载失败/)).toBeInTheDocument()
    })
    expect(await screen.findByText('MCP 入口配置')).toBeInTheDocument()
    expect(screen.getByText('https://beacon.example.com')).toBeInTheDocument()
  })
})
