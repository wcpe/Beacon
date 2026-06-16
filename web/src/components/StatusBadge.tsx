// 健康状态徽标：online / lost / offline 三色区分。

import type { InstanceStatus } from '../api/types'

// 状态到样式类的映射（未知状态走默认灰）
const CLASS_MAP: Record<InstanceStatus, string> = {
  online: 'badge badge-online',
  lost: 'badge badge-lost',
  offline: 'badge badge-offline',
}

export default function StatusBadge({ status }: { status: string }) {
  const cls = CLASS_MAP[status as InstanceStatus] ?? 'badge'
  return <span className={cls}>{status}</span>
}
