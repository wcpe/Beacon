// 影响预览 Tab：GET impact → 共享编排预览（目标范围 / 批次占比累计 / 生效方式 /
// 影响面汇总）+ 逐目标分页表。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, Badge, DataTable, type DataTableColumn } from '@beacon/ui'

import {
  fetchChangeImpact,
  type ChangeImpactConfigScope,
  type ChangeImpactTarget,
  type ChangeOrderDetail,
} from '../../api/delivery-changes'
import OrderOrchestration from '../../features/delivery/order-orchestration'
import Pager from '../../features/delivery/pager'

const PAGE_SIZE = 20

interface ImpactTabProps {
  order: ChangeOrderDetail
}

// 配置命中紧凑展示：作用域 + from→to 版本 id（无来源版本 = 首次下发）
function configScopeText(scope: ChangeImpactConfigScope): string {
  const range =
    scope.fromVersionId === null
      ? `→#${String(scope.toVersionId)}`
      : `#${String(scope.fromVersionId)}→#${String(scope.toVersionId)}`
  return `${scope.scopeKind}:${String(scope.scopeId)} ${range}`
}

export default function ImpactTab({ order }: ImpactTabProps) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['change-orders', 'impact', order.id, page],
    queryFn: () => fetchChangeImpact(order.id, page, PAGE_SIZE),
    placeholderData: keepPreviousData,
  })

  const summary = query.data?.summary

  const columns = useMemo<DataTableColumn<ChangeImpactTarget>[]>(
    () => [
      {
        header: t('delivery.changes.detail.impact.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changes.detail.impact.columns.online'),
        cell: (row) =>
          row.online ? (
            <Badge variant="ok" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t('delivery.changes.detail.impact.online')}
            </Badge>
          ) : (
            <Badge variant="off" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t('delivery.changes.detail.impact.offline')}
            </Badge>
          ),
      },
      { header: t('delivery.changes.detail.impact.columns.level'), cell: (row) => row.level },
      { header: t('delivery.changes.detail.impact.columns.add'), cell: (row) => row.addCount },
      { header: t('delivery.changes.detail.impact.columns.update'), cell: (row) => row.updateCount },
      { header: t('delivery.changes.detail.impact.columns.delete'), cell: (row) => row.deleteCount },
      { header: t('delivery.changes.detail.impact.columns.skip'), cell: (row) => row.skipCount },
      {
        header: t('delivery.changes.detail.impact.columns.configScopes'),
        cell: (row) =>
          row.configScopes.length === 0 ? (
            <span className="text-ink-3">—</span>
          ) : (
            <div className="grid gap-0.5">
              {row.configScopes.map((scope) => (
                <span key={configScopeText(scope)} className="tnum font-mono text-xs text-ink-2">
                  {configScopeText(scope)}
                </span>
              ))}
            </div>
          ),
      },
    ],
    [t],
  )

  const targetsTotal = query.data?.targets.total ?? 0

  return (
    <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
      <section className="grid gap-4">
        {/* 共享编排预览：范围 / 批次 / 生效方式 / 影响面汇总 */}
        {summary && <OrderOrchestration order={order} summary={summary} />}

        {/* 逐目标分页表 */}
        <div className="grid gap-2">
          <h4 className="text-[13px] font-semibold text-ink-2">
            {t('delivery.changes.detail.impact.targetsTitle')}
          </h4>
          <DataTable
            columns={columns}
            rows={query.data?.targets.items}
            rowKey={(row) => row.serverId}
            density="compact"
          />
          <Pager
            page={page}
            total={targetsTotal}
            pageSize={PAGE_SIZE}
            onPageChange={setPage}
          />
        </div>
      </section>
    </AsyncSection>
  )
}
