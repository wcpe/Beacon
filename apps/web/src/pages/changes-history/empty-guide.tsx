// 交付历史空态引导：还没有任何归档记录时，说明历史从哪来并把人送回变更单发起第一单。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { ClipboardList } from 'lucide-react'

import { Button } from '@beacon/ui'

export default function EmptyGuide() {
  const { t } = useTranslation()
  return (
    <div className="grid justify-items-center gap-4 px-4 py-10 text-center">
      <div className="grid justify-items-center gap-1">
        <h3 className="text-sm font-semibold text-ink-1">{t('delivery.guides.history.title')}</h3>
        <p className="max-w-xl text-sm text-muted-foreground">{t('delivery.guides.history.desc')}</p>
      </div>
      <Button size="lg" asChild>
        <Link to="/changes">
          <ClipboardList className="size-4" aria-hidden />
          {t('delivery.guides.history.cta')}
        </Link>
      </Button>
    </div>
  )
}
