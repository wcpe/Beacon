// MCP 管理面 mock（/admin/v2/mcp-clients* 与 /admin/v2/mcp/config）。
// 契约真源：docs/specs/built-in-admin-v2-mcp-and-oauth.md §3.2 / §4。
//
// 四态（FR-159）：
//   empty  —— 无客户端 + MCP 未启用（验证空态与"未启用"配置指引）
//   normal —— 2 个客户端（observer 生效 / automation 已吊销）+ 已启用配置
//   huge   —— 60 个客户端（验证列表自区滚与不卡顿）
//   error  —— 列表与配置端点均返回 500（验证错误态）
//
// 写端点语义与真后端对齐：创建/轮换/启用返回 202 审批票据（明文 secret 仅首次出现），
// 吊销为直执返回 {ok:true}。缺 Idempotency-Key 时按真后端返回 403。

import { HttpResponse, type HttpHandler } from 'msw'
import type { MCPClientItem, MCPClientProfile, MCPConfigView } from '@beacon/contracts'
import { internalError, jsonError, mockGet, mockPost, readBody, pathParam } from '../http'
import { defineScenarioStore } from '../store'
import { getMockScenario, type MockScenario } from '../scenario'
import { isoOffset } from '../support'

// mock 客户端行：在契约视图基础上补 hash 位（mock 不落库，仅保持结构可扩展）
interface MCPClientRow extends MCPClientItem {
  // 待审批变更（提审后产生，批准在 mock 中即时应用以便观察列表变化）
  pendingReason?: string
}

// 场景初始数据
function buildClients(scenario: MockScenario): MCPClientRow[] {
  if (scenario === 'empty') {
    return []
  }
  if (scenario === 'huge') {
    return Array.from({ length: 60 }, (_, i) => {
      const active = i % 5 !== 0
      return {
        clientId: `mcp_huge${String(i).padStart(3, '0')}${'x'.repeat(20)}`,
        displayName: `外部集成 ${String(i + 1).padStart(2, '0')}`,
        secretPrefix: `mcs_${String(i).padStart(4, '0')}`,
        profile: (i % 3 === 0 ? 'automation' : 'observer') satisfies MCPClientProfile,
        status: active ? 'active' : 'revoked',
        secretVersion: (i % 4) + 1,
        createdBy: 'human:admin',
        createdAt: isoOffset(-(i + 1) * 36e5),
        updatedAt: isoOffset(-(i + 1) * 18e5),
        revokedAt: active ? null : isoOffset(-(i + 1) * 9e5),
      }
    })
  }
  return [
    {
      clientId: 'mcp_7f3a91c4e8b25d60a1f9',
      displayName: '巡检观测端',
      secretPrefix: 'mcs_9a2f',
      profile: 'observer',
      status: 'active',
      secretVersion: 1,
      createdBy: 'human:admin',
      createdAt: isoOffset(-72 * 36e5),
      updatedAt: isoOffset(-72 * 36e5),
    },
    {
      clientId: 'mcp_c18d4b70a6e93f25c0b1',
      displayName: '内网自动化平台',
      secretPrefix: 'mcs_4d71',
      profile: 'automation',
      status: 'revoked',
      secretVersion: 2,
      createdBy: 'human:admin',
      createdAt: isoOffset(-240 * 36e5),
      updatedAt: isoOffset(-96 * 36e5),
      revokedAt: isoOffset(-96 * 36e5),
    },
  ]
}

const getClients = defineScenarioStore(buildClients)

// 配置随场景变化：empty 场景顺带演示「未启用」指引
function buildConfig(scenario: MockScenario): MCPConfigView {
  if (scenario === 'empty') {
    return {
      enabled: false,
      publicBaseUrl: '',
      trustedProxyCidrs: [],
      allowInsecureInternal: false,
      allowedHosts: [],
      allowApprovalDecide: false,
      allowMachineRegister: false,
      directMode: false,
    }
  }
  return {
    enabled: true,
    publicBaseUrl: 'https://beacon.example.com',
    trustedProxyCidrs: ['10.0.0.0/8', '172.16.0.0/12'],
    allowInsecureInternal: false,
    allowedHosts: [],
    allowApprovalDecide: true,
    allowMachineRegister: false,
    directMode: false,
  }
}

const getConfig = defineScenarioStore(buildConfig)

// mock 明文 secret：与真后端同形（mcs_ 前缀 + 高熵串），仅用于演示一次性明文弹窗
function mockSecret(): string {
  return `mcs_${crypto.randomUUID().replace(/-/g, '')}${crypto.randomUUID().replace(/-/g, '').slice(0, 12)}`
}

