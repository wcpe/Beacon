// 系统域 mock：控制面健康（/system）、版本与更新（/system/version）、密钥（/api-keys）、运维设置（/settings）。
// v2 草案未覆盖本域，全部沿用 docs/API.md Legacy /admin/v1 契约形状（路径保持 /admin/v1/...）：
// system/status、system/observability、update-check、update 进度 / 触发 / 取消、rollback、proxy-test、
// api-keys 列表 / 创建 / 吊销 / 重置、settings 列表 / 单项修改。

import { http, HttpResponse, type HttpHandler } from 'msw'
import type {
  ApiKeyItem,
  SettingItem,
  SystemObservability,
  SystemStatus,
  UpdateCheck,
  UpdateProgress,
} from '@beacon/contracts'
import { jsonError, mockDelete, mockGet, mockPost, mockPut, pathParam, readBody } from '../http'
import { getClusterState } from '../data/cluster'
import { getMockScenario, type MockScenario } from '../scenario'
import { defineScenarioStore } from '../store'
import { isoOffset, pseudoSha256 } from '../support'

interface SystemState {
  update: UpdateProgress
  apiKeys: ApiKeyItem[]
  nextKeyId: number
  settings: SettingItem[]
}

const DAY = 86_400_000

// Legacy 热改项白名单（docs/API.md 运维设置 37 项）
const SETTING_SEEDS: [string, string, SettingItem['valueType'], string][] = [
  ['health.degraded-after-sec', '15', 'int', '超过多少秒未心跳降级 degraded'],
  ['health.ttl-sec', '30', 'int', '超过多少秒未心跳判失联'],
  ['health.offline-grace-sec', '300', 'int', '失联多久后转 offline'],
  ['health.scan-interval-sec', '5', 'int', '健康扫描周期（秒）'],
  ['metric.enabled', 'true', 'bool', '是否启用负载指标采样器'],
  ['metric.sample-interval-sec', '10', 'int', '指标采样周期（秒）'],
  ['metric.retention-hours', '72', 'int', '指标内存保留时长（小时）'],
  ['longpoll.max-hold-ms', '30000', 'int', '长轮询最大挂起毫秒'],
  ['alert.webhook-url', '', 'string', '告警 webhook 地址（空 = 不推送）'],
  ['alert.webhook-timeout-ms', '3000', 'int', '告警 webhook 超时毫秒'],
  ['log.level', 'INFO', 'string', '日志级别（ERROR|WARN|INFO|DEBUG）'],
  ['reverse-fetch.max-file-bytes', '1048576', 'int', '反向抓取单文件上限（字节）'],
  ['update.proxy-url', '', 'string', '更新出站代理（含凭据回显脱敏）'],
  ['update.channel', 'stable', 'string', '更新渠道（stable，仅合法 GA）'],
  ['update.auto-check-enabled', 'true', 'bool', '是否自动检查更新'],
  ['update.check-interval-hours', '6', 'int', '自动检查更新周期（小时）'],
  ['undo.window-hours', '24', 'int', '配置操作可撤回时间窗（小时）'],
  ['identity.conflict-window-sec', '600', 'int', '并发身份冲突检测窗口（秒）'],
  ['delivery.approver-separation-enabled', 'true', 'bool', '是否启用变更单审批职责分离'],
  ['delivery.blob-retention-days', '7', 'int', '交付中转 blob 保留天数'],
  ['delivery.blob-capacity-bytes', '21474836480', 'int', '交付中转存储容量上限（字节）'],
  ['delivery.upload-concurrency', '4', 'int', '交付流式上传全局并发上限'],
  ['delivery.download-concurrency', '64', 'int', '交付流式下载全局并发上限'],
  ['delivery.cleanup-interval-minutes', '60', 'int', '交付中转 blob 后台清理周期（分钟）'],
  ['archive.retention-days.metric-sample', '14', 'int', '指标采样热库保留天数（超期归档，下限 7）'],
  ['archive.retention-days.health-snapshot', '30', 'int', '健康快照热库保留天数（下限 7）'],
  ['archive.retention-days.sched-decision', '60', 'int', '调度决策热库保留天数（下限 7）'],
  ['archive.retention-days.conn-detail', '60', 'int', '连接明细热库保留天数（下限 7）'],
  ['archive.retention-days.msg-trace', '60', 'int', '消息链路热库保留天数（下限 7）'],
  ['archive.retention-days.msg-payload', '30', 'int', '消息载荷热库保留天数（下限 7）'],
  ['archive.retention-days.audit', '180', 'int', '审计日志热库保留天数（下限 7）'],
  ['archive.auto-enabled', 'true', 'bool', '是否启用每日自动归档'],
  ['archive.schedule-hour-utc', '4', 'int', '每日自动归档触发时刻（UTC 小时，0-23）'],
  ['archive.batch-rows', '1000', 'int', '归档搬迁每批行数（1-100000）'],
  ['archive.batch-interval-ms', '200', 'int', '归档批次间隔毫秒（0-60000）'],
  ['archive.verify-sample-size', '100', 'int', '删除前 sha256 抽样校验行数（1-10000）'],
  ['archive.cold-query-max-days', '31', 'int', '冷查询单次最大时间跨度（天，1-366）'],
]

