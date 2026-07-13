// 冷查询游标翻页控件（FR-152）：冷查询无总数，用「上一页 / 下一页」游标翻页 + 当前页序，
// 取代页码式 Pager（无总页数）。sched-decisions / audits 冷模式共用。
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

interface CursorPagerProps {
  // 当前页序（1 起）
  pageIndex: number
  // 是否可回上一页
  canPrev: boolean
  // 是否有下一页（后端返回了非空 nextCursor）
  canNext: boolean
  onPrev: () => void
  onNext: () => void
  // 是否冷查询（页信息带「含归档」标注）；连接 / 消息明细热查询也原生游标分页，传 false 用普通文案
  cold?: boolean
}

export default function CursorPager({ pageIndex, canPrev, canNext, onPrev, onNext, cold = true }: CursorPagerProps) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
      <span>
        {cold
          ? t('observability.common.cursorPageInfo', { page: pageIndex })
          : t('observability.common.cursorPageInfoHot', { page: pageIndex })}
      </span>
      <Button size="sm" variant="outline" disabled={!canPrev} onClick={onPrev}>
        {t('observability.common.prevPage')}
      </Button>
      <Button size="sm" variant="outline" disabled={!canNext} onClick={onNext}>
        {t('observability.common.nextPage')}
      </Button>
    </div>
  )
}
