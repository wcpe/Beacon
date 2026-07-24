// 向导第 1 步：选交付内容。三张任务卡（文件 / 配置 / 混合），选择决定后续步骤。
// ContentTypeCard 同时被列表空态引导复用（点卡带预选类型进向导）。
import { useTranslation } from 'react-i18next'

import { FileCog, Layers, Package } from 'lucide-react'

import { cn } from '@beacon/ui'

import type { WizardContent } from './wizard-state'

const CARD_ICON: Record<WizardContent, typeof Package> = {
  files: Package,
  configs: FileCog,
  both: Layers,
}

/** 全部内容类型（卡片展示顺序） */
export const CONTENT_TYPES: WizardContent[] = ['files', 'configs', 'both']

interface ContentTypeCardProps {
  type: WizardContent
  selected?: boolean
  onClick: () => void
}

/** 单张任务说明卡：图标 + 标题 + 说明 + 前提提示；selected 时品牌描边高亮 */
export function ContentTypeCard({ type, selected, onClick }: ContentTypeCardProps) {
  const { t } = useTranslation()
  const Icon = CARD_ICON[type]
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onClick}
      className={cn(
        'grid w-full gap-1.5 rounded-xl border p-3.5 text-left transition-colors',
        selected === true
          ? 'border-brand bg-brand-50/60 ring-1 ring-brand'
          : 'border-border bg-card hover:border-brand-200 hover:bg-brand-50/30',
      )}
    >
      <span className="flex items-center gap-2 text-[13px] font-semibold text-ink-1">
        <span
          className={cn(
            'grid size-7 shrink-0 place-items-center rounded-lg',
            selected === true ? 'bg-brand text-white' : 'bg-brand-50 text-brand',
          )}
        >
          <Icon className="size-4" aria-hidden />
        </span>
        {t(`delivery.changes.wizard.content.${type}.title`)}
      </span>
      <span className="text-xs leading-relaxed text-ink-2">
        {t(`delivery.changes.wizard.content.${type}.desc`)}
      </span>
      <span className="text-xs leading-relaxed text-ink-3">
        {t(`delivery.changes.wizard.content.${type}.hint`)}
      </span>
    </button>
  )
}

interface StepContentProps {
  value: WizardContent
  onChange: (content: WizardContent) => void
}

export default function WizardStepContent({ value, onChange }: StepContentProps) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-3">
      <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.content.lead')}</p>
      <div className="grid gap-2.5 sm:grid-cols-3">
        {CONTENT_TYPES.map((type) => (
          <ContentTypeCard
            key={type}
            type={type}
            selected={value === type}
            onClick={() => {
              onChange(type)
            }}
          />
        ))}
      </div>
    </div>
  )
}