function buildSystem(scenario: MockScenario): SystemState {
  const settings: SettingItem[] = SETTING_SEEDS.map(([key, value, valueType, desc]) => ({
    key,
    value,
    valueType,
    default: value,
    desc,
    isStartup: false,
  }))
  const apiKeys: ApiKeyItem[] =
    scenario === 'empty'
      ? []
      : [
          {
            id: 1,
            name: '运维脚本（巡检）',
            role: 'readonly',
            keyPrefix: 'bk_9f2a',
            status: 'active',
            createdAt: isoOffset(-60 * DAY),
            expiresAt: null,
            lastUsedAt: isoOffset(-3_600_000),
          },
          {
            id: 2,
            name: '业务管理后端',
            role: 'full',
            keyPrefix: 'bk_c81d',
            status: 'active',
            createdAt: isoOffset(-30 * DAY),
            expiresAt: isoOffset(150 * DAY),
            lastUsedAt: isoOffset(-120_000),
          },
          {
            id: 3,
            name: '旧压测脚本',
            role: 'readonly',
            keyPrefix: 'bk_77e0',
            status: 'revoked',
            createdAt: isoOffset(-120 * DAY),
            expiresAt: null,
            lastUsedAt: isoOffset(-90 * DAY),
          },
        ]
  return {
    update: {
      phase: 'idle',
      percent: 0,
      targetVersion: '',
      error: '',
      rollbackAvailable: scenario !== 'empty',
    },
    apiKeys,
    nextKeyId: apiKeys.length + 1,
    settings,
  }
}

const getSystemState: () => SystemState = defineScenarioStore(buildSystem)

