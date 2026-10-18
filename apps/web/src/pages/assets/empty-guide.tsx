// 文件资产空态引导：该命名空间还没有任何清单上报时，代替清单表格给出「下一步做什么」。
// 与变更单空态（pages/changes/empty-guide.tsx）同一视觉语言：大标题 + 说明 + 主要动作。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { ClipboardList, RefreshCw } from 'lucide-react'

import { Button } from '@beacon/ui'

interface EmptyGuideProps {
  // 重新拉取清单探测与各面板数据（agent 刚上报完不必刷新整页）
  onRefresh: () => void
}

export default function EmptyGuide({ onRefresh }: EmptyGuideProps) {
  const { t } = useTranslation()
  return (
    <div className="grid justify-items-center gap-4 px-4 py-10 text-center">
      <div className="grid justify-items-center gap-1">
        <h3 className="text-sm font-semibold text-ink-1">{t('delivery.guides.assets.title')}</h3>
        <p className="max-w-xl text-sm text-muted-foreground">{t('delivery.guides.assets.desc')}</p>
      </div>
      <div className="flex flex-wrap items-center justify-center gap-2">
        <Button size="lg" variant="outline" asChild>
          <Link to="/changes">
            <ClipboardList className="size-4" aria-hidden />
            {t('delivery.guides.assets.cta')}
          </Link>
        </Button>
        <Button size="lg" variant="outline" onClick={onRefresh}>
          <RefreshCw className="size-4" aria-hidden />
          {t('delivery.guides.assets.refresh')}
        </Button>
      </div>
    </div>
  )
}
