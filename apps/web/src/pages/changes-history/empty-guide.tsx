// 交付历史空态引导：还没有任何归档记录时，说明历史从哪来并把人送回变更单发起第一单。
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { ClipboardList } from 'lucide-react'

import { Button } from '@beacon/ui'

import EmptyGuideFrame from '../../features/shared/empty-guide-frame'

export default function EmptyGuide() {
  const { t } = useTranslation()
  return (
    <EmptyGuideFrame title={t('delivery.guides.history.title')} description={t('delivery.guides.history.desc')}>
      <Button size="lg" asChild>
        <Link to="/changes">
          <ClipboardList className="size-4" aria-hidden />
          {t('delivery.guides.history.cta')}
        </Link>
      </Button>
    </EmptyGuideFrame>
  )
}
