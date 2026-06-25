// 服务分析 / 平台用量看板页（FR-73）：按时间窗（近 7 天 / 30 天）+ 环境聚合运维操作审计活动，
// 展示 KPI 卡片（总数 / 成功 / 失败 / 成功率）+ 按动作分布排行 + 每日趋势折线。
// 复用 FR-32 看板范式（环境 Combobox + 时间窗 Tabs + StatCard + recharts），不引新图表库。
// 边界：与 FR-32 负载看板（运行时遥测）数据源 / 页面 / 刷新节奏独立——本页是「人/系统对平台做了什么」。

import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Activity, CheckCircle2, Percent, XCircle } from 'lucide-react'
import { getAuditAnalytics, listNamespaces } from '../api/client'
import { namespaceOptions } from '../api/format'
import StatCard from './dashboard/StatCard'
import DayTrendChart from './service-analysis/DayTrendChart'
import ActionRankChart from './service-analysis/ActionRankChart'
import type { ActionRankItem } from './service-analysis/ActionRankChart'
import AsyncSection from '@/components/AsyncSection'
import { CardGridSkeleton } from '@/components/skeletons'
import { Skeleton } from '@/components/ui/skeleton'
import { Label } from '@/components/ui/label'
import { Card, CardContent } from '@/components/ui/card'
import { Combobox } from '@/components/ui/combobox'
import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

// 时间窗预设（天数）：本地算 from/to 传 RFC3339；窗口上限 92 天内，故 7/30 天均合法。
type AnalyticsWindow = '7d' | '30d'
const WINDOWS: Array<{ value: AnalyticsWindow; days: number; labelKey: string }> = [
  { value: '7d', days: 7, labelKey: 'serviceAnalysis.win7d' },
  { value: '30d', days: 30, labelKey: 'serviceAnalysis.win30d' },
]
const DEFAULT_WINDOW: AnalyticsWindow = '30d'

// 一天的毫秒数（窗口 from 由 to 回退 days 天得到）
const MS_PER_DAY = 24 * 60 * 60 * 1000

// 由窗口天数算 [from, to] 的 RFC3339 字符串（to=当前，from=to-days 天）。
function windowRange(days: number): { from: string; to: string } {
  const now = Date.now()
  return {
    from: new Date(now - days * MS_PER_DAY).toISOString(),
    to: new Date(now).toISOString(),
  }
}

