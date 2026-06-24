import { describe, expect, it } from 'vitest'
import type { InstanceView } from '@/api/types'
import { buildMajorityVersions, isAgentVersionMismatch } from './agentVersionConsistency'

// 构造最小实例（只关心 namespace + agentVersion，其余字段对本测试无关，置零值）。
function inst(namespace: string, agentVersion: string): InstanceView {
  return {
    namespace,
    serverId: `${namespace}-${agentVersion || 'none'}`,
    role: 'bukkit',
    group: '',
    zone: null,
    assigned: false,
    address: '',
    version: '',
    agentVersion,
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
      backendAvgLatencyMs: 0,
    },
    registeredAt: '',
  }
}

describe('buildMajorityVersions', () => {
  it('取每环境出现次数最多的版本', () => {
    const maj = buildMajorityVersions([
      inst('prod', '0.12.0'),
      inst('prod', '0.12.0'),
      inst('prod', '0.11.0'),
    ])
    expect(maj.get('prod')).toBe('0.12.0')
  })

  it('按环境隔离统计，互不串值', () => {
    const maj = buildMajorityVersions([
      inst('prod', '0.12.0'),
      inst('test', '0.11.0'),
      inst('test', '0.11.0'),
    ])
    expect(maj.get('prod')).toBe('0.12.0')
    expect(maj.get('test')).toBe('0.11.0')
  })

  it('空版本不参与统计', () => {
    const maj = buildMajorityVersions([
      inst('prod', ''),
      inst('prod', '0.12.0'),
    ])
    expect(maj.get('prod')).toBe('0.12.0')
  })

  it('全环境皆空时该环境无多数条目', () => {
    const maj = buildMajorityVersions([inst('prod', ''), inst('prod', '')])
    expect(maj.has('prod')).toBe(false)
  })

  it('计数并列时取字典序较小版本（确定性）', () => {
    const maj = buildMajorityVersions([
      inst('prod', '0.12.0'),
      inst('prod', '0.11.0'),
    ])
    expect(maj.get('prod')).toBe('0.11.0')
  })
})

describe('isAgentVersionMismatch', () => {
  const majority = buildMajorityVersions([
    inst('prod', '0.12.0'),
    inst('prod', '0.12.0'),
    inst('prod', '0.11.0'),
  ])

  it('与多数版本不同 → 不一致', () => {
    expect(isAgentVersionMismatch({ namespace: 'prod', agentVersion: '0.11.0' }, majority)).toBe(true)
  })

  it('与多数版本相同 → 一致', () => {
    expect(isAgentVersionMismatch({ namespace: 'prod', agentVersion: '0.12.0' }, majority)).toBe(false)
  })

  it('空版本（旧 agent 未上报）→ 不标不一致', () => {
    expect(isAgentVersionMismatch({ namespace: 'prod', agentVersion: '' }, majority)).toBe(false)
  })

  it('环境无多数条目（全空）→ 不标不一致', () => {
    const emptyMaj = buildMajorityVersions([inst('test', '')])
    expect(isAgentVersionMismatch({ namespace: 'test', agentVersion: '0.12.0' }, emptyMaj)).toBe(false)
  })
})
