// 变更单 / 批次 / 目标状态徽标：状态枚举 → 颜色变体 + i18n 文案，收敛各处重复的状态展示。
import { useTranslation } from 'react-i18next'

import { Badge } from '@beacon/ui'
import type {
  ChangeBatchStatus,
  ChangeOrderStatus,
  ChangeTargetStatus,
} from '../../api/delivery-changes'

type BadgeVariant = 'default' | 'secondary' | 'destructive' | 'outline'

// 变更单状态 → 徽标色（灰度中 / 回滚中用主色强调，失败态用危险色，终态用弱色）
const ORDER_VARIANT: Record<ChangeOrderStatus, BadgeVariant> = {
  draft: 'outline',
  pending_approval: 'secondary',
  approved: 'default',
  rolling: 'default',
  paused: 'destructive',
  completed: 'secondary',
  cancelled: 'destructive',
  rolling_back: 'destructive',
  rolled_back: 'secondary',
}

const BATCH_VARIANT: Record<ChangeBatchStatus, BadgeVariant> = {
  pending: 'outline',
  running: 'default',
  observing: 'default',
  awaiting_confirm: 'secondary',
  completed: 'secondary',
  failed: 'destructive',
  skipped: 'outline',
}

const TARGET_VARIANT: Record<ChangeTargetStatus, BadgeVariant> = {
  pending: 'outline',
  pushing: 'default',
  pushed: 'default',
  activating: 'default',
  activated: 'secondary',
  failed: 'destructive',
  skipped: 'outline',
}

export function OrderStatusBadge({ status }: { status: ChangeOrderStatus }) {
  const { t } = useTranslation()
  return <Badge variant={ORDER_VARIANT[status]}>{t(`delivery.changes.status.${status}`)}</Badge>
}

export function BatchStatusBadge({ status }: { status: ChangeBatchStatus }) {
  const { t } = useTranslation()
  return <Badge variant={BATCH_VARIANT[status]}>{t(`delivery.changes.batchStatus.${status}`)}</Badge>
}

export function TargetStatusBadge({ status }: { status: ChangeTargetStatus }) {
  const { t } = useTranslation()
  return <Badge variant={TARGET_VARIANT[status]}>{t(`delivery.changes.targetStatus.${status}`)}</Badge>
}
