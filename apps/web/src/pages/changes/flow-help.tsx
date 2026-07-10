// 「交付流程」帮助卡（非模态，页头入口开合）：五步生命周期横向图示
// 创建 → 审批 → 灰度批次 → 观察 → 完成/回滚，每步一句话，给新手一张全局地图。
import { useTranslation } from 'react-i18next'

import { ChevronRight, X } from 'lucide-react'

import { Button } from '@beacon/ui'

const FLOW_STEPS = ['create', 'approve', 'batch', 'observe', 'finish'] as const

interface FlowHelpProps {
  onClose: () => void
}

export default function FlowHelp({ onClose }: FlowHelpProps) {
  const { t } = useTranslation()
  return (
    <section className="rounded-xl border border-border bg-card px-4 py-3 shadow-card">
      <div className="mb-2 flex items-center justify-between gap-2">
        <h3 className="text-[13px] font-semibold text-ink-1">{t('delivery.changes.flow.title')}</h3>
        <Button
          size="sm"
          variant="ghost"
          className="size-7 shrink-0 p-0"
          aria-label={t('delivery.changes.flow.close')}
          onClick={onClose}
        >
          <X className="size-4" />
        </Button>
      </div>
      <ol className="flex flex-wrap items-stretch gap-1.5">
        {FLOW_STEPS.map((step, index) => (
          <li key={step} className="flex min-w-0 flex-1 basis-40 items-center gap-1.5">
            <div className="grid min-w-0 flex-1 gap-0.5 rounded-lg border border-border bg-surface-2 px-3 py-2">
              <span className="flex items-center gap-1.5 text-xs font-semibold text-ink-1">
                <span className="grid size-4.5 shrink-0 place-items-center rounded-full bg-brand-50 text-[10px] font-bold text-brand">
                  {index + 1}
                </span>
                {t(`delivery.changes.flow.steps.${step}.name`)}
              </span>
              <span className="text-xs leading-relaxed text-ink-3">
                {t(`delivery.changes.flow.steps.${step}.desc`)}
              </span>
            </div>
            {index < FLOW_STEPS.length - 1 && (
              <ChevronRight className="size-4 shrink-0 text-ink-3/60" aria-hidden />
            )}
          </li>
        ))}
      </ol>
    </section>
  )
}
