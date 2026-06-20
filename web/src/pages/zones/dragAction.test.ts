// zone 看板拖拽落点解析单测（FR-35）：
// 穷举「指派到某 zone / 跨桶改派 / 拖回未指派取消 / 原地放回与桶外无操作」核心分支，
// 锁定 onDragEnd 翻译为 assignZone / unassignZone 入参的逻辑（jsdom 难测全量 DnD，故抽纯函数测）。
import { describe, it, expect } from 'vitest'
import {
  resolveDragAction,
  encodeZoneDroppableId,
  UNASSIGNED_DROPPABLE_ID,
} from './dragAction'
import type { InstanceView } from '../../api/types'

// 构造实例样例：默认已指派到 (gA, z1)，可按需覆盖字段
function makeInstance(overrides: Partial<InstanceView> = {}): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'srv-1',
    role: 'bukkit',
    group: 'gA',
    zone: 'z1',
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.0.0',
    status: 'online',
    capacity: 100,
    weight: 1,
    metadata: {},
    lastHeartbeat: '2026-06-20T08:00:00Z',
    appliedMd5: 'abc',
    playerCount: 0,
    tps: 20,
    registeredAt: '2026-06-20T07:00:00Z',
    ...overrides,
  }
}

describe('resolveDragAction', () => {
  it('未指派实例拖入某 zone → 指派到该 (group, zone)', () => {
    const inst = makeInstance({ serverId: 'srv-new', assigned: false, group: '', zone: null })
    const action = resolveDragAction(inst, encodeZoneDroppableId('gA', 'z1'))
    expect(action).toEqual({
      kind: 'assign',
      params: { namespace: 'prod', serverId: 'srv-new', group: 'gA', zone: 'z1', note: '' },
    })
  })

  it('已指派实例拖到另一个 zone → 改派到新 (group, zone)', () => {
    const inst = makeInstance({ group: 'gA', zone: 'z1' })
    const action = resolveDragAction(inst, encodeZoneDroppableId('gB', 'z9'))
    expect(action).toEqual({
      kind: 'assign',
      params: { namespace: 'prod', serverId: 'srv-1', group: 'gB', zone: 'z9', note: '' },
    })
  })

  it('已指派实例拖回未指派池 → 取消指派', () => {
    const inst = makeInstance({ serverId: 'srv-1' })
    const action = resolveDragAction(inst, UNASSIGNED_DROPPABLE_ID)
    expect(action).toEqual({ kind: 'unassign', namespace: 'prod', serverId: 'srv-1' })
  })

  it('未指派实例拖回未指派池 → 无操作', () => {
    const inst = makeInstance({ assigned: false, group: '', zone: null })
    expect(resolveDragAction(inst, UNASSIGNED_DROPPABLE_ID)).toEqual({ kind: 'none' })
  })

  it('已指派实例落回其当前所属 zone → 无操作（不重复指派）', () => {
    const inst = makeInstance({ group: 'gA', zone: 'z1' })
    expect(resolveDragAction(inst, encodeZoneDroppableId('gA', 'z1'))).toEqual({ kind: 'none' })
  })

  it('落点为 null（拖到桶外释放）→ 无操作', () => {
    expect(resolveDragAction(makeInstance(), null)).toEqual({ kind: 'none' })
  })

  it('落点为非法桶 id → 无操作', () => {
    expect(resolveDragAction(makeInstance(), 'garbage-id')).toEqual({ kind: 'none' })
  })

  it('同名 group 不同 zone 视为改派（仅 group 相同不算原地）', () => {
    const inst = makeInstance({ group: 'gA', zone: 'z1' })
    const action = resolveDragAction(inst, encodeZoneDroppableId('gA', 'z2'))
    expect(action).toMatchObject({ kind: 'assign', params: { group: 'gA', zone: 'z2' } })
  })
})

describe('encodeZoneDroppableId', () => {
  it('编码后能区分未指派池 id', () => {
    expect(encodeZoneDroppableId('gA', 'z1')).not.toBe(UNASSIGNED_DROPPABLE_ID)
  })
})
