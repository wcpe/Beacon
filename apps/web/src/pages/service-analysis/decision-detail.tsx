// 调度决策详情（右侧非模态面板内容）：按 traceId 拉取单条详情，展示决策上下文全字段
// 与逐台排除原因（可解释「为什么没选某台」）。只读，不提供任何写操作。

import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, Badge, CardGridSkeleton } from '@beacon/ui'

import { fetchSchedDecisionDetail } from '../../api/metrics'

interface DecisionDetailProps {
  // 目标决策 traceId
  traceId: string
}

export default function DecisionDetail({ traceId }: DecisionDetailProps) {
  const { t } = useTranslation()
  const query = useQuery({
    queryKey: ['service-analysis', 'sched-decision-detail', traceId],
    queryFn: () => fetchSchedDecisionDetail(traceId),
  })
  const detail = query.data
  const dash = t('observability.serviceAnalysis.dash')
  const fields = 'observability.serviceAnalysis.decisions.fields'

  return (
    <AsyncSection
      isLoading={query.isLoading}
      isError={query.isError}
      error={query.error}
      skeleton={<CardGridSkeleton count={2} />}
    >
      {detail !== undefined && (
        <div className="grid gap-3 text-sm">
          {/* 结果 + 决策来源（控制面 / 本地降级）药丸 */}
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant={detail.failReason === null ? 'ok' : 'crit'}>
              {detail.failReason === null
                ? t('observability.serviceAnalysis.decisions.result.success')
                : t('observability.serviceAnalysis.decisions.result.failed')}
            </Badge>
            <Badge variant={detail.source === 'local_fallback' ? 'warn' : 'brand'}>
              {t(`observability.serviceAnalysis.decisions.source.${detail.source}`)}
            </Badge>
          </div>

          <Field label={t(`${fields}.traceId`)} value={detail.traceId} mono />
          <Field label={t(`${fields}.time`)} value={new Date(detail.tsMs).toLocaleString()} />

          {/* 决策上下文：双列紧凑 KV */}
          <div className="grid grid-cols-2 gap-3">
            <Field label={t(`${fields}.requester`)} value={detail.requesterServerId} mono />
            <Field label={t(`${fields}.zone`)} value={detail.zoneName} mono />
            <Field label={t(`${fields}.plugin`)} value={detail.plugin ?? dash} />
            <Field label={t(`${fields}.purpose`)} value={detail.purpose ?? dash} />
            <Field label={t(`${fields}.strategy`)} value={detail.strategy} mono />
            <Field
              label={t(`${fields}.weightsRev`)}
              value={detail.weightsRev === null ? dash : String(detail.weightsRev)}
            />
            <Field label={t(`${fields}.candidates`)} value={String(detail.candidateCount)} />
            <Field label={t(`${fields}.excluded`)} value={String(detail.excludedCount)} />
            <Field label={t(`${fields}.chosen`)} value={detail.chosenServerId ?? dash} mono />
            <Field
              label={t(`${fields}.chosenScore`)}
              value={detail.chosenServerId === null ? dash : String(detail.chosenScore)}
            />
            <Field label={t(`${fields}.namespace`)} value={String(detail.namespaceId)} />
            <Field
              label={t(`${fields}.crossNamespace`)}
              value={detail.crossNamespace ? t('observability.serviceAnalysis.yes') : t('observability.serviceAnalysis.no')}
            />
            <Field
              label={t(`${fields}.duration`)}
              value={t('observability.serviceAnalysis.decisions.durationMs', { count: detail.durationMs })}
            />
            <Field label={t(`${fields}.failReason`)} value={detail.failReason ?? dash} />
          </div>

          {/* 逐台排除原因：解释「为什么没选某台」 */}
          <div className="grid gap-1.5 border-t border-border pt-3">
            <span className="text-xs font-semibold text-ink-2">
              {t('observability.serviceAnalysis.decisions.excludedTitle')}
            </span>
            {detail.excluded.length === 0 ? (
              <p className="text-xs text-ink-4">{t('observability.serviceAnalysis.decisions.excludedEmpty')}</p>
            ) : (
              <ul className="grid gap-1">
                {detail.excluded.map((row, idx) => (
                  <li
                    key={`${row.serverId}:${String(idx)}`}
                    className="flex items-center justify-between gap-2 rounded-lg bg-secondary/60 px-2.5 py-1.5"
                  >
                    <span className="font-mono text-xs text-ink-2">{row.serverId}</span>
                    <Badge variant="off">{row.reason}</Badge>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      )}
    </AsyncSection>
  )
}

// 单个只读字段（标签 + 值）
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className={mono ? 'font-mono text-xs break-all text-ink-2' : 'text-sm text-ink-1'}>{value}</span>
    </div>
  )
}
