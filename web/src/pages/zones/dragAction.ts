// zone 看板拖拽落点解析（无副作用纯函数，供单测）。
// 把 @dnd-kit 的 onDragEnd 结果（被拖卡的 serverId + 落入的桶 id）翻译成一次后端动作：
// 指派 / 改派 → assignZone；拖回未指派池 → unassignZone；无效落点 → none。
// 页面据此调用既有 API（PUT/DELETE /zones/assignments），后端零改动（FR-35，纯 UI 增强 FR-8）。

import type { AssignParams } from '../../api/client'
import type { InstanceView } from '../../api/types'

// 未指派池放置桶的固定 id（与桶集合区分；zone 桶 id 用 encodeZoneDroppableId 编码）
export const UNASSIGNED_DROPPABLE_ID = 'unassigned'

// zone 桶 id 编码：把 (group, zone) 拼成放置桶标识，落点解析时再还原。
// 用换行分隔符（group/zone 业务取值不含换行），避免与值内的连字符/斜杠歧义。
const ZONE_ID_SEP = '\n'

// 把 (group, zone) 编码为 zone 桶 droppable id
export function encodeZoneDroppableId(group: string, zone: string): string {
  return `zone${ZONE_ID_SEP}${group}${ZONE_ID_SEP}${zone}`
}

// 还原 zone 桶 droppable id 为 (group, zone)；非 zone 桶 id 返回 null
function decodeZoneDroppableId(id: string): { group: string; zone: string } | null {
  const parts = id.split(ZONE_ID_SEP)
  if (parts.length !== 3 || parts[0] !== 'zone') return null
  return { group: parts[1], zone: parts[2] }
}

// 解析后的动作：指派/改派、取消指派、或无操作
export type DragAction =
  | { kind: 'assign'; params: AssignParams }
  | { kind: 'unassign'; namespace: string; serverId: string }
  | { kind: 'none' }

// 把一次拖拽落点解析为后端动作。
// instance：被拖动的卡片对应实例（onDragEnd 时按 active.id 查得）。
// overId：落入的放置桶 id（null 表示拖到桶外，不动作）。
// 规则：
//   - 落入未指派池：已指派 → 取消指派；本就未指派 → 无操作。
//   - 落入某 zone 桶：与当前 (group, zone) 相同 → 无操作（原地放回）；否则指派/改派到该 zone。
//   - 落点非法 / 落到桶外：无操作。
// note 沿用实例当前指派备注（拖拽不改备注），未指派时为空串。
export function resolveDragAction(
  instance: InstanceView,
  overId: string | null,
): DragAction {
  if (overId === null) return { kind: 'none' }

  if (overId === UNASSIGNED_DROPPABLE_ID) {
    // 拖回未指派池：仅对已指派实例生效，未指派实例原地放回无操作
    if (!instance.assigned) return { kind: 'none' }
    return { kind: 'unassign', namespace: instance.namespace, serverId: instance.serverId }
  }

  const target = decodeZoneDroppableId(overId)
  if (target === null) return { kind: 'none' }

  // 落回当前所属 zone（同 group 同 zone）：无操作，避免无谓写
  if (instance.assigned && instance.group === target.group && instance.zone === target.zone) {
    return { kind: 'none' }
  }

  return {
    kind: 'assign',
    params: {
      namespace: instance.namespace,
      serverId: instance.serverId,
      group: target.group,
      zone: target.zone,
      // 拖拽不编辑备注，沿用现有指派记录的备注（由调用方注入），此处缺省空串
      note: '',
    },
  }
}
