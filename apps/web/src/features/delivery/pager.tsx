// 交付大域通用服务端分页控件：总数超过页大小才渲染，上一页 / 下一页 + 页码摘要。
import { useTranslation } from 'react-i18next'

import { Button } from '@beacon/ui'

interface PagerProps {
  page: number
  total: number
  pageSize: number
  onPageChange: (page: number) => void
}

export default function Pager({ page, total, pageSize, onPageChange }: PagerProps) {
  const { t } = useTranslation()
  const pageCount = Math.max(1, Math.ceil(total / pageSize))
  if (total <= pageSize) {
    return null
  }
  return (
    <div className="flex items-center justify-end gap-2 text-sm text-muted-foreground">
      <span>{t('delivery.pager.summary', { page, pageCount, total })}</span>
      <Button
        size="sm"
        variant="outline"
        disabled={page <= 1}
        onClick={() => {
          onPageChange(Math.max(1, page - 1))
        }}
      >
        {t('delivery.pager.prev')}
      </Button>
      <Button
        size="sm"
        variant="outline"
        disabled={page >= pageCount}
        onClick={() => {
          onPageChange(Math.min(pageCount, page + 1))
        }}
      >
        {t('delivery.pager.next')}
      </Button>
    </div>
  )
}
