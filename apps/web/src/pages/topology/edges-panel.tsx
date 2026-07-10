// 消息异常链路：messages/stats 边聚合，异常边（失败/过期）高亮；点击看明细（样本消息 + 主要失败原因）。
// 明细支持与 /commands、/audits 互跳（FR-157）。

import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'
import { ArrowRight, ArrowRightLeft, ExternalLink, GitCompareArrows } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  DataTable,
  SummaryStrip,
  cn,
  type DataTableColumn,
} from '@beacon/ui'
import type { MessageEdgeStat } from '@beacon/contracts'

import { fetchMessageEdges } from '../../api/cluster'

// 边的失败率阈值：超过即视为异常边高亮
const ABNORMAL_RATE = 5

export default function EdgesPanel() {
  const { t } = useTranslation()
  const [selectedKey, setSelectedKey] = useState<string | null>(null)

  const query = useQuery({ queryKey: ['message-edges'], queryFn: fetchMessageEdges })
  // 异常边（失败率高）排前，便于运维一眼看到问题链路
  const edges = useMemo(
    () => [...(query.data?.edges ?? [])].sort((a, b) => b.failRatePercent - a.failRatePercent),
    [query.data],
  )
  // 异常边计数：给标题旁的危急徽标
  const abnormalCount = useMemo(
    () => edges.filter((edge) => edge.failRatePercent >= ABNORMAL_RATE).length,
    [edges],
  )

  // 数据剖析聚合指标：总链路 / 总消息 / 异常边 / 最高失败率 / 最高 P95。
  const aggregates = useMemo(() => {
    const totalMessages = edges.reduce((sum, e) => sum + e.total, 0)
    const maxFailRate = edges.reduce((m, e) => Math.max(m, e.failRatePercent), 0)
    const maxP95 = edges.reduce((m, e) => Math.max(m, e.p95DurationMs), 0)
    return { totalMessages, maxFailRate, maxP95 }
  }, [edges])

  const edgeKey = (edge: MessageEdgeStat) => `${edge.sourceServerId}→${edge.resolvedServerId}`
  const selected = useMemo(
    () => edges.find((edge) => edgeKey(edge) === selectedKey) ?? null,
    [edges, selectedKey],
  )

  const columns = useMemo<DataTableColumn<MessageEdgeStat>[]>(
    () => [
      {
        header: t('cluster.topology.edges.source'),
        cell: (edge) => <span className="font-mono text-xs text-ink-1">{edge.sourceServerId}</span>,
      },
      {
        header: t('cluster.topology.edges.target'),
        cell: (edge) => <span className="font-mono text-xs text-ink-1">→ {edge.resolvedServerId}</span>,
      },
      { header: t('cluster.topology.edges.total'), cell: (edge) => <span className="tnum text-ink-2">{edge.total}</span> },
      {
        header: t('cluster.topology.edges.failRate'),
        cell: (edge) =>
          edge.failRatePercent >= ABNORMAL_RATE ? (
            <Badge variant="crit" className="gap-1.5 tnum">
              <span className="size-1.5 rounded-full bg-current" />
              {edge.failRatePercent}%
            </Badge>
          ) : (
            <span className="tnum text-ink-2">{edge.failRatePercent}%</span>
          ),
      },
      { header: t('cluster.topology.edges.p95'), cell: (edge) => <span className="tnum text-ink-3">{String(edge.p95DurationMs)}ms</span> },
    ],
    [t],
  )

  return (
    <section className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      <div className="flex items-center gap-2.5">
        <span
          className={cn(
            'grid size-[26px] place-items-center rounded-lg',
            abnormalCount > 0 ? 'bg-crit-bg text-crit' : 'bg-brand-50 text-brand',
          )}
        >
          <ArrowRightLeft className="size-[15px]" />
        </span>
        <h2 className="text-[13px] font-semibold text-ink-1">{t('cluster.topology.edges.title')}</h2>
        {abnormalCount > 0 && (
          <Badge variant="crit" className="tnum">
            {t('cluster.topology.edges.abnormal')} {abnormalCount}
          </Badge>
        )}
      </div>
      {/* 数据剖析聚合指标条：一眼看总量与最差链路 */}
      <SummaryStrip
        items={[
          { label: t('cluster.topology.data.linkCount'), value: edges.length },
          { label: t('cluster.topology.data.totalMessages'), value: aggregates.totalMessages },
          {
            label: t('cluster.topology.edges.abnormal'),
            value: abnormalCount,
            tone: abnormalCount > 0 ? 'danger' : 'muted',
          },
          {
            label: t('cluster.topology.data.maxFailRate'),
            value: `${String(aggregates.maxFailRate)}%`,
            tone: aggregates.maxFailRate >= ABNORMAL_RATE ? 'danger' : 'muted',
          },
          { label: t('cluster.topology.data.maxP95'), value: `${String(aggregates.maxP95)}ms` },
        ]}
      />
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {/* 主从布局：链路表占主区，选中明细进右侧固定侧面板（不落页面底部、不 reflow 主区） */}
        <div className="flex items-start gap-3">
          <div className="min-w-0 flex-1">
            <DataTable
              columns={columns}
              rows={edges}
              rowKey={(edge) => edgeKey(edge)}
              emptyText={t('cluster.topology.edges.empty')}
              density="compact"
              pageSize={20}
              onRowClick={(edge) => {
                setSelectedKey(edgeKey(edge))
              }}
              rowClassName={(edge) =>
                cn(
                  edge.failRatePercent >= ABNORMAL_RATE && 'bg-crit-bg',
                  edgeKey(edge) === selectedKey && 'ring-1 ring-brand ring-inset',
                )
              }
            />
          </div>

          {/* 链路明细固定侧面板：常驻布局列，选中链路在此展示明细 + 互跳入口，图不被推动 */}
          <aside className="w-[248px] shrink-0 self-start rounded-lg border border-border bg-surface-2 p-3 text-sm">
            {selected ? (
              <div className="grid gap-2.5">
                <p className="flex items-center gap-1.5 font-semibold text-ink-1">
                  <GitCompareArrows className="size-3.5 text-brand" />
                  {t('cluster.topology.edges.detailTitle')}
                </p>
                <p className="flex items-center gap-1 font-mono text-xs text-ink-2">
                  <span className="truncate">{selected.sourceServerId}</span>
                  <ArrowRight className="size-3 shrink-0 text-ink-4" />
                  <span className="truncate">{selected.resolvedServerId}</span>
                </p>

                {selected.topFailReasons.length > 0 && (
                  <div>
                    <p className="text-[11px] font-semibold tracking-[0.3px] text-ink-4 uppercase">
                      {t('cluster.topology.edges.topReasons')}
                    </p>
                    <ul className="mt-1.5 flex flex-wrap gap-1.5">
                      {selected.topFailReasons.map((reason) => (
                        <li key={reason.reason}>
                          <Badge variant="crit" className="tnum">
                            {reason.reason} · {reason.count}
                          </Badge>
                        </li>
                      ))}
                    </ul>
                  </div>
                )}

                <div>
                  <p className="text-[11px] font-semibold tracking-[0.3px] text-ink-4 uppercase">
                    {t('cluster.topology.edges.sampleMessages')}
                  </p>
                  <ul className="mt-1 grid gap-0.5">
                    {selected.sampleMessageIds.map((id) => (
                      <li key={id} className="truncate font-mono text-xs text-ink-3">
                        {id}
                      </li>
                    ))}
                  </ul>
                </div>

                {/* 与 /commands、/audits 互跳（FR-157） */}
                <div className="flex flex-wrap gap-3 pt-1 text-xs">
                  <Link className="flex items-center gap-1 text-brand-600 hover:underline" to="/commands">
                    <ExternalLink className="size-3" />
                    {t('cluster.topology.edges.viewInCommands')}
                  </Link>
                  <Link className="flex items-center gap-1 text-brand-600 hover:underline" to="/audits">
                    <ExternalLink className="size-3" />
                    {t('cluster.topology.edges.viewInAudits')}
                  </Link>
                </div>
              </div>
            ) : (
              <p className="py-6 text-center text-xs text-ink-4">{t('cluster.topology.graph.detailEmpty')}</p>
            )}
          </aside>
        </div>
      </AsyncSection>
    </section>
  )
}
