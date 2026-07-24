// 变更单状态徽章：按状态映射语义色 + 中文文案（交付历史页自用）。
import { useTranslation } from 'react-i18next'

import { Badge } from '@beacon/ui'
import type { ChangeOrderStatus } from '../../api/delivery-changes'

// 状态 → B 版语义药丸变体（ok 完成 / brand 进行中 / warn 待处理 / crit 危险·回滚 / off 草稿·终态弱化）
const VARIANT: Record<ChangeOrderStatus, 'ok' | 'brand' | 'warn' | 'crit' | 'off'> = {
  draft: 'off',
  pending_approval: 'warn',
  approved: 'brand',
  rolling: 'brand',
  paused: 'crit',
  completed: 'ok',
  cancelled: 'off',
  rolling_back: 'crit',
  rolled_back: 'off',
}

export default function StatusBadge({ status }: { status: ChangeOrderStatus }) {
  const { t } = useTranslation()
  return (
    <Badge variant={VARIANT[status]} className="gap-1.5">
      <span className="size-1.5 rounded-full bg-current" aria-hidden />
      {t(`delivery.changes.status.${status}`)}
    </Badge>
  )
}
