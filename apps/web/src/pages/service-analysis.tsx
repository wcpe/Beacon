// 服务分析页（/service-analysis）：左右分栏——左侧吸顶服务器选择列（可搜索多选，固定不随右侧滚），
// 右侧主区分「指标时序 / 数据对比 / 调度决策」板块（吸顶切换常驻）。指标时序 / 数据对比选服即时出图；
// 调度决策自带时间窗与筛选、不依赖左侧选服。支持 ?view= 定位板块（dashboard 调度概览下钻入口）。
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { GitCompareArrows, LineChart, MousePointerClick, TrendingUp, Workflow } from 'lucide-react'

import { SectionHeader, cn } from '@beacon/ui'

import ComparePanel from './service-analysis/compare-panel'
import DecisionsPanel from './service-analysis/decisions-panel'
import ServerPicker from './service-analysis/server-picker'
import SeriesPanel from './service-analysis/series-panel'

type MetricKey = 'cpu' | 'tps' | 'mem' | 'online'
// 右侧区板块：指标时序 / 数据对比 / 调度决策
const PANEL_TABS = ['series', 'compare', 'decisions'] as const
type PanelTab = (typeof PANEL_TABS)[number]

// 校验 URL ?view= 参数是否为合法板块名
function isPanelTab(value: string | null): value is PanelTab {
  return value !== null && (PANEL_TABS as readonly string[]).includes(value)
}

export default function ServiceAnalysisPage() {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [metric, setMetric] = useState<MetricKey>('cpu')
  const [step, setStep] = useState(60)
  // 初始板块可由 ?view= 指定（如 dashboard 调度概览下钻到调度决策）
  const [tab, setTab] = useState<PanelTab>(() => {
    const view = searchParams.get('view')
    return isPanelTab(view) ? view : 'series'
  })

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
          <div className="grid gap-3">
            {/* 板块切换（吸顶常驻）：调度决策不依赖选服，未选服也可直达 */}
            <div className="sticky top-0 z-20 -mx-1 flex flex-wrap gap-1 bg-surface-2/95 px-1 py-1 backdrop-blur supports-backdrop-filter:bg-surface-2/80">
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
              <PanelTabButton
                active={tab === 'decisions'}
                onClick={() => {
                  setTab('decisions')
                }}
                icon={<Workflow className="size-4" />}
                label={t('observability.serviceAnalysis.tabDecisions')}
              />
            </div>
            {tab === 'series' &&
              (serverIds.length === 0 ? (
                <PickHint text={t('observability.serviceAnalysis.pickHint')} />
              ) : (
                <SeriesPanel
                  serverIds={serverIds}
                  metric={metric}
                  onMetricChange={setMetric}
                  step={step}
                  onStepChange={setStep}
                />
              ))}
            {tab === 'compare' &&
              (serverIds.length === 0 ? (
                <PickHint text={t('observability.serviceAnalysis.pickHint')} />
              ) : (
                <ComparePanel serverIds={serverIds} />
              ))}
            {tab === 'decisions' && <DecisionsPanel />}
          </div>
        </div>
      </div>
    </section>
  )
}

// 未选服引导（虚线空态框）：依赖选服的板块在选服前的占位
function PickHint({ text }: { text: string }) {
  return (
    <div className="flex items-center gap-2.5 rounded-xl border border-dashed border-border bg-card/60 px-4 py-16 text-sm text-ink-3">
      <MousePointerClick className="size-4 shrink-0 text-ink-4" />
      {text}
    </div>
  )
}

// 板块切换按钮（指标时序 / 数据对比 / 调度决策）：激活态品牌底色高亮，role=tab 便于测试与无障碍定位。
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
