// zone 看板模型派生单测（FR-35）：验证未指派池/分桶归并/空桶保留/排序/备注沿用。
import { describe, it, expect } from 'vitest'
import { buildKanbanModel, noteForServer } from './kanbanModel'
import type { AssignmentView, InstanceView, ZoneStatView } from '../../api/types'

function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'srv',
    role: 'bukkit',
    group: '',
    zone: null,
    assigned: false,
    address: '',
    version: '',
    status: 'online',
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
    registeredAt: '',
    ...overrides,
  }
}

const SUMMARY: ZoneStatView[] = [
  { group: 'gA', zone: 'z1', serverCount: 1, onlineCount: 1 },
  { group: 'gA', zone: 'z2', serverCount: 0, onlineCount: 0 },
  { group: 'gB', zone: 'z9', serverCount: 0, onlineCount: 0 },
]

describe('buildKanbanModel', () => {
  it('未指派实例进未指派池，已指派实例进对应 zone 桶', () => {
    const m = buildKanbanModel(
      [
        inst({ serverId: 'free-1', assigned: false }),
        inst({ serverId: 'a-1', assigned: true, group: 'gA', zone: 'z1' }),
      ],
      SUMMARY,
    )
    expect(m.unassigned.map((i) => i.serverId)).toEqual(['free-1'])
    const gA = m.groups.find((g) => g.group === 'gA')!
    const z1 = gA.zones.find((z) => z.zone === 'z1')!
    expect(z1.instances.map((i) => i.serverId)).toEqual(['a-1'])
  })

  it('保留 summary 中无实例的空 zone 作为可放置桶', () => {
    const m = buildKanbanModel([], SUMMARY)
    const gA = m.groups.find((g) => g.group === 'gA')!
    expect(gA.zones.map((z) => z.zone)).toEqual(['z1', 'z2'])
    expect(gA.zones.find((z) => z.zone === 'z2')!.instances).toHaveLength(0)
  })

  it('实例指向 summary 缺失的 zone 时补桶，不丢卡片', () => {
    const m = buildKanbanModel(
      [inst({ serverId: 'orphan', assigned: true, group: 'gC', zone: 'zX' })],
      SUMMARY,
    )
    const gC = m.groups.find((g) => g.group === 'gC')!
    expect(gC.zones[0].zone).toBe('zX')
    expect(gC.zones[0].instances.map((i) => i.serverId)).toEqual(['orphan'])
  })

  it('大区、zone、卡片均按字典序稳定排序', () => {
    const m = buildKanbanModel(
      [
        inst({ serverId: 'b', assigned: true, group: 'gA', zone: 'z1' }),
        inst({ serverId: 'a', assigned: true, group: 'gA', zone: 'z1' }),
      ],
      SUMMARY,
    )
    expect(m.groups.map((g) => g.group)).toEqual(['gA', 'gB'])
    const z1 = m.groups[0].zones.find((z) => z.zone === 'z1')!
    expect(z1.instances.map((i) => i.serverId)).toEqual(['a', 'b'])
  })

  it('assigned 为 true 但 zone 为 null 的实例归入未指派池（容错）', () => {
    const m = buildKanbanModel([inst({ serverId: 'weird', assigned: true, zone: null })], SUMMARY)
    expect(m.unassigned.map((i) => i.serverId)).toEqual(['weird'])
  })
})

describe('noteForServer', () => {
  const assignments: AssignmentView[] = [
    {
      namespace: 'prod',
      serverId: 'srv-1',
      group: 'gA',
      zone: 'z1',
      note: '主城',
      updatedAt: '',
    },
  ]

  it('命中实例返回其备注', () => {
    expect(noteForServer(assignments, 'prod', 'srv-1')).toBe('主城')
  })

  it('未命中返回空串', () => {
    expect(noteForServer(assignments, 'prod', 'absent')).toBe('')
    expect(noteForServer(assignments, 'other', 'srv-1')).toBe('')
  })
})
