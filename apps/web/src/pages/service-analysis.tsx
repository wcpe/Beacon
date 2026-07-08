// 服务分析页（/service-analysis）：指标聚合、趋势与对比。
// 选在线子服（可多选）→ 看指标时序（CPU/TPS/内存/在线人数）与多服对比。
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
      <ServerPicker
        selected={selected}
        onToggle={toggle}
        onClear={() => {
          setSelected(new Set())
        }}
      />
      {serverIds.length === 0 ? (
        <div className="flex items-center gap-2.5 rounded-xl border border-dashed border-border bg-card/60 px-4 py-8 text-sm text-ink-3">
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
    </section>
  )
}
