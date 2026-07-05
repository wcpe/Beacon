import { describe, expect, it } from 'vitest'
import type { InstanceView } from '@/api/types'
import * as filters from './instanceFiltering'
import type { FileSyncTargetStatus, FileSyncTargetView } from '@/api/types'
import {
  filterInstancesByKeyword,
  getVisibleWindow,
  prioritizeInstances,
} from './instanceFiltering'

type FileSyncTargetFilterApi = {
  filterFileSyncTargets?: (
    targets: FileSyncTargetView[],
    options: {
      keyword?: string
      status?: FileSyncTargetStatus | 'all'
      failedFirst?: boolean
    },
  ) => FileSyncTargetView[]
  mergeSelectedIds?: (current: string[], ids: Iterable<string>) => string[]
  removeSelectedIds?: (current: string[], ids: Iterable<string>) => string[]
}

function inst(partial: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'srv-0001',
    role: 'bukkit',
    group: 'server-a',
    zone: 'zone-01',
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.20.1',
    agentVersion: '0.12.0',
    status: 'online',
    capacity: 100,
    weight: 100,
    metadata: {},
    lastHeartbeat: '2026-01-01T00:00:00Z',
    lastHeartbeatAgeSec: 1,
    healthReason: '',
    appliedMd5: 'md5',
    playerCount: 0,
    tps: 20,
    backends: [],
    zoneDefaultEntry: false,
    proxy: {
      onlineConnections: 0,
      threadCount: 0,
      uptimeMs: 0,
      backendUp: 0,
      backendTotal: 0,
      backendAvgLatencyMs: 0,
    },
    registeredAt: '2026-01-01T00:00:00Z',
    ...partial,
  }
}

function target(
  partial: Partial<FileSyncTargetView> & Pick<FileSyncTargetView, 'serverId'>,
): FileSyncTargetView {
  return {
    taskId: 'task-1',
    batchNo: 1,
    serverId: partial.serverId,
    namespace: 'prod',
    group: 'server-a',
    zone: 'zone-01',
    status: 'pending',
    backupPath: '',
    currentFileCount: 0,
    changedFileCount: 0,
    skippedFileCount: 0,
    bytesTotal: 0,
    bytesDone: 0,
    error: '',
    updatedAt: '2026-01-01T00:00:00Z',
    ...partial,
  }
}

describe('instanceFiltering', () => {
  it('按 serverId、IP、大区和小区做大小写无关搜索', () => {
    const list = [
      inst({ serverId: 'alpha-01', address: '10.1.0.8:25565', group: 'g-a', zone: 'z-a' }),
      inst({ serverId: 'beta-02', address: '10.2.0.9:25565', group: 'g-b', zone: 'z-b' }),
    ]

    expect(filterInstancesByKeyword(list, 'ALPHA').map((i) => i.serverId)).toEqual(['alpha-01'])
    expect(filterInstancesByKeyword(list, '10.2.0.9').map((i) => i.serverId)).toEqual(['beta-02'])
    expect(filterInstancesByKeyword(list, 'z-a').map((i) => i.serverId)).toEqual(['alpha-01'])
  })

  it('状态墙优先展示异常服务器，再按 serverId 稳定排序', () => {
    const list = [
      inst({ serverId: 'online-2', status: 'online' }),
      inst({ serverId: 'lost-1', status: 'lost' }),
      inst({ serverId: 'degraded-1', status: 'degraded' }),
      inst({ serverId: 'online-1', status: 'online' }),
    ]

    expect(prioritizeInstances(list).map((i) => i.serverId)).toEqual([
      'lost-1',
      'degraded-1',
      'online-1',
      'online-2',
    ])
  })

  it('无搜索时限制初始渲染，有搜索时展示匹配窗口', () => {
    const list = Array.from({ length: 5 }, (_, i) => inst({ serverId: `srv-${i}` }))

    expect(getVisibleWindow(list, 2).items.map((i) => i.serverId)).toEqual(['srv-0', 'srv-1'])
    expect(getVisibleWindow(filterInstancesByKeyword(list, 'srv-4'), 2)).toMatchObject({
      total: 1,
      hidden: 0,
    })
  })

  it('文件同步目标明细支持 serverId 搜索、状态筛选和失败优先', () => {
    const api = filters as unknown as FileSyncTargetFilterApi
    expect(api.filterFileSyncTargets).toBeTypeOf('function')
    const rows = [
      target({ serverId: 'srv-003', status: 'succeeded' }),
      target({ serverId: 'srv-001', status: 'failed' }),
      target({ serverId: 'api-002', status: 'failed' }),
    ]

    const result = api.filterFileSyncTargets!(rows, {
      keyword: 'srv',
      status: 'all',
      failedFirst: true,
    })

    expect(result.map((row) => row.serverId)).toEqual(['srv-001', 'srv-003'])
    expect(
      api.filterFileSyncTargets!(rows, { status: 'failed' }).map((row) => row.serverId),
    ).toEqual(['srv-001', 'api-002'])
  })

  it('目标选择集合支持追加去重与按当前筛选结果清除', () => {
    const api = filters as unknown as FileSyncTargetFilterApi
    expect(api.mergeSelectedIds).toBeTypeOf('function')
    expect(api.removeSelectedIds).toBeTypeOf('function')

    const merged = api.mergeSelectedIds!(['target-01'], ['target-01', 'target-02', 'target-03'])
    expect(merged).toEqual(['target-01', 'target-02', 'target-03'])
    expect(api.removeSelectedIds!(merged, ['target-02'])).toEqual(['target-01', 'target-03'])
  })
})
