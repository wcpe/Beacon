// 变更单空态引导：无任何变更单时代替空表格，用三张任务说明卡 + 大号「引导创建」
// 按钮把新手直接带进向导（点任务卡带预选类型）。
import { useTranslation } from 'react-i18next'

import { Sparkles } from 'lucide-react'

import { Button } from '@beacon/ui'

import { CONTENT_TYPES, ContentTypeCard } from './wizard-step-content'
import type { WizardContent } from './wizard-state'

interface EmptyGuideProps {
  // 点任务卡 / 大按钮进向导（卡片带预选类型，大按钮默认从文件任务起步）
  onStart: (content: WizardContent) => void
}

export default function EmptyGuide({ onStart }: EmptyGuideProps) {
  const { t } = useTranslation()
  return (
    <div className="grid justify-items-center gap-4 px-4 py-10 text-center">
      <div className="grid justify-items-center gap-1">
        <h3 className="text-sm font-semibold text-ink-1">{t('delivery.changes.list.emptyGuide.title')}</h3>
        <p className="max-w-xl text-sm text-muted-foreground">{t('delivery.changes.list.emptyGuide.desc')}</p>
      </div>
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
        onClick={() => {
          onStart('files')
        }}
      >
        <Sparkles className="size-4" />
        {t('delivery.changes.list.emptyGuide.cta')}
      </Button>
    </div>
  )
}
