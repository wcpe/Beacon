// Agent 接入与配置 mock（/admin/v2/agent-settings，FR-279）。
//
// 契约真源：packages/contracts/src/agent-settings.ts（handler 返回值一律 satisfies 锚定，防 mock 与契约漂移）。
//
// **本 mock 刻意与真后端同严格**（apps/server/.../v2_agent_settings_handler.go +
// agent_settings_service.go）：mock 比后端宽松时，测试验证的是 mock 而不是缺陷——
// 「每次保存都 400」这类问题就会在测试全绿的情况下长期隐身。逐条对齐：
//   1. GET 返回 scopeLabel（= `环境码` 或 `环境码/serverId`）；
//   2. PUT 的 confirmationScope 缺失或与 scopeLabel 不符 → 400 AGENT_SETTING_CONFIRM_MISMATCH；
//   3. 空值 / 纯空白值 → 400（撤销须显式传 null，本版前端未提供该入口）；
//   4. 200 返回 {scope, applied, removed, unchanged}，**不是** {ok:true}；
//   5. 高危键转 202 票据，票据状态可按场景注入（见 setAgentSettingsTicketStatus）——
//      服务端命中幂等键时会回既有申请而**不判状态**，早已终结的旧票据必须能被测出来。
//
// 来源口径与真后端一致（只表达「控制面能断言到哪一层」）：
//   server 级下发行 → server；namespace 级下发行 → namespace；
//   两级都没有 → local-or-default（agent 本地链：环境变量 > config.yml > 代码默认，
//   控制面无法区分，故**不**回填任何代码默认值）。
//
// 四态（FR-159）：
//   empty  —— 无任何下发值（所有项 value 为空串、来源 local-or-default），用于验证空层与「从未下发」文案；
//   normal —— prod namespace 默认层 3 项 + game-1 的 server 覆盖 2 项；
//   huge   —— 同 normal（本域键集合由 agent 侧决定，不随数据量放大）；
//   error  —— 读写端点统一 500（由 mockGet / mockPut 的 error 场景注入）。
import { HttpResponse, type HttpHandler } from 'msw'
import type {
  AgentSettingItem,
  AgentSettingSource,
  AgentSettingsApprovalTicket,
  AgentSettingsResponse,
  AgentSettingsWriteResult,
  UpdateAgentSettingsBody,
} from '@beacon/contracts'
import { getClusterState } from '../data/cluster'
import { jsonError, mockGet, mockPut, queryStr, readBody } from '../http'
import { defineScenarioStore } from '../store'
import type { MockScenario } from '../scenario'
import { isoOffset } from '../support'

// 可下发键目录：与 agent 侧 AgentBootstrap 读取的键逐键对齐；desc 为控制面侧中文说明。
// valueType / dangerous 与真后端元数据同形（string-list 的分隔约定是逗号或换行，不是 JSON 数组）。
interface CatalogEntry {
  key: string
  desc: string
  valueType: 'int' | 'bool' | 'string' | 'string-list'
  dangerous?: boolean
}

