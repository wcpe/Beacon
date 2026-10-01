// MCP 管理面 mock（/admin/v2/mcp-clients*、/admin/v2/mcp/config 与 /admin/v2/mcp/invocations*）。
// 契约真源：docs/specs/built-in-admin-v2-mcp-and-oauth.md §3.2 / §4；
//          docs/specs/mcp-invocation-audit.md §3.2 / §3.4 / §3.7（工具调用流水，FR-240）。
//
// 四态（FR-159）：
//   empty  —— 无客户端 + MCP 未启用（验证空态与"未启用"配置指引）；流水同样为空
//   normal —— 2 个客户端（observer 生效 / automation 已吊销）+ 已启用配置 + 18 条流水
//   huge   —— 60 个客户端（验证列表自区滚与不卡顿）+ 600+ 条流水（验证游标分页）
//   error  —— 列表与配置端点均返回 500（验证错误态，流水端点同）
//
// 写端点语义与真后端对齐：创建/轮换/启用返回 202 审批票据（明文 secret 仅首次出现），
// 吊销为直执返回 {ok:true}。缺 Idempotency-Key 时按真后端返回 403。
//
// 流水 mock 的语义对齐（§3.7）：六维过滤（tool / clientId / result / riskLevel / reason / from+to）
// + 游标分页（nextCursor 空串=末页，limit 服务端规整到 1..100）；非法枚举与倒置时间范围回 400，
// 详情未命中回 404（不区分「非法 ID」与「不存在」）。与真后端一样**只回脱敏摘要**，无参数正文。

import { HttpResponse, type HttpHandler } from 'msw'
import type {
  MCPClientItem,
  MCPClientProfile,
  MCPConfigView,
  MCPInvocationItem,
  MCPInvocationListResponse,
  MCPInvocationReason,
  MCPInvocationResult,
  MCPInvocationRiskLevel,
} from '@beacon/contracts'
import { internalError, jsonError, mockGet, mockPost, readBody, pathParam, queryInt, queryStr, queryTimeMs } from '../http'
import { defineScenarioStore } from '../store'
import { getMockScenario, type MockScenario } from '../scenario'
import { BASE_MS, isoOffset, pseudoSha256 } from '../support'

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
      productionMode: false,
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
    productionMode: false,
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

// ---- 工具调用流水 mock 数据（FR-240 §3.2 / §3.4 / §3.7）----
//
// 数据集只覆盖「脱敏摘要」这一契约面：targetDigest / argKeys / argBytes 都是 §3.3 三档规则
// 的产物形态，任何参数正文都不出现。行按 createdAt 倒序（同刻以 invocationId 倒序稳定定序），
// 时间铺展到昨日与今日，便于验证时间窗过滤。
//
// 数据是历史快照，不追求与 config mock 的当前快照自洽：例如既有 production_mode 拒执行
// （说明当时开关为开），也有 critical 工具成功的行（说明当时开关为关）。

/** 流水行覆盖的客户端（前两个 ID 与 mcp-clients mock 保持一致；第三个演示直连部署取不到地址） */
const MOCK_INVOCATION_CLIENTS: readonly { clientId: string; profile: MCPClientProfile; clientIp: string }[] = [
  { clientId: 'mcp_7f3a91c4e8b25d60a1f9', profile: 'observer', clientIp: '10.0.0.7' },
  { clientId: 'mcp_c18d4b70a6e93f25c0b1', profile: 'automation', clientIp: '172.16.8.21' },
  { clientId: 'mcp_4e8b2a17c93d05f6ab73', profile: 'automation', clientIp: '' },
]

/** 结果枚举（§3.4 冻结）：查询传入非法值一律 400 INVALID_PARAM */
const INVOCATION_RESULTS: readonly MCPInvocationResult[] = ['ok', 'fail', 'rejected']
/** 风险等级枚举（§3.2 冻结） */
const INVOCATION_RISK_LEVELS: readonly MCPInvocationRiskLevel[] = ['low', 'high', 'critical', 'unknown']
/** 原因码枚举（§3.4 冻结；不含空串——空串等价于「未传」故不参与过滤） */
const INVOCATION_REASONS: readonly MCPInvocationReason[] = [
  'unknown_tool',
  'production_mode',
  'handler_rejected',
  'handler_error',
  'input_required',
  'internal_error',
]

/** 单页条数默认值与上限（§3.7：≤0 取默认、>100 取 100） */
const INVOCATION_DEFAULT_LIMIT = 20
const INVOCATION_MAX_LIMIT = 100
/** huge 场景的流水行数（远大于单页上限，验证多页游标续翻无重复无遗漏） */
const INVOCATION_HUGE_COUNT = 620

function isOneOf<T extends string>(values: readonly T[], value: string): value is T {
  return (values as readonly string[]).includes(value)
}

