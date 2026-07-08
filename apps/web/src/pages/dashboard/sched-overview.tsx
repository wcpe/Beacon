// 调度概览卡（对齐 B 版）：大号成功率 + 决策总数 / 成功落位 / 失败重试 KV + 失败原因 Top 分布条。
// 下钻到 /service-analysis、/alert-events。数据源 sched-decisions/summary（最近 1h 窗口）。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ChevronRight, Workflow } from 'lucide-react'

import { AsyncSection, CardGridSkeleton } from '@beacon/ui'

import { fetchSchedSummary } from '../../api/metrics'

export default function SchedOverview() {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['dashboard', 'sched-summary'],
    queryFn: () => fetchSchedSummary('1h'),
  })
  const data = query.data

  // 失败原因条：以最大计数为满宽基准。
  const failTop = data?.failReasonTop.slice(0, 3) ?? []
  const failMax = failTop.length === 0 ? 1 : Math.max(...failTop.map((r) => r.count), 1)
  const successCount = data ? data.successCount : 0
  const failCount = data ? data.total - data.successCount : 0

  return (
    <section className="grid grid-rows-[auto_1fr] gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <Workflow className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('dashboard.sched.title')}</h2>
        <span className="text-[11px] text-ink-4">{t('dashboard.sched.window1h')}</span>
        <div className="ml-auto flex gap-3 text-[11.5px]">
          <Link className="flex items-center gap-0.5 text-brand-600 hover:underline" to="/service-analysis">
            {t('dashboard.sched.viewAnalysis')}
            <ChevronRight className="size-3" />
          </Link>
          <Link className="flex items-center gap-0.5 text-brand-600 hover:underline" to="/alert-events">
            {t('dashboard.sched.viewAlerts')}
            <ChevronRight className="size-3" />
          </Link>
        </div>
      </div>
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={3} />}
      >
        {!data || data.total === 0 ? (
          <p className="text-sm text-ink-3">{t('dashboard.sched.empty')}</p>
        ) : (
          <div className="grid gap-3.5">
            {/* 大号成功率 + KV */}
            <div className="flex items-center gap-5">
              <div className="flex flex-col">
                <div className="text-[30px] leading-none font-bold tracking-[-1px] text-ink-1 tnum">
                  {data.successRatePercent}%
                </div>
                <div className="mt-1.5 text-[11.5px] text-ink-3">{t('dashboard.sched.successRate')}</div>
              </div>
              <div className="h-11 w-px bg-border" />
              <div className="flex flex-1 flex-col gap-1">
                <KvRow label={t('dashboard.sched.total')} value={data.total} />
                <KvRow label={t('dashboard.sched.landed')} value={successCount} tone="ok" />
                <KvRow label={t('dashboard.sched.retried')} value={failCount} tone="warn" />
              </div>
            </div>
            {/* 失败原因 Top */}
            {failTop.length > 0 && (
              <div className="grid gap-2.5">
                <div className="text-[10.5px] font-semibold tracking-[0.5px] text-ink-4 uppercase">
                  {t('dashboard.sched.failTop')}
                </div>
                <div className="flex flex-col gap-2.5">
                  {failTop.map((reason) => (
                    <div key={reason.reason} className="flex items-center gap-2.5 text-[12px]">
                      <span className="w-28 shrink-0 text-ink-2">{reason.reason}</span>
                      <span className="h-[7px] flex-1 overflow-hidden rounded-full bg-muted">
                        <span
                          className="block h-full rounded-full bg-gradient-to-r from-warn to-crit"
                          style={{ width: `${String((reason.count / failMax) * 100)}%` }}
                        />
                      </span>
                      <span className="w-7 text-right font-bold text-ink-1 tnum">{reason.count}</span>
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        )}
      </AsyncSection>
    </section>
  )
}

// 键值行：弱色标签 + 右对齐加粗数值（可选语义色）。
function KvRow({ label, value, tone }: { label: string; value: number; tone?: 'ok' | 'warn' }) {
  const toneClass = tone === 'ok' ? 'text-ok' : tone === 'warn' ? 'text-warn' : 'text-ink-1'
  return (
    <div className="flex items-center gap-2 text-[12px]">
      <span className="text-ink-3">{label}</span>
      <span className={`ml-auto font-bold tnum ${toneClass}`}>{value.toLocaleString()}</span>
    </div>
  )
}
