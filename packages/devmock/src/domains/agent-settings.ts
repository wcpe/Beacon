// Agent 接入与配置 mock（/admin/v2/agent-settings，FR-279）。
//
// 契约真源：packages/contracts/src/agent-settings.ts（handler 返回值一律 satisfies 锚定，防 mock 与契约漂移）。
//
// 三层优先级：server 级覆盖 > namespace 默认 > 未设置（agent 回退本地 config.yml / 代码默认）。
// GET 逐键回生效值 + 来源；PUT 按作用域写入对应层，并保留真后端的两种口径：
//   - 普通键 → 200 {ok:true}（直执，agent ≤30s 热生效）；
//   - override.command-whitelist → 202 审批票据（ADR-0011 补偿措施：白名单变更走审批 + 审计）。
//
// 四态（FR-159）：
//   empty  —— 无任何下发值（所有项均为「未设置」），用于验证空层与「从未下发」文案；
//   normal —— prod namespace 默认层 3 项 + game-1 的 server 覆盖 2 项；
//   huge   —— 同 normal（本域键集合由 agent 侧决定，不随数据量放大）；
//   error  —— 读写端点统一 500（由 mockGet / mockPut 的 error 场景注入）。
import { HttpResponse, type HttpHandler } from 'msw'
import type {
  AgentSettingItem,
  AgentSettingSource,
  AgentSettingsApprovalTicket,
  AgentSettingsResponse,
  AgentSettingsSaved,
  UpdateAgentSettingsBody,
} from '@beacon/contracts'
import { jsonError, mockGet, mockPut, queryStr, readBody } from '../http'
import { defineScenarioStore } from '../store'
import type { MockScenario } from '../scenario'
import { isoOffset } from '../support'

// 可下发键目录：与 agent 侧 AgentBootstrap 读取的键逐键对齐；desc 为控制面侧中文说明
const CATALOG: readonly { key: string; desc: string }[] = [
  { key: 'timing.poll-timeout-ms', desc: '长轮询挂起上限（毫秒）' },
  { key: 'timing.request-timeout-ms', desc: '单次 HTTP 请求超时（毫秒）' },
  { key: 'timing.heartbeat-fallback-ms', desc: '心跳回退周期（毫秒）' },
  { key: 'backoff.initial-ms', desc: '重连退避初始间隔（毫秒）' },
  { key: 'backoff.max-ms', desc: '重连退避上限（毫秒）' },
  { key: 'snapshot.enabled', desc: '是否持久化生效配置快照' },
  { key: 'file-tree.enabled', desc: '是否启用文件树下发' },
  { key: 'messaging.enabled', desc: '是否启用跨服消息通道' },
  { key: 'messaging.rpc-timeout-ms', desc: '消息 RPC 超时（毫秒）' },
  { key: 'assets.enabled', desc: '是否启用文件树扫描上报' },
  { key: 'assets.scan-interval-sec', desc: '文件树扫描周期（秒）' },
  { key: 'delivery.config-reload-timeout-ms', desc: '配置热重载超时（毫秒）' },
  { key: 'override.command-whitelist', desc: '允许执行的控制台命令白名单（JSON 字符串数组）' },
]

// 禁止下发的键（与 agent PushedConfigPolicy 同集合）：真后端按 400 拒发，mock 必须一致
const FORBIDDEN_KEYS = new Set([
  'beacon.endpoints',
  'beacon.bootstrap-token',
  'identity.server-id',
  'identity.identity-id',
])
const FORBIDDEN_PREFIXES = ['identity.']

function isForbiddenKey(key: string): boolean {
  return FORBIDDEN_KEYS.has(key) || FORBIDDEN_PREFIXES.some((prefix) => key.startsWith(prefix))
}

interface AgentSettingsState {
  // namespace 默认层：namespaceId → (key → value)
  namespaceValues: Map<number, Record<string, string>>
  // server 覆盖层：`${namespaceId}:${serverId}` → (key → value)
  serverValues: Map<string, Record<string, string>>
  // 该作用域最近一次下发时间；空串 = 从未下发
  updatedAt: string
}

function emptyState(): AgentSettingsState {
  return { namespaceValues: new Map(), serverValues: new Map(), updatedAt: '' }
}

