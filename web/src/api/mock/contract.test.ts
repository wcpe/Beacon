/**
 * 防漂移契约测试
 *
 * 目的：驱动 client.ts 的**每个**端点过 mock，断言没有任何请求落到「未注册的 mock 端点」
 * （handlers.ts 兜底返回带特征码 UNROUTED_CODE 的 404）。
 * 以后给 client 新增端点却漏配 mock handler → 该请求落兜底 → 本测试变红，挡住漂移。
 *
 * 实现：包裹 window.fetch 监听所有 /admin/v1/* 响应；凡 body.code===UNROUTED_CODE 即记为「漏配」。
 * 业务 404（NOT_FOUND，如查不存在的 id）属正常分支，不计漏配。
 */

import { afterAll, beforeAll, describe, expect, it } from 'vitest'
import * as client from '../client'
import { enableMock } from './index'
import { UNROUTED_CODE } from './handlers'
import { setAuth } from '../../state/auth'

// 记录所有落到 mock 兜底（漏配）的请求；每个用例前清空。
const unrouted: string[] = []

// 包裹真实 fetch（mock 已接管 window.fetch）：解析 /admin/v1/* 响应体，捕获 UNROUTED_CODE。
function installSniffer(): void {
  const inner = window.fetch
  window.fetch = async (input: Parameters<typeof fetch>[0], init?: RequestInit) => {
    const resp = await inner(input, init)
    const url =
      typeof input === 'string' ? input : input instanceof URL ? input.toString() : input.url
    if (url.includes('/admin/v1/') && resp.status === 404) {
      // 克隆后读 body，避免消费掉调用方要用的响应流
      try {
        const data = (await resp.clone().json()) as { code?: string }
        if (data.code === UNROUTED_CODE) {
          const method = (init?.method ?? 'GET').toUpperCase()
          unrouted.push(`${method} ${url}`)
        }
      } catch {
        // 非 JSON 404：与漏配无关，忽略
      }
    }
    return resp
  }
}

// 吞掉业务异常（如 400/404 NOT_FOUND）——本测试只关心「是否漏配 mock 路由」，不关心业务成败。
async function swallow(promise: Promise<unknown>): Promise<void> {
  try {
    await promise
  } catch {
    // 业务错误（凭据/参数/不存在等）不影响漏配判定
  }
}

beforeAll(() => {
  enableMock()
  installSniffer()
  setAuth('mock-token', 'admin') // 让带令牌的请求成形（mock 不校验令牌）
})

afterAll(() => {
  // 注：mock 接管的 window.fetch 由进程退出回收；测试间无需还原
})

