// 集群健康总览（对齐 B 版顶部指标带）：区段标题 + KPI 卡行（可调度 / 代理·子服 / 玩家 / TPS +
// 健康等级分布环）。异常可下钻到 /servers。数据源 metrics/summary。
// 空态（无任何服务器）给接入引导；KPI 卡行细节委托给 KpiStrip。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ChevronRight } from 'lucide-react'

import { fetchMetricsSummary } from '../../api/metrics'
import KpiStrip from './kpi-strip'

export default function HealthOverview() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['dashboard', 'metrics-summary'],
    queryFn: fetchMetricsSummary,
  })
  const data = query.data

  // 无任何服务器视为空态（接入引导）
  const isEmpty = data?.byKind.proxy.total === 0 && data.byKind.backend.total === 0

  return (
    <section className="grid gap-3">
      <div className="flex items-center justify-between">
        <h2 className="text-[15px] font-semibold text-ink-1">{t('dashboard.health.title')}</h2>
        <Link
          className="flex items-center gap-0.5 text-xs text-brand-600 hover:underline"
          to="/servers"
        >
          {t('dashboard.health.viewServers')}
          <ChevronRight className="size-3" />
        </Link>
      </div>
      {isEmpty ? (
        <p className="rounded-xl border border-border bg-card p-4 text-sm text-ink-3 shadow-card">
          {t('dashboard.health.empty')}
        </p>
      ) : (
        <KpiStrip />
      )}
    </section>
  )
}
