// 变更单 / 批次 / 目标状态徽标：状态枚举 → 颜色变体 + i18n 文案，收敛各处重复的状态展示。
import { useTranslation } from 'react-i18next'

import { Badge } from '@beacon/ui'
import type {
  ChangeBatchStatus,
  ChangeOrderStatus,
  ChangeTargetStatus,
} from '../../api/delivery-changes'

// B 版语义药丸变体：ok 正常终态 / brand 进行中强调 / warn 待处理 / crit 失败·危险 / off 草稿·跳过终态
type PillVariant = 'ok' | 'brand' | 'warn' | 'crit' | 'off'

// 变更单状态 → 语义药丸（灰度中 / 回滚中用品牌强调，失败态用危险色，草稿 / 终态用弱色）
const ORDER_VARIANT: Record<ChangeOrderStatus, PillVariant> = {
  draft: 'off',
  pending_approval: 'warn',
  approved: 'brand',
  rolling: 'brand',
  paused: 'crit',
  completed: 'ok',
  cancelled: 'crit',
  rolling_back: 'crit',
  rolled_back: 'off',
}

const BATCH_VARIANT: Record<ChangeBatchStatus, PillVariant> = {
  pending: 'off',
  running: 'brand',
  observing: 'brand',
  awaiting_confirm: 'warn',
  completed: 'ok',
  failed: 'crit',
  skipped: 'off',
}

const TARGET_VARIANT: Record<ChangeTargetStatus, PillVariant> = {
  pending: 'off',
  pushing: 'brand',
  pushed: 'brand',
  activating: 'brand',
  activated: 'ok',
  failed: 'crit',
  skipped: 'off',
}

// 语义药丸：浅底 + 前导小圆点，一眼辨状态（对齐 B 版状态墙）
function StatusPill({ variant, label }: { variant: PillVariant; label: string }) {
  return (
    <Badge variant={variant} className="gap-1.5">
      <span className="size-1.5 rounded-full bg-current" aria-hidden />
      {label}
    </Badge>
  )
}

export function OrderStatusBadge({ status }: { status: ChangeOrderStatus }) {
  const { t } = useTranslation()
  return <StatusPill variant={ORDER_VARIANT[status]} label={t(`delivery.changes.status.${status}`)} />
}

export function BatchStatusBadge({ status }: { status: ChangeBatchStatus }) {
  const { t } = useTranslation()
  return (
    <StatusPill variant={BATCH_VARIANT[status]} label={t(`delivery.changes.batchStatus.${status}`)} />
  )
}

export function TargetStatusBadge({ status }: { status: ChangeTargetStatus }) {
  const { t } = useTranslation()
  return (
    <StatusPill variant={TARGET_VARIANT[status]} label={t(`delivery.changes.targetStatus.${status}`)} />
  )
}