function buildState(scenario: MockScenario): AgentSettingsState {
  const state = emptyState()
  if (scenario === 'empty') {
    // 空态：什么都没下发过，所有项来源都是「未设置」
    return state
  }
  // prod（namespaceId=1）：namespace 默认层
  state.namespaceValues.set(1, {
    'timing.poll-timeout-ms': '25000',
    'messaging.enabled': 'true',
    'delivery.config-reload-timeout-ms': '30000',
  })
  // prod/game-1：server 覆盖层（验证「server 级覆盖 > namespace 默认」）
  state.serverValues.set('1:game-1', {
    'timing.poll-timeout-ms': '15000',
    'assets.scan-interval-sec': '600',
  })
  state.updatedAt = isoOffset(-3 * 3_600_000)
  return state
}

const getState = defineScenarioStore(buildState)

// GET /admin/v2/agent-settings?namespaceId=&serverId=
export const agentSettingsHandlers: HttpHandler[] = [
  mockGet('/admin/v2/agent-settings', ({ request }) => {
    const url = new URL(request.url)
    const namespaceIdRaw = queryStr(url, 'namespaceId')
    const namespaceId = namespaceIdRaw === null ? 0 : Number.parseInt(namespaceIdRaw, 10)
    if (!Number.isInteger(namespaceId) || namespaceId <= 0) {
      return jsonError(400, 'INVALID_PARAM', 'namespaceId 必填且须为正整数')
    }
    const serverId = queryStr(url, 'serverId') ?? ''
    const state = getState()
    const namespaceLayer = state.namespaceValues.get(namespaceId) ?? {}
    const serverLayer = serverId === '' ? {} : (state.serverValues.get(`${String(namespaceId)}:${serverId}`) ?? {})

    const items: AgentSettingItem[] = CATALOG.map(({ key, desc }) => {
      if (key in serverLayer) {
        return { key, value: serverLayer[key], source: 'server' satisfies AgentSettingSource, desc }
      }
      if (key in namespaceLayer) {
        return { key, value: namespaceLayer[key], source: 'namespace' satisfies AgentSettingSource, desc }
      }
      // 未下发：控制面不知道本地值，故 value 为空串（不回填代码默认，避免误导成「已设置」）
      return { key, value: '', source: 'default' satisfies AgentSettingSource, desc }
    })

    return HttpResponse.json({ items, updatedAt: state.updatedAt } satisfies AgentSettingsResponse)
  }),

  // PUT /admin/v2/agent-settings
  mockPut('/admin/v2/agent-settings', async ({ request }) => {
    const body = await readBody<UpdateAgentSettingsBody>(request)
    const namespaceId = typeof body.namespaceId === 'number' ? body.namespaceId : Number.NaN
    if (!Number.isInteger(namespaceId) || namespaceId <= 0) {
      return jsonError(400, 'INVALID_PARAM', 'namespaceId 必填且须为正整数')
    }
    const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
    if (reason === '') {
      return jsonError(400, 'INVALID_PARAM', 'reason 必填（下发原因会写入审计）')
    }
    const values = body.values ?? {}
    if (Object.keys(values).length === 0) {
      return jsonError(400, 'INVALID_PARAM', 'values 必填且至少包含一项')
    }
    const forbidden = Object.keys(values).filter(isForbiddenKey)
    if (forbidden.length > 0) {
      return jsonError(400, 'FORBIDDEN_AGENT_SETTING_KEY', `以下键不允许控制面下发：${forbidden.join(', ')}`)
    }
    const serverId = typeof body.serverId === 'string' ? body.serverId.trim() : ''
    const state = getState()
    // 白名单变更走审批：批准前不落库（真后端由审批执行器在批准后写入）
    const needsApproval = 'override.command-whitelist' in values
    if (!needsApproval) {
      if (serverId === '') {
        const layer = state.namespaceValues.get(namespaceId) ?? {}
        state.namespaceValues.set(namespaceId, { ...layer, ...values })
      } else {
        const layerKey = `${String(namespaceId)}:${serverId}`
        const layer = state.serverValues.get(layerKey) ?? {}
        state.serverValues.set(layerKey, { ...layer, ...values })
      }
      state.updatedAt = isoOffset(0)
      return HttpResponse.json({ ok: true } satisfies AgentSettingsSaved)
    }
    return HttpResponse.json(
      {
        approvalRequestId: `apr_agentset_${crypto.randomUUID().slice(0, 8)}`,
        status: 'pending',
      } satisfies AgentSettingsApprovalTicket,
      { status: 202 },
    )
  }),
]
