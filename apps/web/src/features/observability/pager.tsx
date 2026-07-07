// 可观测列表页共用服务端分页控件：显示总数（超大量截断明示）+ 上/下一页。
// 派生逻辑（page/pageCount）由页面持有，本组件只呈现，职责单一。

import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

interface PagerProps {
  // 当前页（从 1 起）
  page: number
  // 总页数
  pageCount: number
  // 记录总数（明示超大量）
  total: number
  // 翻页回调
  onPageChange: (page: number) => void
}

export default function Pager({ page, pageCount, total, onPageChange }: PagerProps) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
      <span>{t('observability.common.pageInfo', { page, pages: pageCount, total })}</span>
      <Button
        size="sm"
        variant="outline"
        disabled={page <= 1}
        onClick={() => {
          onPageChange(Math.max(1, page - 1))
        }}
      >
        {t('observability.common.prevPage')}
      </Button>
      <Button
        size="sm"
        variant="outline"
        disabled={page >= pageCount}
        onClick={() => {
          onPageChange(Math.min(pageCount, page + 1))
        }}
      >
        {t('observability.common.nextPage')}
      </Button>
    </div>
  )
}
