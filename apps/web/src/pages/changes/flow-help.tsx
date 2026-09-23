// 「交付流程」帮助弹窗（模态）：五步生命周期横向图示
// 创建 → 审批 → 灰度批次 → 观察 → 完成/回滚，每步一句话，给新手一张全局地图。
// 以模态呈现而非页内内联块——内联会把下方变更单列表整体下推、改变页面布局。
import { useTranslation } from 'react-i18next'

import { ChevronRight } from 'lucide-react'

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@beacon/ui'

const FLOW_STEPS = ['create', 'approve', 'batch', 'observe', 'finish'] as const

interface FlowHelpProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export default function FlowHelp({ open, onOpenChange }: FlowHelpProps) {
  const { t } = useTranslation()
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>{t('delivery.changes.flow.title')}</DialogTitle>
          <DialogDescription>{t('delivery.changes.flow.subtitle')}</DialogDescription>
        </DialogHeader>
        <ol className="flex flex-col gap-1.5 sm:flex-row sm:flex-wrap sm:items-stretch">
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
                <ChevronRight className="hidden size-4 shrink-0 text-ink-3/60 sm:block" aria-hidden />
              )}
            </li>
          ))}
        </ol>
      </DialogContent>
    </Dialog>
  )
}
