// 集群健康总览（对齐 B 版顶部指标带）：KPI 卡行（可调度 / 代理·子服 / 玩家 / TPS +
// 健康等级分布环）。数据源 metrics/summary；空态（无任何服务器）给接入引导。
// KPI 卡行细节委托给 KpiStrip。
//
// E5 补齐：内容区不再渲染区段大标题（页面身份已在页眉面包屑）。原「前往服务器」下钻链接
// 亦已移除——/servers 入口由状态墙卡的「查看全部」承担，同一页不留重复入口。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { CardGridSkeleton } from '@beacon/ui'

import { fetchMetricsSummary } from '../../api/metrics'
import PageSkeleton from '../../features/shared/page-skeleton'
import KpiStrip from './kpi-strip'

export default function HealthOverview() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['dashboard', 'metrics-summary'],
    queryFn: fetchMetricsSummary,
  })
  const data = query.data

  // 首屏加载：给与 KPI 卡同形的骨架，避免空白闪烁。
  // 有缓存数据的后台重新获取不替换内容（避免每次刷新闪白）。
  if (query.isPending) {
    return (
      <section className="grid gap-2">
        <PageSkeleton>
          <CardGridSkeleton count={4} />
        </PageSkeleton>
      </section>
    )
  }

  // 无任何服务器视为空态（接入引导）。data 在加载完成后才读取，此处置空防御。
  const isEmpty = (data?.byKind.proxy.total ?? 0) === 0 && (data?.byKind.backend.total ?? 0) === 0

  return (
    <section className="grid gap-2">
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