/** 流水配方：只声明需要人工区分的维度，其余字段由 buildInvocation 确定性补齐 */
interface InvocationRecipe {
  /** 距会话时间基准的分钟数（越大越旧）：决定 createdAt 与 invocationId 内嵌毫秒 */
  minutesAgo: number
  /** 客户端下标（指向 MOCK_INVOCATION_CLIENTS） */
  clientIndex: number
  toolName: string
  riskLevel: MCPInvocationRiskLevel
  result: MCPInvocationResult
  reason: MCPInvocationReason
  /** 目标标识摘要（A 档白名单键的 `k=v` 拼接，值为脱敏后形态） */
  targetDigest: string
  /** 顶层参数键摘要（内容类键只记 `键名:字节数`，值绝不出现在此处） */
  argKeys: string
  /** 错误 / 拒绝摘要（已脱敏截断）；成功为空串 */
  errorSummary?: string
}

/**
 * 由内嵌毫秒与序号确定性生成 UUIDv7 形态的调用 ID（时间高位 + 版本 7 + 变体位 + 伪随机尾段）。
 * 真后端用 UUIDv7 内嵌毫秒直定日表；mock 只需要形状与唯一性，尾段取伪哈希。
 */
function invocationIdFor(ms: number, seq: number): string {
  const hex = pseudoSha256(`mcp-invocation:${String(seq)}`)
  const ts = Math.max(0, ms)
    .toString(16)
    .padStart(12, '0')
    .slice(-12)
  return `${ts.slice(0, 8)}-${ts.slice(8, 12)}-7${hex.slice(0, 3)}-8${hex.slice(3, 6)}-${hex.slice(6, 18)}`
}

function buildInvocation(recipe: InvocationRecipe, seq: number): MCPInvocationItem {
  const client = MOCK_INVOCATION_CLIENTS[recipe.clientIndex]
  const createdMs = BASE_MS - recipe.minutesAgo * 60_000
  const trace = pseudoSha256(`mcp-invocation-trace:${String(seq)}`)
  return {
    invocationId: invocationIdFor(createdMs, seq),
    clientId: client.clientId,
    profile: client.profile,
    toolName: recipe.toolName,
    riskLevel: recipe.riskLevel,
    result: recipe.result,
    reason: recipe.reason,
    targetDigest: recipe.targetDigest,
    argKeys: recipe.argKeys,
    // 参数原文字节数：无顶层键（未传 / 非对象参数）记 0（§3.2「缺省 0」）；
    // 其余为演示用确定性值，只表达量级——正文本身不入库，也未进入本数据集
    argBytes: recipe.argKeys === '' ? 0 : 32 + (seq % 11) * 47,
    durationMs: 1 + (seq % 37),
    traceId: trace.slice(0, 16),
    clientIp: client.clientIp,
    errorSummary: recipe.errorSummary ?? '',
    createdAt: new Date(createdMs).toISOString(),
  }
}

