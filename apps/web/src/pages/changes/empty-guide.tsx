// 变更单空态引导：无任何变更单时代替空表格，用三张任务说明卡 + 大号「引导创建」
// 按钮把新手直接带进向导（点任务卡带预选类型）。
import { useTranslation } from 'react-i18next'

import { Sparkles } from 'lucide-react'

import { Button } from '@beacon/ui'

import EmptyGuideFrame from '../../features/shared/empty-guide-frame'
import { CONTENT_TYPES, ContentTypeCard } from './wizard-step-content'
import type { WizardContent } from './wizard-state'

interface EmptyGuideProps {
  // 点任务卡 / 大按钮进向导（卡片带预选类型，大按钮默认从文件任务起步）
  onStart: (content: WizardContent) => void
}

export default function EmptyGuide({ onStart }: EmptyGuideProps) {
  const { t } = useTranslation()
  return (
    <EmptyGuideFrame
      title={t('delivery.changes.list.emptyGuide.title')}
      description={t('delivery.changes.list.emptyGuide.desc')}
    >
      <div className="grid w-full max-w-2xl gap-2.5 text-left sm:grid-cols-3">
        {CONTENT_TYPES.map((type) => (
          <ContentTypeCard
            key={type}
            type={type}
            onClick={() => {
              onStart(type)
            }}
          />
        ))}
      </div>
      <Button
        size="lg"
        variant="outline"
        onClick={() => {
          onStart('files')
        }}
      >
        <Sparkles className="size-4" />
        {t('delivery.changes.list.emptyGuide.cta')}
      </Button>
    </EmptyGuideFrame>
  )
}
