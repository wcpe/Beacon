// zone 看板数据派生（无副作用纯函数）：把 listInstances / listAssignments / zoneSummary
// 三个既有查询结果归并成看板视图——未指派卡片池 + 按大区(group)分组的 zone 桶（含其卡片）。
// 复用既有 API、后端零改动（FR-35）。

import type { AssignmentView, InstanceView, ZoneStatView } from '../../api/types'

// 单个 zone 桶：归属大区 + 小区 + 落在其中的实例卡片
export interface ZoneBucket {
  group: string
  zone: string
  instances: InstanceView[]
}

// 按大区分组：大区名 + 其下若干 zone 桶
export interface GroupColumn {
  group: string
  zones: ZoneBucket[]
}

// 看板整体视图：未指派池 + 按大区分组的 zone 桶
export interface KanbanModel {
  unassigned: InstanceView[]
  groups: GroupColumn[]
}

// 用 group/zone 拼联合键（zoneSummary 与实例归桶共用），分隔符取换行（业务值不含）
function zoneKey(group: string, zone: string): string {
  return `${group}\n${zone}`
}

// 构建看板模型：
//   - 桶集合来自 zoneSummary（即便没有实例的空 zone 也作为可放置目标显示）；
//   - 实例按其 (group, zone) 落入对应桶；assigned 为 false 的进未指派池；
//   - 实例指向的 (group, zone) 若不在 summary 中，临时补一个桶以免卡片丢失。
// 输出按 group、zone、serverId 升序，保证渲染稳定（与拖拽顺序无关）。
export function buildKanbanModel(
  instances: InstanceView[],
  summary: ZoneStatView[],
): KanbanModel {
  // 以 summary 预建空桶（保留无实例的 zone 作为放置目标）
  const buckets = new Map<string, ZoneBucket>()
  for (const s of summary) {
    buckets.set(zoneKey(s.group, s.zone), { group: s.group, zone: s.zone, instances: [] })
  }

  const unassigned: InstanceView[] = []
  for (const inst of instances) {
    if (!inst.assigned || inst.zone === null) {
      unassigned.push(inst)
      continue
    }
    const key = zoneKey(inst.group, inst.zone)
    let bucket = buckets.get(key)
    if (!bucket) {
      // 实例指向的 zone 不在 summary（数据短暂不一致），补桶避免卡片丢失
      bucket = { group: inst.group, zone: inst.zone, instances: [] }
      buckets.set(key, bucket)
    }
    bucket.instances.push(inst)
  }

  // 按 group → zone 归并为列，并各自排序
  const byGroup = new Map<string, ZoneBucket[]>()
  for (const bucket of buckets.values()) {
    bucket.instances.sort((a, b) => a.serverId.localeCompare(b.serverId))
    const list = byGroup.get(bucket.group) ?? []
    list.push(bucket)
    byGroup.set(bucket.group, list)
  }

  const groups: GroupColumn[] = [...byGroup.entries()]
    .map(([group, zones]) => ({
      group,
      zones: zones.sort((a, b) => a.zone.localeCompare(b.zone)),
    }))
    .sort((a, b) => a.group.localeCompare(b.group))

  unassigned.sort((a, b) => a.serverId.localeCompare(b.serverId))
  return { unassigned, groups }
}

// 取某实例当前指派记录的备注（改派时沿用，避免拖拽清空备注）；无记录返回空串。
export function noteForServer(assignments: AssignmentView[], namespace: string, serverId: string): string {
  const found = assignments.find((a) => a.namespace === namespace && a.serverId === serverId)
  return found?.note ?? ''
}