const CATALOG: readonly CatalogEntry[] = [
  { key: 'timing.poll-timeout-ms', desc: '长轮询挂起上限（毫秒）', valueType: 'int' },
  { key: 'timing.request-timeout-ms', desc: '单次 HTTP 请求超时（毫秒）', valueType: 'int' },
  { key: 'timing.heartbeat-fallback-ms', desc: '心跳回退周期（毫秒）', valueType: 'int' },
  { key: 'backoff.initial-ms', desc: '重连退避初始间隔（毫秒）', valueType: 'int' },
  { key: 'backoff.max-ms', desc: '重连退避上限（毫秒）', valueType: 'int' },
  { key: 'snapshot.enabled', desc: '是否持久化生效配置快照', valueType: 'bool' },
  { key: 'file-tree.enabled', desc: '是否启用文件树下发', valueType: 'bool' },
  { key: 'messaging.enabled', desc: '是否启用跨服消息通道', valueType: 'bool' },
  { key: 'messaging.rpc-timeout-ms', desc: '消息 RPC 超时（毫秒）', valueType: 'int' },
  { key: 'assets.enabled', desc: '是否启用文件树扫描上报', valueType: 'bool' },
  { key: 'assets.scan-interval-sec', desc: '文件树扫描周期（秒）', valueType: 'int' },
  { key: 'delivery.config-reload-timeout-ms', desc: '配置热重载超时（毫秒）', valueType: 'int' },
  {
    key: 'override.command-whitelist',
    desc: '允许执行的控制台命令白名单（逗号或换行分隔的命令首 token）',
    valueType: 'string-list',
    dangerous: true,
  },
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
    // 空态：什么都没下发过，所有项 value 为空串、来源为 local-or-default
    // （不是「未设置」那种暗示配置缺失的档位，见契约 AgentSettingSource）
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

// 202 票据的状态注入位（随场景重置）：默认 pending，改它即可复现「旧票据已终结」。
// 服务端命中幂等键时回的是既有申请，其 status 可能是 rejected / expired / succeeded，
// 前端必须读 status 而不是只认 approvalRequestId。
interface TicketState {
  status: string
}

const getTicketState = defineScenarioStore<TicketState>(() => ({ status: 'pending' }))

/**
 * 测试 / 演示注入：让高危键下发的 202 票据返回指定状态
 * （`rejected` / `withdrawn` / `expired` / `failed` / `succeeded` / 任意未知值）。
 * 传空串恢复默认 pending。随 resetMockData / 场景切换复位。
 */
export function setAgentSettingsTicketStatus(status: string): void {
  getTicketState().status = status === '' ? 'pending' : status
}

/** 取当前注入的票据状态（测试断言用） */
export function agentSettingsTicketStatus(): string {
  return getTicketState().status
}

/** 作用域标识：`环境码` 或 `环境码/serverId`，与后端 agentSettingScopeLabel 逐字一致 */
function scopeLabelOf(namespaceId: number, serverId: string): string {
  const ns = getClusterState().namespaces.find((row) => row.id === namespaceId)
  const code = ns?.code ?? ns?.name ?? `ns-${String(namespaceId)}`
  return serverId === '' ? code : `${code}/${serverId}`
}

/** 组装某作用域的视图（GET 响应，同时也是 200 写结果里的 scope） */
function buildScopeView(namespaceId: number, serverId: string): AgentSettingsResponse {
  const state = getState()
  const namespaceLayer = state.namespaceValues.get(namespaceId) ?? {}
  const serverLayer = serverId === '' ? {} : (state.serverValues.get(`${String(namespaceId)}:${serverId}`) ?? {})

  const items: AgentSettingItem[] = CATALOG.map(({ key, desc, valueType, dangerous }) => {
    // known 恒为 true：mock 目录即控制面已知清单，未知键在 PUT 阶段就被 400 拒掉（与真后端一致）
    const meta = { valueType, known: true, ...(dangerous === true ? { dangerous: true } : {}) }
    if (key in serverLayer) {
      return { key, value: serverLayer[key], source: 'server' satisfies AgentSettingSource, desc, ...meta }
    }
    if (key in namespaceLayer) {
      return { key, value: namespaceLayer[key], source: 'namespace' satisfies AgentSettingSource, desc, ...meta }
    }
    // 未下发：控制面不知道本地生效值，故 value 为空串（不回填代码默认，避免误导成「已设置」）。
    // 来源如实标 local-or-default：本地链落哪一层（环境变量 / config.yml / 代码默认）无法区分。
    return { key, value: '', source: 'local-or-default' satisfies AgentSettingSource, desc, ...meta }
  })

  return { items, updatedAt: state.updatedAt, scopeLabel: scopeLabelOf(namespaceId, serverId) }
}

// GET /admin/v2/agent-settings?namespaceId=&serverId=
export const agentSettingsHandlers: HttpHandler[] = [
  mockGet('/admin/v2/agent-settings', ({ request }) => {
    const url = new URL(request.url)
    const namespaceIdRaw = queryStr(url, 'namespaceId')
    const namespaceId = namespaceIdRaw === null ? 0 : Number.parseInt(namespaceIdRaw, 10)
    if (!Number.isInteger(namespaceId) || namespaceId <= 0) {
      return jsonError(400, 'INVALID_PARAM', 'namespaceId 必填且须为正整数')
    }
    const serverId = (queryStr(url, 'serverId') ?? '').trim()
    return HttpResponse.json(buildScopeView(namespaceId, serverId))
  }),

  // PUT /admin/v2/agent-settings
  mockPut('/admin/v2/agent-settings', async ({ request }) => {
    const body = await readBody<UpdateAgentSettingsBody>(request)
    const namespaceId = typeof body.namespaceId === 'number' ? body.namespaceId : Number.NaN
    if (!Number.isInteger(namespaceId) || namespaceId <= 0) {
      return jsonError(400, 'INVALID_PARAM', 'namespaceId 必填且须为正整数')
    }
    const serverId = typeof body.serverId === 'string' ? body.serverId.trim() : ''

    // 二次确认先验：作用域错了就不该再费劲校验值——否则报错会指向「某个值不合法」，
    // 而真正的问题是改错了服。错误码与文案与后端 agentSettingConfirmMismatch 一致。
    const expectedScope = scopeLabelOf(namespaceId, serverId)
    const confirmationScope = typeof body.confirmationScope === 'string' ? body.confirmationScope.trim() : ''
    if (confirmationScope !== expectedScope) {
      return jsonError(
        400,
        'AGENT_SETTING_CONFIRM_MISMATCH',
        `二次确认不一致：confirmationScope 须与作用域标识逐字一致（本次应为 ${expectedScope}）`,
      )
    }

    const reason = typeof body.reason === 'string' ? body.reason.trim() : ''
    if (reason === '') {
      return jsonError(400, 'INVALID_PARAM', 'reason 必填（下发原因会写入审计）')
    }

    // 本次要写入的值 与 要撤销的键，分开收集：
    //   - 字符串 = 写入该层覆盖（不得为空，空值不等于撤销）
    //   - null   = 撤销该层覆盖（删除这一行，回退到上一级 / 本地配置 / 代码默认）
    const values: Record<string, string> = {}
    const revoked: string[] = []
    const rawValues: Record<string, unknown> = body.values ?? {}
    const entries = Object.entries(rawValues)
    if (entries.length === 0) {
      return jsonError(400, 'INVALID_PARAM', 'values 必填且至少包含一项')
    }
    for (const [key, value] of entries) {
      if (value === null) {
        revoked.push(key)
        continue
      }
      if (typeof value !== 'string') {
        return jsonError(400, 'INVALID_PARAM', `${key} 的值必须是字符串，或用 null 表示撤销该层覆盖`)
      }
      // 空值一律拒：agent 侧把空白值当「未下发」，存进去等于写了个永不生效的值
      if (value.trim() === '') {
        return jsonError(
          400,
          'INVALID_PARAM',
          `${key} 的值不能为空：空值不等于撤销（要撤销请用「撤销本层覆盖」）`,
        )
      }
      values[key] = value
    }

    const touchedKeys = [...Object.keys(values), ...revoked]
    const forbidden = touchedKeys.filter(isForbiddenKey)
    if (forbidden.length > 0) {
      return jsonError(400, 'FORBIDDEN_AGENT_SETTING_KEY', `以下键不允许控制面下发：${forbidden.join(', ')}`)
    }
    // 未知键一律 400（真后端 normalizeAgentSettingsWrite 对 known=false 的键同样拒）：
    // 前端放行、后端拒绝的键如果 mock 收了，就测不出「改了个永远写不进去的键」
    const unknown = touchedKeys.filter((key) => !CATALOG.some((entry) => entry.key === key))
    if (unknown.length > 0) {
      return jsonError(400, 'UNKNOWN_AGENT_SETTING_KEY', `以下键不在控制面已知清单内：${unknown.join(', ')}`)
    }

    const state = getState()
    const before = buildScopeView(namespaceId, serverId)
    const beforeValueByKey = new Map(before.items.map((item) => [item.key, item.value]))

    // 白名单变更走审批：批准前不落库（真后端由审批执行器在批准后写入）。
    // 撤销白名单同样算能力边界变更，一并走审批。
    const needsApproval = 'override.command-whitelist' in values || revoked.includes('override.command-whitelist')
    if (needsApproval) {
      return HttpResponse.json(
        {
          approvalRequestId: `apr_agentset_${crypto.randomUUID().slice(0, 8)}`,
          // 注入位：复现「命中幂等键、回的是早已终结的旧票据」
          status: getTicketState().status,
          operationKey: 'agent_setting.dangerous',
          secretReturned: false,
        } satisfies AgentSettingsApprovalTicket,
        { status: 202 },
      )
    }

    // 落库：写入新值 + 删除被撤销的键（真后端把 null 解释为删除该作用域的行）。
    // 用重建成对过滤而不是动态 delete：后者被 lint 禁止，且重建更贴合「这一层整体替换」的语义。
    const applyLayer = (layer: Record<string, string>): Record<string, string> => {
      const revokedSet = new Set(revoked)
      const next = Object.fromEntries(
        Object.entries(layer).filter(([key]) => !revokedSet.has(key)),
      )
      return { ...next, ...values }
    }
    if (serverId === '') {
      state.namespaceValues.set(namespaceId, applyLayer(state.namespaceValues.get(namespaceId) ?? {}))
    } else {
      const layerKey = `${String(namespaceId)}:${serverId}`
      state.serverValues.set(layerKey, applyLayer(state.serverValues.get(layerKey) ?? {}))
    }
    state.updatedAt = isoOffset(0)

    // 200 的真实形状：写入后的作用域视图 + 三类变更集（**没有** ok 字段）
    const applied = Object.keys(values).filter((key) => beforeValueByKey.get(key) !== values[key])
    const unchanged = Object.keys(values).filter((key) => beforeValueByKey.get(key) === values[key])
    // removed 只记「本来真有这一层、撤销后才真的少了」的键：
    // 撤销一个本就没有覆盖的键是无操作，不该出现在变更集里
    const layerAfter = new Map(buildScopeView(namespaceId, serverId).items.map((item) => [item.key, item.value]))
    const removed = revoked.filter((key) => layerAfter.get(key) === '' && beforeValueByKey.get(key) !== '')
    return HttpResponse.json({
      scope: buildScopeView(namespaceId, serverId),
      applied,
      removed,
      unchanged,
    } satisfies AgentSettingsWriteResult)
  }),
]