// 覆盖 client.ts 全部端点的调用清单（含两个走原生 fetch 的 importFiles / exportAudits）。
// 用既有 mock 数据里存在的 id（配置 1、文件 1、覆盖集 1、任务 5000、忽略规则 7000、可逆操作 9000）。
const calls: Array<[string, () => Promise<unknown>]> = [
  // 登录 / 身份
  ['login', () => client.login('admin', 'pw')],
  ['logout', () => client.logout()],
  // 环境
  ['listNamespaces', () => client.listNamespaces()],
  ['createNamespace', () => client.createNamespace('demo-ns', '演示环境')],
  ['updateNamespace', () => client.updateNamespace('demo-ns', '改名')],
  ['deleteNamespace', () => client.deleteNamespace('demo-ns')],
  // 配置
  ['listConfigs', () => client.listConfigs({ namespace: 'prod' })],
  ['getConfig', () => client.getConfig(1)],
  [
    'createConfig',
    () =>
      client.createConfig({
        namespace: 'prod',
        group: '__GLOBAL__',
        dataId: 'contract.yml',
        scopeLevel: 'global',
        scopeTarget: '',
        format: 'yaml',
        content: 'a: 1\n',
        comment: '契约测试新建',
      }),
  ],
  ['publishConfig', () => client.publishConfig(1, 'b: 2\n', '契约发布')],
  ['deleteConfig', () => client.deleteConfig(5, '契约删除')],
  ['listRevisions', () => client.listRevisions(1)],
  ['getRevision', () => client.getRevision(1, 1)],
  ['rollbackConfig', () => client.rollbackConfig(1, 1, '契约回滚')],
  ['diffConfig', () => client.diffConfig(1, 1, 2)],
  ['batchConfigs', () => client.batchConfigs('disable', [2])],
  ['effectiveConfig', () => client.effectiveConfig({ namespace: 'prod' })],
  ['impactPreview', () => client.impactPreview({ namespace: 'prod', scopeLevel: 'global' })],
  ['effectiveFiles', () => client.effectiveFiles({ namespace: 'prod' })],
  // 实例与健康
  ['listInstances', () => client.listInstances({ namespace: 'prod' })],
  [
    'serverConfigTimeline',
    () => client.serverConfigTimeline({ serverId: 'server-01', namespace: 'prod' }),
  ],
  ['listOfflineInstances', () => client.listOfflineInstances('prod')],
  ['offlineInstance', () => client.offlineInstance('server-02', 'prod', '维护')],
  ['onlineInstance', () => client.onlineInstance('server-02', 'prod')],
  ['listDrains', () => client.listDrains('prod')],
  ['drainInstance', () => client.drainInstance('server-01', 'prod', '排空')],
  ['undrainInstance', () => client.undrainInstance('server-01', 'prod')],
  ['getTopology', () => client.getTopology('prod')],
  // 指标
  ['metricsSummary', () => client.metricsSummary('prod')],
  ['metricsTrend', () => client.metricsTrend({ namespace: 'prod', window: '1h' })],
  // 控制面状态 / 更新
  ['systemStatus', () => client.systemStatus()],
  ['systemObservability', () => client.systemObservability()],
  ['checkUpdate', () => client.checkUpdate()],
  ['updateProgress', () => client.updateProgress()],
  ['triggerUpdate', () => client.triggerUpdate()],
  ['testProxy', () => client.testProxy()],
  ['cancelUpdate', () => client.cancelUpdate()],
  ['rollbackUpdate', () => client.rollbackUpdate()],
  // zone 分配
  ['listAssignments', () => client.listAssignments('prod')],
  [
    'assignZone',
    () =>
      client.assignZone({
        namespace: 'prod',
        serverId: 'server-02',
        group: 'server-a',
        zone: 'zone-01',
        note: '契约改派',
      }),
  ],
  ['unassignZone', () => client.unassignZone('prod', 'server-02')],
  ['zoneSummary', () => client.zoneSummary('prod')],
  ['listDefaultEntries', () => client.listDefaultEntries('prod')],
  // 审计 / 告警 / 分析
  ['listAudits', () => client.listAudits({ namespace: 'prod', page: 1, size: 20 })],
  ['exportAudits', () => client.exportAudits({ namespace: 'prod' }, 'csv')],
  ['listAlertEvents', () => client.listAlertEvents({ namespace: 'prod' })],
  ['getAuditAnalytics', () => client.getAuditAnalytics({ namespace: 'prod' })],
  ['listCommands', () => client.listCommands({ namespace: 'prod' })],
  ['getCommandAnalytics', () => client.getCommandAnalytics({ namespace: 'prod' })],
  // API 密钥
  ['listApiKeys', () => client.listApiKeys()],
  ['createApiKey', () => client.createApiKey({ name: '契约密钥', role: 'readonly' })],
  ['resetApiKey', () => client.resetApiKey(1)],
  ['revokeApiKey', () => client.revokeApiKey(1)],
  // 文件树托管
  ['listFiles', () => client.listFiles({ namespace: 'prod' })],
  ['getFile', () => client.getFile(1)],
  [
    'createFile',
    () =>
      client.createFile({
        namespace: 'prod',
        group: '__GLOBAL__',
        path: 'plugins/contract/x.yml',
        scopeLevel: 'global',
        scopeTarget: '',
        content: 'k: v\n',
        comment: '契约建文件',
      }),
  ],
  ['publishFile', () => client.publishFile(1, 'k: v2\n', '契约发布')],
  ['deleteFile', () => client.deleteFile(2, '契约删除')],
  ['listFileRevisions', () => client.listFileRevisions(1)],
  ['getFileRevision', () => client.getFileRevision(1, 1)],
  ['rollbackFile', () => client.rollbackFile(1, 1, '契约回滚')],
  [
    'importFiles',
    () =>
      client.importFiles('prod', '__GLOBAL__', [{ path: 'a.yml', file: new File(['x'], 'a.yml') }]),
  ],
  [
    'triggerReverseFetch',
    () => client.triggerReverseFetch('server-01', 'prod', { scope: 'group', group: 'server-a' }),
  ],
  ['browse', () => client.browse('server-01', 'prod', { op: 'tree' })],
  ['triggerImprint', () => client.triggerImprint('server-01', 'prod', { path: 'x.yml' })],
  ['imprintStatus', () => client.imprintStatus(1)],
  ['imprintDiff', () => client.imprintDiff(1, { scope: 'server' })],
  [
    'confirmImprint',
    () => client.confirmImprint(1, { scope: 'server', reviewedMd5: 'aaaaaaaa1111' }),
  ],
  ['requestAgentLogs', () => client.requestAgentLogs('server-01', 'prod')],
  ['getAgentLogs', () => client.getAgentLogs('server-01', 'prod')],
  ['triggerResync', () => client.triggerResync('server-01', 'prod')],
  // 覆盖集
  ['listOverrideSets', () => client.listOverrideSets({ namespace: 'prod' })],
  ['getOverrideSet', () => client.getOverrideSet(1)],
  ['publishOverrideSet', () => client.publishOverrideSet(1, '/plugins/x', '/reload', '契约发布')],
  ['listOverrideSetRevisions', () => client.listOverrideSetRevisions(1)],
  ['rollbackOverrideSet', () => client.rollbackOverrideSet(1, 1, '契约回滚')],
  ['dryRunOverrideSet', () => client.dryRunOverrideSet(1)],
  ['deleteOverrideSet', () => client.deleteOverrideSet(1, '契约删除')],
  // 反向抓取受管任务 + 冲突 + 忽略规则
  [
    'createScanTask',
    () => client.createScanTask('server-01', 'prod', { scope: 'group', group: 'server-a' }),
  ],
  ['listReverseFetchTasks', () => client.listReverseFetchTasks({ namespace: 'prod' })],
  ['getReverseFetchTask', () => client.getReverseFetchTask(5000)],
  [
    'submitReverseFetchTask',
    () =>
      client.submitReverseFetchTask(5000, {
        selectedPaths: ['spawn.yml'],
        confirmOverThreshold: false,
      }),
  ],
  ['cancelReverseFetchTask', () => client.cancelReverseFetchTask(5000)],
  ['listConflicts', () => client.listConflicts(5000)],
  ['conflictDiff', () => client.conflictDiff(5000, 'spawn.yml')],
  [
    'resolveConflicts',
    () => client.resolveConflicts(5000, [{ path: 'spawn.yml', action: 'keep' }]),
  ],
  ['listIgnoreRules', () => client.listIgnoreRules({ namespace: 'prod' })],
  [
    'createIgnoreRule',
    () =>
      client.createIgnoreRule({
        namespace: 'prod',
        scope: 'group',
        group: 'server-a',
        ruleType: 'exact',
        pattern: 'x.yml',
      }),
  ],
  ['deleteIgnoreRule', () => client.deleteIgnoreRule(7000)],
  // 运维设置
  ['listSettings', () => client.listSettings()],
  ['updateSetting', () => client.updateSetting('health.ttl-sec', '40')],
  // 操作级撤回
  ['listReversibleOperations', () => client.listReversibleOperations({ namespace: 'prod' })],
  ['undoReversibleOperation', () => client.undoReversibleOperation(9000)],
]

describe('mock 契约：client 每个端点都有对应 mock handler', () => {
  it.each(calls)('%s 不落到未注册端点', async (_name, fn) => {
    unrouted.length = 0
    await swallow(fn())
    expect(unrouted).toEqual([])
  })

  it('清单覆盖 client.ts 导出的全部端点函数（防漏列）', async () => {
    // client 中非端点的导出（工具 / 错误类 / 类型）：从断言集合剔除。
    const nonEndpoints = new Set(['setOnUnauthorized', 'ApiClientError'])
    const exported = Object.keys(client).filter(
      (k) => typeof (client as Record<string, unknown>)[k] === 'function' && !nonEndpoints.has(k),
    )
    const covered = new Set(calls.map(([name]) => name))
    const missing = exported.filter((name) => !covered.has(name))
    expect(missing).toEqual([])
  })
})
