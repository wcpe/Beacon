// 变更单状态徽章：按状态映射语义色 + 中文文案（交付历史页自用）。
import { useTranslation } from 'react-i18next'

import { Badge } from '@beacon/ui'
import type { ChangeOrderStatus } from '../../api/delivery-changes'

// 状态 → Badge variant（语义色）
const VARIANT: Record<ChangeOrderStatus, 'default' | 'secondary' | 'outline' | 'destructive'> = {
  draft: 'outline',
  pending_approval: 'outline',
  approved: 'secondary',
  rolling: 'default',
  paused: 'destructive',
  completed: 'secondary',
  cancelled: 'outline',
  rolling_back: 'destructive',
  rolled_back: 'secondary',
}

export default function StatusBadge({ status }: { status: ChangeOrderStatus }) {
  const { t } = useTranslation()
  return <Badge variant={VARIANT[status]}>{t(`delivery.changes.status.${status}`)}</Badge>
}
