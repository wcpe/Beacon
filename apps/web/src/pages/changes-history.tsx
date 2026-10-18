// 交付历史页（/changes/history，交付大域「溯」）：任务 / 批次 / 单服状态与整单回滚。
// 页头统一为「图标 + 大标题 + 一行解释 + 右侧常用操作」（PageHeader）：文档入口
// （本页只看不改，没有自己的业务操作按钮）；列表视图与详情视图切换（选中某单进详情，可整单回滚 / 结束回滚）。
// 作用域命名空间取自页眉观测范围选择器，页内不再有自己的命名空间选择器（全站统一一套）。
import { useEffect, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { History } from 'lucide-react'

import { PageHeader, TableSkeleton } from '@beacon/ui'
import type { ChangeOrderSummary } from '@beacon/contracts'

import MasterDetail from '../features/shared/master-detail'
import DocHelp from '../features/delivery/doc-help'
import { useDefaultObservationNamespaceState } from '../features/env/use-default-namespace'
import ListView from './changes-history/list-view'
import DetailView from './changes-history/detail-view'

export default function ChangesHistoryPage() {
  const { t } = useTranslation()
  // 选中的历史变更单（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ChangeOrderSummary | null>(null)

  // 命名空间范围唯一真源是页眉的观测范围选择器（页内不再有自己的选择器）。
  // 进页面时若页眉还停在「全部命名空间」，由 useDefaultObservationNamespaceState 自动补第一个
  // 可用命名空间并写回页眉范围；用户已选定的范围不会被覆盖。
  // 本页详情含整单回滚 / 结束回滚等写操作，目标必须明确：namespaceId 仍为 0（解析中或确实没有
  // 可用命名空间）时不给列表与详情入口，先引导去页眉选择。
  const { namespaceId, pending: scopePending } = useDefaultObservationNamespaceState()
  const scopeReady = namespaceId > 0

  // 页眉命名空间变化时清空右侧选中：避免详情停在上一个命名空间的单据上。
  // 首次解析（0 → 具体命名空间）属于「初始范围」而非用户切换，故跳过。
  const prevNamespaceIdRef = useRef(0)
  useEffect(() => {
    const prev = prevNamespaceIdRef.current
    prevNamespaceIdRef.current = namespaceId
    if (prev === 0 || prev === namespaceId) {
      return
    }
    setSelected(null)
  }, [namespaceId])

  return (
    <section className="grid gap-4 md:flex md:h-full md:min-h-0 md:flex-col">
      {/* 页头：左=图标 + 大标题 + 一句话说明本页做什么；右=文档入口 */}
      <PageHeader
        icon={<History />}
        title={t('nav.changesHistory')}
        description={t('delivery.changesHistory.pageDesc')}
        actions={
          <>
            {/* 页头文档入口：本页功能解释由 description 承担，这里只负责跳使用者文档 */}
            <DocHelp topic="history" />
          </>
        }
      />

      {scopeReady ? (
        <MasterDetail
          fill
          master={
            <ListView
              namespaceId={namespaceId}
              selectedId={selected?.id ?? null}
              onView={setSelected}
            />
          }
          detail={selected ? <DetailView orderId={selected.id} /> : null}
          detailTitle={selected?.title ?? ''}
          closeLabel={t('delivery.changesHistory.detail.backToList')}
          onClose={() => {
            setSelected(null)
          }}
        />
      ) : scopePending ? (
        // 范围解析中（env 选项加载 / 默认值回流）：给骨架，不要闪出「请先选命名空间」的引导
        <div className="md:min-h-0 md:flex-1 md:overflow-y-auto" data-slot="delivery-scope-pending">
          <TableSkeleton columns={5} rows={6} />
        </div>
      ) : (
        // 确实没有可用命名空间（或页眉范围失效）：不渲染列表与详情（详情内含整单回滚等写操作），
        // 也不伪造「全部命名空间」的数据，直接把用户指回页眉选择器
        <div
          data-slot="delivery-scope-required"
          className="grid gap-1 rounded-lg border border-dashed border-border bg-surface-2/40 px-4 py-6"
        >
          <p className="text-sm font-medium text-ink-1">{t('delivery.scope.requiredTitle')}</p>
          <p className="max-w-2xl text-sm text-ink-3">{t('delivery.scope.required')}</p>
        </div>
      )}
    </section>
  )
}
