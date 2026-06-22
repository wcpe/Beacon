// zone 汇总树派生（无副作用纯函数，FR-55）：把扁平的 zoneSummary（ZoneStatView）与看板模型
// （buildKanbanModel 结果）归并成「大区 → 小区 → 子服」三级树，供汇总区树形展示。
//
// 计数权威：服数 / 在线数一律取自 summary（ZoneStatView），与原扁平表口径完全一致——
//   服数 = DB 指派数、在线数 = 在线注册数；大区为其下小区之合计。
// 子服叶子：从看板模型按 (group, zone) 取在册子服，仅作展示（BC 代理已由看板模型排除）。
//   故子服叶子数可能与服数不相等（口径不同，属预期）。

import type { InstanceView, ZoneStatView } from '../../api/types'
import type { KanbanModel } from './kanbanModel'

// 子服叶节点：仅展示所需的 serverId 与在线状态
export interface SummaryServer {
  serverId: string
  status: string
}

// 小区节点：服数 / 在线数取自 summary，子服来自看板模型
export interface SummaryZoneNode {
  zone: string
  serverCount: number
  onlineCount: number
  servers: SummaryServer[]
}

// 大区节点：服数 / 在线数为其下小区合计
export interface SummaryGroupNode {
  group: string
  serverCount: number
  onlineCount: number
  zones: SummaryZoneNode[]
}

// 汇总树整体
export interface SummaryTree {
  groups: SummaryGroupNode[]
}

// 用 group/zone 拼联合键，分隔符取换行（业务值不含）
function zoneKey(group: string, zone: string): string {
  return `${group}\n${zone}`
}

// 构建汇总树：
//   - 节点集合 = summary 的 (group, zone) ∪ 模型中实际有子服的 (group, zone)（避免漏掉 summary 短暂缺失的桶）；
//   - 每个小区的服数 / 在线数直取 summary（缺省 0），子服来自模型对应桶；
//   - 大区计数为其小区合计；输出按大区、小区、serverId 字典序稳定排序。
export function buildSummaryTree(summary: ZoneStatView[], model: KanbanModel): SummaryTree {
  // summary 计数索引：键 → {服数, 在线数}
  const stats = new Map<string, { serverCount: number; onlineCount: number }>()
  for (const s of summary) {
    stats.set(zoneKey(s.group, s.zone), { serverCount: s.serverCount, onlineCount: s.onlineCount })
  }

  // 模型子服索引：键 → 子服实例（已按 serverId 排序、已排除 BC）
  const modelServers = new Map<string, InstanceView[]>()
  for (const col of model.groups) {
    for (const bucket of col.zones) {
      modelServers.set(zoneKey(bucket.group, bucket.zone), bucket.instances)
    }
  }

  // 收集所有 (group, zone)：summary 与模型的并集
  const zoneSet = new Map<string, { group: string; zone: string }>()
  for (const s of summary) {
    zoneSet.set(zoneKey(s.group, s.zone), { group: s.group, zone: s.zone })
  }
  for (const col of model.groups) {
    for (const bucket of col.zones) {
      zoneSet.set(zoneKey(bucket.group, bucket.zone), { group: bucket.group, zone: bucket.zone })
    }
  }

  // 按大区归并小区节点
  const byGroup = new Map<string, SummaryZoneNode[]>()
  for (const { group, zone } of zoneSet.values()) {
    const key = zoneKey(group, zone)
    const stat = stats.get(key) ?? { serverCount: 0, onlineCount: 0 }
    const servers = (modelServers.get(key) ?? []).map((i) => ({ serverId: i.serverId, status: i.status }))
    const list = byGroup.get(group) ?? []
    list.push({ zone, serverCount: stat.serverCount, onlineCount: stat.onlineCount, servers })
    byGroup.set(group, list)
  }

  const groups: SummaryGroupNode[] = [...byGroup.entries()]
    .map(([group, zones]) => {
      const sorted = zones.sort((a, b) => a.zone.localeCompare(b.zone))
      return {
        group,
        // 大区计数 = 其下小区合计
        serverCount: sorted.reduce((sum, z) => sum + z.serverCount, 0),
        onlineCount: sorted.reduce((sum, z) => sum + z.onlineCount, 0),
        zones: sorted,
      }
    })
    .sort((a, b) => a.group.localeCompare(b.group))

  return { groups }
}