/** 常规场景流水配方：覆盖三种 result、四种 riskLevel、三种客户端与全部原因码 */
const MCP_INVOCATION_RECIPES: readonly InvocationRecipe[] = [
  // 成功：低 / 高 / 关键三档各若干，含无参数调用（targetDigest 与 argKeys 均为空串）
  { minutesAgo: 2, clientIndex: 0, toolName: 'beacon.observability.health', riskLevel: 'low', result: 'ok', reason: '', targetDigest: '', argKeys: '' },
  { minutesAgo: 6, clientIndex: 1, toolName: 'beacon.namespaces.list', riskLevel: 'low', result: 'ok', reason: '', targetDigest: 'namespaceId=1', argKeys: 'namespaceId:1' },
  {
    minutesAgo: 11,
    clientIndex: 1,
    toolName: 'beacon.config.publish',
    riskLevel: 'high',
    result: 'ok',
    reason: '',
    targetDigest: 'namespaceCode=lobby;path=plugins/Essentials/config.yml',
    argKeys: 'comment:24,namespaceCode:5,path:35',
  },
  { minutesAgo: 73, clientIndex: 0, toolName: 'beacon.observability.metrics_query', riskLevel: 'low', result: 'ok', reason: '', targetDigest: 'serverId=game-1', argKeys: 'serverId:6,windowMinutes:3' },
  { minutesAgo: 96, clientIndex: 1, toolName: 'beacon.files.create', riskLevel: 'high', result: 'ok', reason: '', targetDigest: 'path=plugins/Beacon/notes.yml', argKeys: 'content:412,path:24' },
  // 生产模式开关为关时的 critical 工具成功行（与下方拒执行互为对照）
  {
    minutesAgo: 132,
    clientIndex: 2,
    toolName: 'beacon.approvals.approve',
    riskLevel: 'critical',
    result: 'ok',
    reason: '',
    targetDigest: 'requestId=apr-1c07',
    argKeys: 'idempotencyKey:36,reason:15,requestId:9',
  },
  { minutesAgo: 240, clientIndex: 0, toolName: 'beacon.messages.list', riskLevel: 'low', result: 'ok', reason: '', targetDigest: 'serverId=lobby-1', argKeys: 'limit:2,serverId:8' },
  { minutesAgo: 1_500, clientIndex: 0, toolName: 'beacon.observability.health', riskLevel: 'low', result: 'ok', reason: '', targetDigest: '', argKeys: '' },
  // 失败：SDK 包装错误（参数校验 / handler 抛错）与两条防御分支样例
  {
    minutesAgo: 58,
    clientIndex: 2,
    toolName: 'beacon.namespaces.list',
    riskLevel: 'low',
    result: 'fail',
    reason: 'handler_error',
    targetDigest: 'namespaceId=2',
    argKeys: 'namespaceId:1',
    errorSummary: 'query namespaces timeout',
  },
  {
    minutesAgo: 175,
    clientIndex: 1,
    toolName: 'beacon.config.rollback',
    riskLevel: 'high',
    result: 'fail',
    reason: 'handler_error',
    targetDigest: 'id=318;namespaceId=1;version=12',
    argKeys: 'id:2,namespaceId:1,version:2',
    errorSummary: '回滚目标版本不存在',
  },
  // 多轮交互未完成与流水层 panic 属枚举覆盖样例（现网通常不出现，仅用于演示筛选项）
  {
    minutesAgo: 1_620,
    clientIndex: 1,
    toolName: 'beacon.files.import',
    riskLevel: 'high',
    result: 'fail',
    reason: 'input_required',
    targetDigest: 'namespaceId=1',
    argKeys: 'namespaceId:1',
  },
  {
    minutesAgo: 1_800,
    clientIndex: 2,
    toolName: 'beacon.system.update_apply',
    riskLevel: 'critical',
    result: 'fail',
    reason: 'internal_error',
    targetDigest: '',
    argKeys: '',
    errorSummary: 'panic',
  },
  // 被拒：生产模式拒执（FR-242，文本前缀判据）、业务拒绝与未登记工具
  {
    minutesAgo: 17,
    clientIndex: 1,
    toolName: 'beacon.approvals.approve',
    riskLevel: 'critical',
    result: 'rejected',
    reason: 'production_mode',
    targetDigest: 'requestId=apr-8f31',
    argKeys: 'idempotencyKey:36,reason:12,requestId:9',
    errorSummary: '生产模式已禁用 critical 风险等级工具：beacon.approvals.approve',
  },
  {
    minutesAgo: 44,
    clientIndex: 1,
    toolName: 'beacon.system.update_apply',
    riskLevel: 'critical',
    result: 'rejected',
    reason: 'production_mode',
    targetDigest: '',
    argKeys: 'idempotencyKey:36,reason:21',
    errorSummary: '生产模式已禁用 critical 风险等级工具：beacon.system.update_apply',
  },
  {
    minutesAgo: 23,
    clientIndex: 2,
    toolName: 'beacon.files.create',
    riskLevel: 'high',
    result: 'rejected',
    reason: 'handler_rejected',
    targetDigest: 'path=plugins/Bad/config.yml',
    argKeys: 'content:1180,path:23,reason:18',
    errorSummary: '目标路径不在运维放行目录内',
  },
  {
    minutesAgo: 305,
    clientIndex: 1,
    toolName: 'beacon.identities.approve',
    riskLevel: 'high',
    result: 'rejected',
    reason: 'handler_rejected',
    targetDigest: 'identityId=idt-2f91;serverId=game-3',
    argKeys: 'identityId:7,note:26,serverId:6',
    errorSummary: '身份不在待确认状态',
  },
  {
    minutesAgo: 31,
    clientIndex: 0,
    toolName: 'beacon.ops.undeclared_tool',
    riskLevel: 'unknown',
    result: 'rejected',
    reason: 'unknown_tool',
    targetDigest: 'namespaceId=1',
    argKeys: 'namespaceId:1',
    errorSummary: 'unknown tool: beacon.ops.undeclared_tool',
  },
  {
    minutesAgo: 420,
    clientIndex: 2,
    toolName: 'beacon.tools.probe_directory',
    riskLevel: 'unknown',
    result: 'rejected',
    reason: 'unknown_tool',
    targetDigest: '',
    argKeys: '',
    errorSummary: 'unknown tool: beacon.tools.probe_directory',
  },
]

