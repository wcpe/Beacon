// 服务分析页（/service-analysis）：左右分栏——左侧吸顶服务器选择列（可搜索多选，固定不随右侧滚），
// 右侧主区分「指标时序 / 数据对比 / 调度决策 / 健康快照」板块（吸顶切换常驻）。指标时序 / 数据对比 /
// 健康快照选服即时出图；调度决策自带时间窗与筛选、不依赖左侧选服。支持 ?view= 定位板块
//（dashboard 调度概览下钻入口）。选中服务器经 localStorage 持久化，刷新恢复。
import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { GitCompareArrows, History, LineChart, MousePointerClick, TrendingUp, Workflow } from 'lucide-react'

import { PageHeader, cn } from '@beacon/ui'

import { fetchServers } from '../api/cluster'
import {
  resolveApiNamespaceId,
  useEnvNamespaceScope,
} from '../features/env/use-env-scope'
import {
  setServiceAnalysisSelected,
  useServiceAnalysisSelected,
} from '../state/service-analysis-selection'
import ComparePanel from './service-analysis/compare-panel'
import DecisionsPanel from './service-analysis/decisions-panel'
import ServerPicker from './service-analysis/server-picker'
import SeriesPanel from './service-analysis/series-panel'
import SnapshotsPanel from './service-analysis/snapshots-panel'

type MetricKey = 'cpu' | 'tps' | 'mem' | 'online'
// 右侧区板块：指标时序 / 数据对比 / 调度决策 / 健康快照
const PANEL_TABS = ['series', 'compare', 'decisions', 'snapshots'] as const
type PanelTab = (typeof PANEL_TABS)[number]

// 校验 URL ?view= 参数是否为合法板块名
function isPanelTab(value: string | null): value is PanelTab {
  return value !== null && (PANEL_TABS as readonly string[]).includes(value)
}

export default function ServiceAnalysisPage() {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  // 从持久化恢复上次选中；toggle/clear 同步写回 localStorage
  const persistedIds = useServiceAnalysisSelected()
  const selected = useMemo(() => new Set(persistedIds), [persistedIds])
  const [metric, setMetric] = useState<MetricKey>('cpu')
  const [step, setStep] = useState(60)
  // 初始板块可由 ?view= 指定（如 dashboard 调度概览下钻到调度决策）
  const [tab, setTab] = useState<PanelTab>(() => {
    const view = searchParams.get('view')
    return isPanelTab(view) ? view : 'series'
  })

  // 在线子服列表：用于剪掉 localStorage 里已不存在 / 已下线的幽灵选中（避免右侧卡旧 game1）
  const envScope = useEnvNamespaceScope()
  const apiNamespaceId = resolveApiNamespaceId(undefined, envScope)
  const serversQuery = useQuery({
    queryKey: ['service-analysis', 'servers', apiNamespaceId, envScope],
    queryFn: () => fetchServers({ kind: 'backend', namespaceId: apiNamespaceId, pageSize: 200 }),
  })
  useEffect(() => {
    if (!serversQuery.isSuccess || persistedIds.length === 0) {
      return
    }
    const onlineIds = new Set(
      (serversQuery.data?.items ?? []).filter((s) => s.online).map((s) => s.serverId),
    )
    // 只保留仍在线的；若全部失效则清空，避免分析区卡在已下线 id
    const kept = persistedIds.filter((id) => onlineIds.has(id))
    if (kept.length !== persistedIds.length) {
      setServiceAnalysisSelected(kept)
    }
  }, [serversQuery.isSuccess, serversQuery.data, persistedIds])

  const toggle = (serverId: string) => {
    const next = new Set(selected)
    if (next.has(serverId)) {
      next.delete(serverId)
    } else {
      next.add(serverId)
    }
    setServiceAnalysisSelected(next)
  }

  const clearSelected = () => {
    setServiceAnalysisSelected([])
  }

  // 右侧分析仅用仍在线的选中，避免幽灵 id 触发空/错图
  const onlineIdSet = useMemo(
    () => new Set((serversQuery.data?.items ?? []).filter((s) => s.online).map((s) => s.serverId)),
    [serversQuery.data],
  )
  const serverIds = useMemo(
    () => persistedIds.filter((id) => onlineIdSet.has(id)),
    [persistedIds, onlineIdSet],
  )

  return (
    <section className="grid gap-5">
      <PageHeader
        icon={<LineChart className="size-5" />}
        title={t('nav.serviceAnalysis')}
        description={t('observability.serviceAnalysis.mission')}
      />
      {/* 左侧选择列（固定宽度吸顶）+ 右侧主区（占剩余宽度） */}
      <div className="grid gap-4 lg:grid-cols-[17rem_minmax(0,1fr)] xl:grid-cols-[19rem_minmax(0,1fr)]">
        <ServerPicker selected={selected} onToggle={toggle} onClear={clearSelected} />
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
              <PanelTabButton
                active={tab === 'snapshots'}
                onClick={() => {
                  setTab('snapshots')
                }}
                icon={<History className="size-4" />}
                label={t('observability.serviceAnalysis.tabSnapshots')}
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
            {tab === 'snapshots' &&
              (serverIds.length === 0 ? (
                <PickHint text={t('observability.serviceAnalysis.pickHintSnapshots')} />
              ) : (
                <SnapshotsPanel serverIds={serverIds} />
              ))}
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

// 板块切换按钮（指标时序 / 数据对比 / 调度决策 / 健康快照）：激活态品牌底色高亮，role=tab 便于测试与无障碍定位。
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
