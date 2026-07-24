// 未分配服务器拖拽落区的数据载荷约定：拖起 chip 写入 dataTransfer，
// 树里的放置目标读取以判定兼容性并触发分配。用原生 HTML5 拖拽，不引 DnD 库。

// 自定义 MIME，避免与文本拖拽混淆
export const ASSIGN_DRAG_MIME = 'application/x-beacon-server'

// 拖拽载荷：被拖服务器的行 id、serverId 与 kind。
// 已分配服务器改派时带上当前归属（fromId/fromName），供落区目标判定首次分配 vs 换区改派。
export interface AssignDragPayload {
  id: number
  serverId: string
  kind: 'backend' | 'proxy'
  // 当前归属节点 id（backend=zoneId、proxy=bcClusterId）；未分配为 null
  fromId?: number | null
  // 当前归属可读名（换区确认展示用）
  fromName?: string | null
}

/** 写入拖拽载荷（dragStart 调用）。 */
export function writeAssignDrag(dt: DataTransfer, payload: AssignDragPayload): void {
  dt.setData(ASSIGN_DRAG_MIME, JSON.stringify(payload))
  dt.effectAllowed = 'move'
}

/** 读取拖拽载荷；非本类型或解析失败返回 null。 */
export function readAssignDrag(dt: DataTransfer): AssignDragPayload | null {
  const raw = dt.getData(ASSIGN_DRAG_MIME)
  if (raw === '') {
    return null
  }
  try {
    const parsed = JSON.parse(raw) as AssignDragPayload
    return parsed
  } catch {
    return null
  }
}
