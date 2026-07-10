// 交付历史详情：复用共享控件达成全程追溯——「当时改了什么」（变更内容预览）、
// 「发给谁 / 怎么编排」（编排预览）、「怎么推进的」（批次状态机只读回放 + 单服状态）、
// 「出过什么事」（进度时间线双模式）；整单回滚 / 结束回滚走共享 order-rollback。
import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router-dom'

import { Layers, Server } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  SectionHeader,
  Tabs,
  TabsContent,
  TabsList,
  TabsTrigger,
  type DataTableColumn,
} from '@beacon/ui'
import type { ChangeTarget } from '@beacon/devmock'

import Pager from '../../features/delivery/pager'
import {
  fetchChangeEvents,
  fetchChangeImpact,
  fetchChangeOrder,
  fetchChangeTargets,
  type ChangeOrderDetail,
} from '../../api/delivery-changes'
import BatchFlow from '../../features/delivery/batch-flow'
import EventsTimeline from '../../features/delivery/events-timeline'
import OrderChangePreview from '../../features/delivery/order-change-preview'
import OrderOrchestration from '../../features/delivery/order-orchestration'
import { OrderRollbackActions, RollbackBanner } from '../../features/delivery/order-rollback'
import { TargetStatusBadge } from '../../features/delivery/status-badges'
import StatusBadge from './status-badge'

const TARGET_PAGE_SIZE = 20

interface DetailViewProps {
  orderId: number
}

export default function DetailView({ orderId }: DetailViewProps) {
  const { t } = useTranslation()

  const detailQuery = useQuery({
    queryKey: ['change-orders', 'detail', orderId],
    queryFn: () => fetchChangeOrder(orderId),
  })

  const detail = detailQuery.data

  return (
    <div className="grid gap-4">
      {/* 状态 + 回滚 / 结束操作（面板标题已由 MasterDetail 头部承担） */}
      <div className="flex flex-wrap items-center justify-between gap-3 rounded-xl border border-border bg-surface-2 px-3 py-2.5">
        <div className="flex flex-wrap items-center gap-3">
          {detail && <StatusBadge status={detail.status} />}
        </div>
        <div className="flex items-center gap-2">
          {detail && <OrderRollbackActions order={detail} />}
          {detail && (
            <Button variant="ghost" size="sm" asChild>
              <Link to={`/changes?order=${String(detail.id)}`}>
                {t('delivery.changesHistory.list.openInChanges')}
              </Link>
            </Button>
          )}
        </div>
      </div>

      {/* 回滚信息横幅 + 回滚中逐目标进度 */}
      {detail && <RollbackBanner order={detail} />}

      <AsyncSection isLoading={detailQuery.isLoading} isError={detailQuery.isError} error={detailQuery.error}>
        {detail && (
          <Tabs defaultValue="replay">
            <TabsList>
              <TabsTrigger value="replay">{t('delivery.changesHistory.detail.tabs.replay')}</TabsTrigger>
              <TabsTrigger value="content">{t('delivery.changesHistory.detail.tabs.content')}</TabsTrigger>
              <TabsTrigger value="orchestration">
                {t('delivery.changesHistory.detail.tabs.orchestration')}
              </TabsTrigger>
              <TabsTrigger value="timeline">{t('delivery.changesHistory.detail.tabs.timeline')}</TabsTrigger>
            </TabsList>
            <TabsContent value="replay" className="pt-3">
              <ReplayTab detail={detail} />
            </TabsContent>
            <TabsContent value="content" className="pt-3">
              {/* 当时改了什么：共享变更内容预览（含配置版本反查与行级 diff） */}
              <OrderChangePreview items={detail.items} orderId={detail.id} />
            </TabsContent>
            <TabsContent value="orchestration" className="pt-3">
              <OrchestrationTab detail={detail} />
            </TabsContent>
            <TabsContent value="timeline" className="pt-3">
              <TimelineTab orderId={detail.id} />
            </TabsContent>
          </Tabs>
        )}
      </AsyncSection>
    </div>
  )
}

