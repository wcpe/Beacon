// 服务分析页（/service-analysis）：左右分栏——左侧吸顶服务器选择列（可搜索多选，固定不随右侧滚），
// 右侧主区并排展示所选服务器指标时序与多服对比（控制条吸顶常驻，选服即时出图，无需长滚）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { LineChart, MousePointerClick } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'

import ServerPicker from './service-analysis/server-picker'
import SeriesPanel from './service-analysis/series-panel'

type MetricKey = 'cpu' | 'tps' | 'mem' | 'online'

export default function ServiceAnalysisPage() {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [metric, setMetric] = useState<MetricKey>('cpu')
  const [step, setStep] = useState(60)

  const toggle = (serverId: string) => {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(serverId)) {
        next.delete(serverId)
      } else {
        next.add(serverId)
      }
      return next
    })
  }

  const serverIds = [...selected]

  return (
    <section className="grid gap-5">
      <SectionHeader
        size="lg"
        icon={<LineChart className="size-5" />}
        title={t('nav.serviceAnalysis')}
        count={t('observability.serviceAnalysis.mission')}
      />
      {/* 左侧选择列（固定宽度吸顶）+ 右侧主区（占剩余宽度） */}
      <div className="grid gap-4 lg:grid-cols-[17rem_minmax(0,1fr)] xl:grid-cols-[19rem_minmax(0,1fr)]">
        <ServerPicker
          selected={selected}
          onToggle={toggle}
          onClear={() => {
            setSelected(new Set())
          }}
        />
        <div className="min-w-0">
          {serverIds.length === 0 ? (
            <div className="flex items-center gap-2.5 rounded-xl border border-dashed border-border bg-card/60 px-4 py-16 text-sm text-ink-3">
              <MousePointerClick className="size-4 shrink-0 text-ink-4" />
              {t('observability.serviceAnalysis.pickHint')}
            </div>
          ) : (
            <SeriesPanel
              serverIds={serverIds}
              metric={metric}
              onMetricChange={setMetric}
              step={step}
              onStepChange={setStep}
            />
          )}
        </div>
      </div>
    </section>
  )
}
