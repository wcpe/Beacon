// 服务分析页（/service-analysis）：左右分栏——左侧吸顶服务器选择列（可搜索多选，固定不随右侧滚），
// 右侧主区分「指标时序 / 数据对比」两板块（吸顶切换常驻，选服即时出图，无需长滚）。
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { GitCompareArrows, LineChart, MousePointerClick, TrendingUp } from 'lucide-react'

import { SectionHeader, cn } from '@beacon/ui'

import ComparePanel from './service-analysis/compare-panel'
import ServerPicker from './service-analysis/server-picker'
import SeriesPanel from './service-analysis/series-panel'

type MetricKey = 'cpu' | 'tps' | 'mem' | 'online'
// 右侧区板块：指标时序 / 数据对比
type PanelTab = 'series' | 'compare'

export default function ServiceAnalysisPage() {
  const { t } = useTranslation()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [metric, setMetric] = useState<MetricKey>('cpu')
  const [step, setStep] = useState(60)
  const [tab, setTab] = useState<PanelTab>('series')

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
            <div className="grid gap-3">
              {/* 板块切换：指标时序 / 数据对比（吸顶常驻，选服后无需下滚即可切换） */}
              <div className="sticky top-0 z-20 -mx-1 flex gap-1 bg-surface-2/95 px-1 py-1 backdrop-blur supports-backdrop-filter:bg-surface-2/80">
                <PanelTabButton
                  active={tab === 'series'}
                  onClick={() => {
                    setTab('series')
                  }}
                  icon={<TrendingUp className="size-4" />}
                  label={t('observability.serviceAnalysis.tabSeries')}
                />
                <PanelTabButton
                  active={tab === 'compare'}
                  onClick={() => {
                    setTab('compare')
                  }}
                  icon={<GitCompareArrows className="size-4" />}
                  label={t('observability.serviceAnalysis.tabCompare')}
                />
              </div>
              {tab === 'series' ? (
                <SeriesPanel
                  serverIds={serverIds}
                  metric={metric}
                  onMetricChange={setMetric}
                  step={step}
                  onStepChange={setStep}
                />
              ) : (
                <ComparePanel serverIds={serverIds} />
              )}
            </div>
          )}
        </div>
      </div>
    </section>
  )
}

// 板块切换按钮（指标时序 / 数据对比）：激活态品牌底色高亮，role=tab 便于测试与无障碍定位。
function PanelTabButton({
  active,
  onClick,
  icon,
  label,
}: {
  active: boolean
  onClick: () => void
  icon: ReactNode
  label: string
}) {
  return (
    <button
      type="button"
      role="tab"
      aria-selected={active}
      onClick={onClick}
      className={cn(
        'inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-[13px] font-medium transition-colors',
        active ? 'bg-brand text-white shadow-sm' : 'text-ink-3 hover:bg-brand-50/60 hover:text-brand-700',
      )}
    >
      {icon}
      {label}
    </button>
  )
}