// 执行回放：批次状态机（只读）+ 单服状态分页表
function ReplayTab({ detail }: { detail: ChangeOrderDetail }) {
  const { t } = useTranslation()
  const [page, setPage] = useState(1)

  const targetsQuery = useQuery({
    queryKey: ['change-orders', 'targets', detail.id, page],
    queryFn: () => fetchChangeTargets(detail.id, { page, pageSize: TARGET_PAGE_SIZE }),
    placeholderData: keepPreviousData,
  })
  const targetsTotal = targetsQuery.data?.total ?? 0

  const targetColumns = useMemo<DataTableColumn<ChangeTarget>[]>(
    () => [
      {
        header: t('delivery.changesHistory.detail.columns.serverId'),
        cell: (row) => <span className="font-mono">{row.serverId}</span>,
      },
      {
        header: t('delivery.changesHistory.detail.columns.batch'),
        cell: (row) => <span className="tnum">#{String(row.batchNo)}</span>,
      },
      {
        header: t('delivery.changesHistory.detail.columns.status'),
        cell: (row) => <TargetStatusBadge status={row.status} />,
      },
      {
        header: t('delivery.changesHistory.detail.columns.rollback'),
        cell: (row) =>
          row.rollbackStatus === null ? (
            <span className="text-ink-4">
              {t('delivery.changesHistory.detail.rollbackStatus.none')}
            </span>
          ) : (
            <Badge
              variant={row.rollbackStatus === 'failed' ? 'crit' : 'ok'}
              className="gap-1.5"
            >
              <span className="size-1.5 rounded-full bg-current" aria-hidden />
              {t(`delivery.changesHistory.detail.rollbackStatus.${row.rollbackStatus}`)}
            </Badge>
          ),
      },
    ],
    [t],
  )

  return (
    <div className="grid gap-4">
      {/* 批次状态机只读回放 */}
      <div className="grid gap-2">
        <SectionHeader
          icon={<Layers className="size-4" />}
          title={t('delivery.changesHistory.detail.batchesTitle')}
        />
        <BatchFlow batches={detail.batches} orderStatus={detail.status} readOnly />
      </div>

      {/* 单服状态 */}
      <div className="grid gap-2">
        <SectionHeader
          icon={<Server className="size-4" />}
          title={t('delivery.changesHistory.detail.targetsTitle')}
        />
        <AsyncSection isLoading={targetsQuery.isLoading} isError={targetsQuery.isError} error={targetsQuery.error}>
          <DataTable
            columns={targetColumns}
            rows={targetsQuery.data?.items}
            rowKey={(row) => `${row.serverId}:${String(row.batchNo)}`}
            emptyText={t('delivery.changes.detail.batches.empty')}
            density="compact"
          />
        </AsyncSection>
        <Pager page={page} total={targetsTotal} pageSize={TARGET_PAGE_SIZE} onPageChange={setPage} />
      </div>
    </div>
  )
}

// 交付编排：影响面汇总（固化目标集时反映实际执行范围）+ 共享编排预览
function OrchestrationTab({ detail }: { detail: ChangeOrderDetail }) {
  const impactQuery = useQuery({
    // pageSize 1：本 Tab 只用 summary，逐目标见「执行回放」
    queryKey: ['change-orders', 'impact', detail.id, 1, 1],
    queryFn: () => fetchChangeImpact(detail.id, 1, 1),
  })
  return (
    <AsyncSection isLoading={impactQuery.isLoading} isError={impactQuery.isError} error={impactQuery.error}>
      {impactQuery.data && <OrderOrchestration order={detail} summary={impactQuery.data.summary} />}
    </AsyncSection>
  )
}

// 进度时间线：共享双模式时间线（历史单为全程回放，不必轮询）
function TimelineTab({ orderId }: { orderId: number }) {
  const query = useQuery({
    queryKey: ['change-orders', 'events', orderId],
    queryFn: () => fetchChangeEvents(orderId),
  })
  return (
    <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
      <EventsTimeline events={query.data?.events ?? []} />
    </AsyncSection>
  )
}