export default function ServiceAnalysisPage() {
  const { t } = useTranslation()
  // 审计 action 英文枚举 → 中文显示：复用 AuditsPage 同一份 i18n key（未知枚举回退原文）
  const actionLabel = (action: string) => t(`audit.action.${action}`, { defaultValue: action })

  // 环境过滤（可编辑下拉，FR-51）：空表示聚合全部环境，进页默认即聚合全部。
  const [namespace, setNamespace] = useState('')
  const [window, setWindow] = useState<AnalyticsWindow>(DEFAULT_WINDOW)

  // 环境下拉候选来自 listNamespaces；候选显示「编码 · 名称」，真实值仍是 code（FR-70）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })
  const nsOptions = useMemo(() => namespaceOptions(namespacesQuery.data), [namespacesQuery.data])

  // 选中窗口对应的天数；切窗口或重渲染时据当前时刻算 from/to。
  const days = WINDOWS.find((w) => w.value === window)?.days ?? 30
  // useMemo 依赖 window，使切窗口时重算 range（同窗口内引用稳定，不抖动 queryKey）。
  const range = useMemo(() => windowRange(days), [days])

  const analyticsQuery = useQuery({
    queryKey: ['audit-analytics', namespace, range.from, range.to],
    queryFn: () =>
      getAuditAnalytics({ namespace: namespace || undefined, from: range.from, to: range.to }),
  })

  const data = analyticsQuery.data
  const total = data?.total ?? 0
  const okCount = data?.okCount ?? 0
  const failCount = data?.failCount ?? 0
  // 成功率 = okCount / total；total=0 时显 0%（不除零），否则保留一位小数百分比。
  const successRate = total > 0 ? `${((okCount / total) * 100).toFixed(1)}%` : '0%'

  // 按动作排行：后端已降序，前端原样透传并映射中文标签喂图。
  const rankItems: ActionRankItem[] = useMemo(
    () => (data?.byAction ?? []).map((a) => ({ action: a.action, label: actionLabel(a.action), count: a.count })),
    // actionLabel 闭包稳定（依赖 t），按 data 变更重算即可
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [data],
  )
  const dayPoints = data?.byDay ?? []

  const isFetching = analyticsQuery.isFetching

  return (
    <div className="space-y-4">
      <div className="flex items-center gap-3">
        <h1 className="text-xl font-semibold">{t('serviceAnalysis.title')}</h1>
        {isFetching && <span className="text-sm text-muted-foreground">{t('common.refreshing')}</span>}
      </div>
      <p className="text-sm text-muted-foreground">{t('serviceAnalysis.subtitle')}</p>

      <Card>
        <CardContent>
          <div className="flex flex-wrap items-end justify-between gap-3">
            <div className="space-y-1.5">
              <Label htmlFor="sa-namespace">{t('common.namespace')}</Label>
              {/* 环境筛选：可编辑下拉，候选来自 API 但允许键入列表外值（FR-51）；留空聚合全部环境。
                  clearable：选某环境后可一键清回空值（聚合全部）。 */}
              <Combobox
                id="sa-namespace"
                aria-label={t('common.namespace')}
                className="w-40"
                placeholder={t('serviceAnalysis.nsPlaceholder')}
                value={namespace}
                onChange={setNamespace}
                options={nsOptions}
                allowCustom
                clearable
                clearLabel={t('serviceAnalysis.clearFilter')}
              />
            </div>
            {/* 时间窗 Tabs：切换即据当前时刻算 from/to 传 RFC3339 重查。 */}
            <Tabs value={window} onValueChange={(v) => setWindow(v as AnalyticsWindow)}>
              <TabsList>
                {WINDOWS.map((w) => (
                  <TabsTrigger key={w.value} value={w.value}>
                    {t(w.labelKey)}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Tabs>
          </div>
        </CardContent>
      </Card>

      <AsyncSection
        isLoading={analyticsQuery.isLoading}
        isError={analyticsQuery.isError}
        error={analyticsQuery.error}
        skeleton={
          // KPI 卡 + 两图占位：贴近真实「4 卡 + 2 图」布局
          <div className="space-y-4">
            <CardGridSkeleton count={4} />
            <div className="grid grid-cols-1 gap-4 xl:grid-cols-2">
              <Skeleton className="h-64 w-full rounded-xl" />
              <Skeleton className="h-64 w-full rounded-xl" />
            </div>
          </div>
        }
      >
        {/* KPI 卡片：总数 / 成功 / 失败 / 成功率 */}
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard
            label={t('serviceAnalysis.cardTotal')}
            value={total}
            icon={<Activity className="size-4" />}
          />
          <StatCard
            label={t('serviceAnalysis.cardOk')}
            value={okCount}
            icon={<CheckCircle2 className="size-4" />}
          />
          <StatCard
            label={t('serviceAnalysis.cardFail')}
            value={failCount}
            icon={<XCircle className="size-4" />}
          />
          <StatCard
            label={t('serviceAnalysis.cardSuccessRate')}
            value={successRate}
            icon={<Percent className="size-4" />}
          />
        </div>

        <div className="mt-4 grid grid-cols-1 gap-4 xl:grid-cols-2">
          {/* 按动作分布排行（降序柱状） */}
          <Card>
            <CardContent className="space-y-3">
              <div className="text-base font-medium">{t('serviceAnalysis.byActionTitle')}</div>
              <ActionRankChart items={rankItems} />
            </CardContent>
          </Card>

          {/* 每日趋势折线 */}
          <Card>
            <CardContent className="space-y-3">
              <div className="text-base font-medium">{t('serviceAnalysis.byDayTitle')}</div>
              <DayTrendChart points={dayPoints} />
            </CardContent>
          </Card>
        </div>
      </AsyncSection>
    </div>
  )
}