function mockClientId(): string {
  return `mcp_${crypto.randomUUID().replace(/-/g, '').slice(0, 20)}`
}

// 缺幂等键按真后端口径拒绝（requestCredentialChange 的前置校验）
function requireIdempotency(request: Request): Response | null {
  const key = request.headers.get('Idempotency-Key')
  if (key === null || key.trim() === '') {
    return jsonError(403, 'FORBIDDEN', '缺少 Idempotency-Key')
  }
  return null
}

export const mcpHandlers: HttpHandler[] = [
  mockGet('/admin/v2/mcp-clients', () => {
    if (getMockScenario() === 'error') {
      return internalError()
    }
    return HttpResponse.json({ items: getClients() })
  }),

  mockGet('/admin/v2/mcp-clients/:clientId', ({ params }) => {
    if (getMockScenario() === 'error') {
      return internalError()
    }
    const clientId = String(params.clientId)
    const found = getClients().find((item) => item.clientId === clientId)
    if (found === undefined) {
      return jsonError(404, 'MCP_CLIENT_NOT_FOUND', '客户端不存在')
    }
    return HttpResponse.json(found)
  }),

  mockGet('/admin/v2/mcp/config', () => {    if (getMockScenario() === 'error') {
      return internalError()
    }
    return HttpResponse.json(getConfig())
  }),

  mockPost('/admin/v2/mcp-clients', async ({ request }) => {
    const missing = requireIdempotency(request)
    if (missing !== null) {
      return missing
    }
    const body = await readBody<{ displayName?: string; profile?: string; reason?: string }>(request)
    const displayName = (body.displayName ?? '').trim()
    const profile = body.profile === 'automation' ? 'automation' : 'observer'
    if (displayName === '' || (body.reason ?? '').trim() === '') {
      return jsonError(400, 'INVALID_PARAM', '名称与审批原因必填')
    }
    const clientId = mockClientId()
    const secret = mockSecret()
    const now = isoOffset(0)
    // mock 直接落库为 active，便于列表立刻可见；真后端需审批后由 worker 应用
    getClients().unshift({
      clientId,
      displayName,
      secretPrefix: secret.slice(0, 8),
      profile,
      status: 'active',
      secretVersion: 1,
      createdBy: 'human:admin',
      createdAt: now,
      updatedAt: now,
    })
    return HttpResponse.json(
      {
        approvalRequestId: `apr_${crypto.randomUUID().slice(0, 8)}`,
        clientId,
        clientSecret: secret,
        status: 'pending',
      },
      { status: 202 },
    )
  }),

  mockPost('/admin/v2/mcp-clients/:clientId/rotate', ({ params, request }) => {
    const missing = requireIdempotency(request)
    if (missing !== null) {
      return missing
    }
    const clientId = String(params.clientId)
    const target = getClients().find((item) => item.clientId === clientId)
    if (target === undefined) {
      return jsonError(404, 'MCP_CLIENT_NOT_FOUND', '客户端不存在')
    }
    const secret = mockSecret()
    target.secretPrefix = secret.slice(0, 8)
    target.secretVersion += 1
    target.updatedAt = isoOffset(0)
    return HttpResponse.json(
      {
        approvalRequestId: `apr_${crypto.randomUUID().slice(0, 8)}`,
        clientId,
        clientSecret: secret,
        status: 'pending',
      },
      { status: 202 },
    )
  }),

  mockPost('/admin/v2/mcp-clients/:clientId/enable', ({ params, request }) => {
    const missing = requireIdempotency(request)
    if (missing !== null) {
      return missing
    }
    const clientId = String(params.clientId)
    const target = getClients().find((item) => item.clientId === clientId)
    if (target === undefined) {
      return jsonError(404, 'MCP_CLIENT_NOT_FOUND', '客户端不存在')
    }
    target.status = 'active'
    target.revokedAt = null
    target.updatedAt = isoOffset(0)
    return HttpResponse.json(
      { approvalRequestId: `apr_${crypto.randomUUID().slice(0, 8)}`, clientId, status: 'pending' },
      { status: 202 },
    )
  }),

  // 吊销为直执（不等待审批）
  mockPost('/admin/v2/mcp-clients/:clientId/revoke', ({ params }) => {
    const clientId = String(params.clientId)
    const target = getClients().find((item) => item.clientId === clientId)
    if (target === undefined) {
      return jsonError(404, 'MCP_CLIENT_NOT_FOUND', '客户端不存在')
    }
    target.status = 'revoked'
    target.revokedAt = isoOffset(0)
    target.updatedAt = isoOffset(0)
    return HttpResponse.json({ ok: true })
  }),
]

// 供测试直接取用（避免经 HTTP 断言 mock 内部状态）
export { pathParam }
