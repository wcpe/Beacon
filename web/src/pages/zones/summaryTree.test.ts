// zone 汇总树派生单测（FR-55）：验证大区→小区→子服层级、计数取自 summary 与原表一致、
// 子服叶子来自看板模型、稳定排序与空态。
import { describe, it, expect } from 'vitest'
import { buildSummaryTree } from './summaryTree'
import { buildKanbanModel } from './kanbanModel'
import type { InstanceView, ZoneStatView } from '../../api/types'

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
    backends: [],
    registeredAt: '',
    ...overrides,
  }
}

const SUMMARY: ZoneStatView[] = [
  { group: 'gA', zone: 'z1', serverCount: 2, onlineCount: 1 },
  { group: 'gA', zone: 'z2', serverCount: 0, onlineCount: 0 },
  { group: 'gB', zone: 'z9', serverCount: 3, onlineCount: 2 },
]

describe('buildSummaryTree', () => {
  it('按大区→小区→子服三级构树，结构与 summary 一致', () => {
    const instances = [
      inst({ serverId: 'a-1', assigned: true, group: 'gA', zone: 'z1' }),
      inst({ serverId: 'a-2', assigned: true, group: 'gA', zone: 'z1' }),
      inst({ serverId: 'b-1', assigned: true, group: 'gB', zone: 'z9' }),
    ]
    const tree = buildSummaryTree(SUMMARY, buildKanbanModel(instances, SUMMARY))
    expect(tree.groups.map((g) => g.group)).toEqual(['gA', 'gB'])
    const gA = tree.groups.find((g) => g.group === 'gA')!
    expect(gA.zones.map((z) => z.zone)).toEqual(['z1', 'z2'])
    const z1 = gA.zones.find((z) => z.zone === 'z1')!
    expect(z1.servers.map((s) => s.serverId)).toEqual(['a-1', 'a-2'])
  })

  it('小区服数 / 在线数取自 summary，与原扁平表一致', () => {
    const tree = buildSummaryTree(SUMMARY, buildKanbanModel([], SUMMARY))
    const z1 = tree.groups.find((g) => g.group === 'gA')!.zones.find((z) => z.zone === 'z1')!
    // 服数=DB 指派数、在线数=在线注册数，均直取 summary，不由子服叶子推算
    expect(z1.serverCount).toBe(2)
    expect(z1.onlineCount).toBe(1)
  })

  it('大区服数 / 在线数为其小区合计', () => {
    const tree = buildSummaryTree(SUMMARY, buildKanbanModel([], SUMMARY))
    const gA = tree.groups.find((g) => g.group === 'gA')!
    // gA：z1(2/1) + z2(0/0) = 2/1
    expect(gA.serverCount).toBe(2)
    expect(gA.onlineCount).toBe(1)
    const gB = tree.groups.find((g) => g.group === 'gB')!
    expect(gB.serverCount).toBe(3)
    expect(gB.onlineCount).toBe(2)
  })

  it('空 zone 无子服时叶子为空但仍保留为节点', () => {
    const tree = buildSummaryTree(SUMMARY, buildKanbanModel([], SUMMARY))
    const z2 = tree.groups.find((g) => g.group === 'gA')!.zones.find((z) => z.zone === 'z2')!
    expect(z2.servers).toHaveLength(0)
  })

  it('大区、小区、子服均按字典序稳定排序', () => {
    const instances = [
      inst({ serverId: 'b', assigned: true, group: 'gA', zone: 'z1' }),
      inst({ serverId: 'a', assigned: true, group: 'gA', zone: 'z1' }),
    ]
    const tree = buildSummaryTree(SUMMARY, buildKanbanModel(instances, SUMMARY))
    expect(tree.groups.map((g) => g.group)).toEqual(['gA', 'gB'])
    const z1 = tree.groups[0].zones.find((z) => z.zone === 'z1')!
    expect(z1.servers.map((s) => s.serverId)).toEqual(['a', 'b'])
  })

  it('BC 代理不出现在子服叶子（沿用看板模型既有排除）', () => {
    const instances = [
      inst({ serverId: 'bc-1', role: 'bungee', assigned: true, group: 'gA', zone: 'z1' }),
      inst({ serverId: 'a-1', role: 'bukkit', assigned: true, group: 'gA', zone: 'z1' }),
    ]
    const tree = buildSummaryTree(SUMMARY, buildKanbanModel(instances, SUMMARY))
    const z1 = tree.groups.find((g) => g.group === 'gA')!.zones.find((z) => z.zone === 'z1')!
    expect(z1.servers.map((s) => s.serverId)).toEqual(['a-1'])
  })

  it('summary 为空时树为空', () => {
    const tree = buildSummaryTree([], buildKanbanModel([], []))
    expect(tree.groups).toHaveLength(0)
  })

  it('summary 缺失但模型中有子服的 zone 也并入树（不丢节点）', () => {
    // 模型按补桶逻辑会为 summary 缺失的 zone 建桶；汇总树应一并展示该 zone（计数缺省为 0）
    const instances = [inst({ serverId: 'orphan', assigned: true, group: 'gC', zone: 'zX' })]
    const tree = buildSummaryTree(SUMMARY, buildKanbanModel(instances, SUMMARY))
    const gC = tree.groups.find((g) => g.group === 'gC')
    expect(gC).toBeDefined()
    const zX = gC!.zones.find((z) => z.zone === 'zX')!
    expect(zX.servers.map((s) => s.serverId)).toEqual(['orphan'])
    expect(zX.serverCount).toBe(0)
    expect(zX.onlineCount).toBe(0)
  })
})