/** 构造当前场景的流水数据集（按 createdAt 倒序；同刻以 ID 倒序保证定序稳定） */
function buildInvocations(scenario: MockScenario): MCPInvocationItem[] {
  if (scenario === 'empty') {
    return []
  }
  const recipes =
    scenario === 'huge'
      ? // 超大量场景按配方循环铺开、每行间隔 3 分钟（跨昨日与今日），保证多页续翻与过滤都可验证
        Array.from({ length: INVOCATION_HUGE_COUNT }, (_, i) => ({
          ...MCP_INVOCATION_RECIPES[i % MCP_INVOCATION_RECIPES.length],
          minutesAgo: i * 3 + 1,
        }))
      : MCP_INVOCATION_RECIPES
  return recipes
    .map((recipe, seq) => buildInvocation(recipe, seq))
    .sort((a, b) =>
      a.createdAt === b.createdAt
        ? a.invocationId < b.invocationId
          ? 1
          : -1
        : a.createdAt < b.createdAt
          ? 1
          : -1,
    )
}

const getInvocations = defineScenarioStore(buildInvocations)

/**
 * 游标分页（与真端点同口径）：mock 以不透明偏移量令牌作 nextCursor，**空串表示末页**；
 * limit 由服务端规整到 1..100（≤0 或非法取默认 20）。`/admin/v2/connections` 同为偏移量令牌。
 */
function sliceByCursor(rows: readonly MCPInvocationItem[], url: URL): MCPInvocationListResponse {
  const start = queryInt(url, 'cursor', 0)
  const limit = Math.min(queryInt(url, 'limit', INVOCATION_DEFAULT_LIMIT), INVOCATION_MAX_LIMIT)
  return {
    items: rows.slice(start, start + limit),
    nextCursor: start + limit < rows.length ? String(start + limit) : '',
  }
}

/**
 * 解析 RFC3339 时间参数（与真端点 handler.parseISOms 同口径）：未传或无法解析一律回 null = 该侧不限。
 * 也就是说非法时间**不报错**，只有 from 与 to 都能解析且 from > to 才回 400（§3.7 唯一冻结的时间类 400）。
 */
function parseInvocationTime(url: URL, name: string): number | null {
  const ms = queryTimeMs(url, name)
  return ms === null || Number.isNaN(ms) ? null : ms
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

  // 工具调用流水列表（FR-240 §3.7）：六维过滤 + 游标分页，不返回 total
  mockGet('/admin/v2/mcp/invocations', ({ request }) => {
    const url = new URL(request.url)
    const result = queryStr(url, 'result')
    if (result !== null && !isOneOf(INVOCATION_RESULTS, result)) {
      return jsonError(400, 'INVALID_PARAM', 'result 仅支持 ok / fail / rejected')
    }
    const riskLevel = queryStr(url, 'riskLevel')
    if (riskLevel !== null && !isOneOf(INVOCATION_RISK_LEVELS, riskLevel)) {
      return jsonError(400, 'INVALID_PARAM', 'riskLevel 仅支持 low / high / critical / unknown')
    }
    const reason = queryStr(url, 'reason')
    if (reason !== null && !isOneOf(INVOCATION_REASONS, reason)) {
      return jsonError(400, 'INVALID_PARAM', `reason 仅支持 ${INVOCATION_REASONS.join(' / ')}`)
    }
    const tool = queryStr(url, 'tool')
    const clientId = queryStr(url, 'clientId')
    const fromMs = parseInvocationTime(url, 'from')
    const toMs = parseInvocationTime(url, 'to')
    if (fromMs !== null && toMs !== null && fromMs > toMs) {
      return jsonError(400, 'INVALID_PARAM', 'from 不得晚于 to')
    }
    const rows = getInvocations().filter((row) => {
      const createdMs = Date.parse(row.createdAt)
      if (fromMs !== null && createdMs < fromMs) {
        return false
      }
      if (toMs !== null && createdMs > toMs) {
        return false
      }
      if (tool !== null && row.toolName !== tool) {
        return false
      }
      if (clientId !== null && row.clientId !== clientId) {
        return false
      }
      if (result !== null && row.result !== result) {
        return false
      }
      if (riskLevel !== null && row.riskLevel !== riskLevel) {
        return false
      }
      if (reason !== null && row.reason !== reason) {
        return false
      }
      return true
    })
    return HttpResponse.json(sliceByCursor(rows, url) satisfies MCPInvocationListResponse)
  }),

  // 单条流水详情（FR-241 行点击）：非法 ID 与不存在一律 404，不区分（防探测，§3.7）
  mockGet('/admin/v2/mcp/invocations/:invocationId', ({ params }) => {
    const invocationId = String(params.invocationId)
    const row = getInvocations().find((item) => item.invocationId === invocationId)
    if (row === undefined) {
      return jsonError(404, 'mcp_invocation_not_found', '调用流水不存在')
    }
    return HttpResponse.json(row satisfies MCPInvocationItem)
  }),
]

// 供测试直接取用（避免经 HTTP 断言 mock 内部状态）
export { pathParam }