export const systemHandlers: HttpHandler[] = [
  // 控制面自身状态（版本 / 运行时长 / DB 连通 / 在线实例数 / Go 运行时）
  mockGet('/admin/v1/system/status', () => {
    const cluster = getClusterState()
    const online = cluster.servers.filter((s) => s.online).length
    const status: SystemStatus = {
      version: 'v0.21.0',
      startedAt: isoOffset(-36 * 3_600_000),
      uptimeSeconds: 36 * 3600,
      db: { connected: true },
      onlineInstances: online,
      samplerEnabled: true,
      runtime: { goroutines: 42 + online, heapAlloc: 134_217_728, heapSys: 268_435_456 },
      cpuAvailable: true,
      cpuPercent: 23.4,
    }
    return HttpResponse.json(status)
  }),

  // 控制面内部运行态快照（连接池 / 长轮询 / 注册表 / 命令队列）
  mockGet('/admin/v1/system/observability', () => {
    const cluster = getClusterState()
    const online = cluster.servers.filter((s) => s.online).length
    const lost = cluster.servers.filter((s) => !s.online).length
    const registryByStatus: Record<string, number> = {}
    if (online > 0) {
      registryByStatus.online = online
    }
    if (lost > 0) {
      registryByStatus.lost = lost
    }
    const observability: SystemObservability = {
      dbPool: {
        maxOpenConnections: 20,
        openConnections: Math.min(20, 3 + Math.floor(online / 100)),
        inUse: 2,
        idle: 1 + Math.floor(online / 200),
        waitCount: 0,
        waitDurationMs: 0,
      },
      longpoll: {
        config: Math.floor(online / 2),
        file: Math.floor(online / 4),
        topology: 1,
        command: Math.floor(online / 3),
        total: Math.floor(online / 2) + Math.floor(online / 4) + 1 + Math.floor(online / 3),
      },
      registryByStatus,
      registryTotal: cluster.servers.length,
      commandByStatus: cluster.servers.length === 0 ? {} : { pending: 2, fetched: 1, done: 40, failed: 3 },
    }
    return HttpResponse.json(observability)
  }),

  // 更新检查自行把 error 场景降级为 200，因此不走 mockGet 的统一 500 包装。
  http.get('*/admin/v1/system/update-check', () => {
    const scenario = getMockScenario()
    const failed = scenario === 'error'
    const hasUpdate = !failed && scenario !== 'empty'
    const check: UpdateCheck = {
      status: failed ? 'check-failed' : 'ok',
      failureReason: failed ? '查 release 列表失败: proxyconnect tcp 10.0.0.5:7890: connection refused' : undefined,
      currentVersion: 'v0.21.0',
      channel: 'stable',
      hasUpdate,
      isDevBuild: false,
      latestVersion: failed ? '' : hasUpdate ? 'v0.22.0' : 'v0.21.0',
      releaseNotes: hasUpdate ? '## 变更\n- 第二版管理台 mock 全量上线\n- 修复若干问题' : '',
      releaseUrl: hasUpdate ? 'https://github.com/wcpe/Beacon/releases/tag/v0.22.0' : '',
      publishedAt: failed ? '' : isoOffset(-2 * DAY),
      checkedAt: isoOffset(0),
      cacheExpiresAt: isoOffset(6 * 3_600_000),
    }
    return HttpResponse.json(check)
  }),

  // 读更新进度内存态
  mockGet('/admin/v1/system/update', () => {
    return HttpResponse.json(getSystemState().update)
  }),

  // 代理连通性诊断
  mockGet('/admin/v1/system/proxy-test', () => {
    return HttpResponse.json({ ok: true })
  }),

  // 触发应用更新（异步受理，进度经 GET 轮询；进行中再触发 409）
  mockPost('/admin/v1/system/update', () => {
    const state = getSystemState()
    if (state.update.phase !== 'idle' && state.update.phase !== 'failed') {
      return jsonError(409, 'UPDATE_IN_PROGRESS', '已有更新进行中')
    }
    state.update = {
      phase: 'downloading',
      percent: 42,
      targetVersion: 'v0.22.0',
      error: '',
      rollbackAvailable: state.update.rollbackAvailable,
    }
    return HttpResponse.json({ accepted: true }, { status: 202 })
  }),

  // 取消进行中的更新下载（幂等）
  mockPost('/admin/v1/system/update/cancel', () => {
    const state = getSystemState()
    if (state.update.phase === 'downloading' || state.update.phase === 'verifying' || state.update.phase === 'staging') {
      state.update = { ...state.update, phase: 'idle', percent: 0, targetVersion: '', error: '' }
      return HttpResponse.json({ cancelled: true }, { status: 202 })
    }
    return HttpResponse.json({ cancelled: false })
  }),

  // 回退到上一版本（无 .old 可退 → 409）
  mockPost('/admin/v1/system/rollback', () => {
    const state = getSystemState()
    if (!state.update.rollbackAvailable) {
      return jsonError(409, 'NO_ROLLBACK_AVAILABLE', '不存在可回退的上一版本备份')
    }
    return HttpResponse.json({ accepted: true }, { status: 202 })
  }),

  // API 密钥列表（含已吊销，无明文）
  mockGet('/admin/v1/api-keys', () => {
    return HttpResponse.json({ items: getSystemState().apiKeys })
  }),

  // 创建密钥：响应含一次性明文 key
  mockPost('/admin/v1/api-keys', async ({ request }) => {
    const body = await readBody<{ name?: string; role?: 'full' | 'readonly'; expiresAt?: string }>(request)
    if (!body.name || (body.role !== 'full' && body.role !== 'readonly')) {
      return jsonError(400, 'INVALID_PARAM', 'name / role 非法')
    }
    const state = getSystemState()
    const id = state.nextKeyId
    state.nextKeyId += 1
    const plaintext = `bk_${pseudoSha256(`api-key:${String(id)}:${body.name}`).slice(0, 40)}`
    const item: ApiKeyItem = {
      id,
      name: body.name,
      role: body.role,
      keyPrefix: plaintext.slice(0, 7),
      status: 'active',
      createdAt: isoOffset(0),
      expiresAt: body.expiresAt ?? null,
      lastUsedAt: null,
    }
    state.apiKeys.push(item)
    return HttpResponse.json({ ...item, key: plaintext }, { status: 201 })
  }),

  // 吊销密钥（软删，不可逆）
  mockDelete('/admin/v1/api-keys/:id', (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const item = getSystemState().apiKeys.find((k) => k.id === id)
    if (!item || item.status === 'revoked') {
      return jsonError(404, 'API_KEY_NOT_FOUND', '密钥不存在或已吊销')
    }
    item.status = 'revoked'
    return HttpResponse.json({ ok: true })
  }),

  // 重置密钥（轮换明文，旧明文立即失效）
  mockPost('/admin/v1/api-keys/:id/reset', (info) => {
    const id = Number.parseInt(pathParam(info, 'id'), 10)
    const item = getSystemState().apiKeys.find((k) => k.id === id)
    if (!item || item.status === 'revoked') {
      return jsonError(404, 'API_KEY_NOT_FOUND', '密钥不存在或已吊销')
    }
    const plaintext = `bk_${pseudoSha256(`api-key-reset:${String(id)}:${isoOffset(0)}`).slice(0, 40)}`
    item.keyPrefix = plaintext.slice(0, 7)
    return HttpResponse.json({ ...item, key: plaintext })
  }),

  // 全部热改项当前值（含凭据项回显脱敏）
  mockGet('/admin/v1/settings', () => {
    const items = getSystemState().settings.map((item) => {
      if (item.key === 'update.proxy-url' && item.value !== '') {
        // 凭据项回显脱敏：userinfo 段掩为 ***
        return { ...item, value: item.value.replace(/\/\/[^@]+@/, '//***:***@') }
      }
      return item
    })
    return HttpResponse.json({ items })
  }),

  // 修改单个热改项（白名单外 400；类型校验）
  mockPut('/admin/v1/settings/:key', async (info) => {
    const key = pathParam(info, 'key')
    const state = getSystemState()
    const item = state.settings.find((s) => s.key === key)
    if (!item) {
      return jsonError(400, 'SETTING_KEY_NOT_ALLOWED', `设置项 ${key} 不在热改白名单内`)
    }
    const body = await readBody<{ value?: string }>(info.request)
    if (typeof body.value !== 'string') {
      return jsonError(400, 'SETTING_VALUE_INVALID', 'value 必填（字符串化值）')
    }
    if (item.valueType === 'int' && Number.isNaN(Number.parseInt(body.value, 10))) {
      return jsonError(400, 'SETTING_VALUE_INVALID', `设置项 ${key} 需要整数值`)
    }
    if (item.valueType === 'bool' && body.value !== 'true' && body.value !== 'false') {
      return jsonError(400, 'SETTING_VALUE_INVALID', `设置项 ${key} 需要 true/false`)
    }
    item.value = body.value
    return HttpResponse.json({ ok: true })
  }),
]
