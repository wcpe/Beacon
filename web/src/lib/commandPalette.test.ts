// 命令面板纯逻辑单测（FR-83）：穷举 buildItems / filterItems / groupItems 的归一、过滤、分组行为。
import { describe, it, expect } from 'vitest'
import type { ConfigView, FileView, InstanceView } from '@/api/types'
import {
  buildItems,
  filterItems,
  groupItems,
  type BuildSources,
} from './commandPalette'

// 最小可用的视图样例工厂（仅填命令面板用到的字段）
function cfg(id: number, dataId: string, namespace = 'prod', group = 'g1'): ConfigView {
  return {
    id,
    namespace,
    group,
    dataId,
    scopeLevel: 'group',
    scopeTarget: group,
    format: 'yaml',
    version: 1,
    md5: 'x',
    enabled: true,
    updatedAt: '',
  }
}

function file(id: number, path: string): FileView {
  return {
    id,
    namespace: 'prod',
    group: 'g1',
    path,
    scopeLevel: 'group',
    scopeTarget: 'g1',
    version: 1,
    md5: 'x',
    enabled: true,
    updatedAt: '',
  }
}

function inst(serverId: string, role = 'bukkit'): InstanceView {
  return {
    namespace: 'prod',
    serverId,
    role,
    group: 'g1',
    zone: null,
    assigned: false,
    address: '',
    version: '',
    status: 'online',
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    lastHeartbeatAgeSec: 0,
    healthReason: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
    backends: [],
    zoneDefaultEntry: false,
    proxy: {
      onlineConnections: 0,
      threadCount: 0,
      uptimeMs: 0,
      backendUp: 0,
      backendTotal: 0,
      backendAvgLatencyMs: -1,
    },
    registeredAt: '',
  }
}

const BASE: BuildSources = {
  navItems: [
    { to: '/configs', label: '配置中心' },
    { to: '/servers', label: '服务器' },
  ],
  auditActions: [{ action: 'config.publish', label: '发布配置' }],
}

describe('buildItems', () => {
  it('归一导航 / 配置 / 文件 / 服务器 / 审计动作并带跳转目标', () => {
    const items = buildItems({
      ...BASE,
      configs: [cfg(1, 'server.yml')],
      files: [file(2, 'plugins/Foo/config.yml')],
      instances: [inst('lobby-1')],
    })
    // 各分组应都出现，且跳转目标带正确深链参数
    const byGroup = (g: string) => items.filter((it) => it.group === g)
    expect(byGroup('navigation')).toHaveLength(2)
    expect(byGroup('config')).toHaveLength(2) // 配置 + 文件同归 config 组
    expect(byGroup('server')).toHaveLength(1)
    expect(byGroup('audit')).toHaveLength(1)

    expect(items.find((it) => it.id === 'config:1')?.to).toBe('/configs?dataId=server.yml')
    expect(items.find((it) => it.id === 'server:lobby-1')?.to).toBe('/servers?serverId=lobby-1')
    expect(items.find((it) => it.id === 'audit:config.publish')?.to).toBe(
      '/audits?action=config.publish',
    )
  })

  it('缺省 configs/files/instances 时只产出导航 + 审计动作', () => {
    const items = buildItems(BASE)
    expect(items).toHaveLength(3)
    expect(items.every((it) => it.group === 'navigation' || it.group === 'audit')).toBe(true)
  })
})

describe('filterItems', () => {
  const items = buildItems({
    ...BASE,
    configs: [cfg(1, 'server.yml'), cfg(2, 'bukkit.yml')],
    instances: [inst('lobby-1')],
  })

  it('空 query 只返回导航与审计动作（不刷全量配置 / 服务器）', () => {
    const out = filterItems(items, '   ')
    expect(out.every((it) => it.group === 'navigation' || it.group === 'audit')).toBe(true)
    expect(out.some((it) => it.group === 'config')).toBe(false)
  })

  it('按 title 子串大小写无关命中', () => {
    const out = filterItems(items, 'SERVER')
    // 命中 dataId=server.yml 与 serverId=lobby-1（subtitle 含 server？否）——这里至少含 server.yml
    expect(out.some((it) => it.id === 'config:1')).toBe(true)
  })

  it('按 subtitle 命中（如环境名）', () => {
    const out = filterItems(items, 'prod')
    expect(out.some((it) => it.group === 'config')).toBe(true)
  })

  it('无命中返回空', () => {
    expect(filterItems(items, '不存在的关键字zzz')).toHaveLength(0)
  })
})

describe('groupItems', () => {
  it('按固定组顺序归类、空组不出现、组内保序', () => {
    const items = buildItems({
      ...BASE,
      configs: [cfg(1, 'a.yml'), cfg(2, 'b.yml')],
    })
    const grouped = groupItems(items)
    // 无 server 数据 → server 组不出现；顺序为 navigation → config → audit
    expect(grouped.map((g) => g.group)).toEqual(['navigation', 'config', 'audit'])
    // config 组内保持传入顺序
    expect(grouped.find((g) => g.group === 'config')?.items.map((it) => it.title)).toEqual([
      'a.yml',
      'b.yml',
    ])
  })
})
