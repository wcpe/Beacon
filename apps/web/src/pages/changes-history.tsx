// 交付历史页（/changes/history，交付大域「溯」）：任务 / 批次 / 单服状态与整单回滚。
// 顶部 namespace 作用域；列表视图与详情视图切换（选中某单进详情，可整单回滚 / 结束回滚）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './changes-history/list-view'
import DetailView from './changes-history/detail-view'

export default function ChangesHistoryPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const [activeOrderId, setActiveOrderId] = useState<number | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" title={t('delivery.changesHistory.title')} />
        <NamespacePicker value={namespaceId} onChange={setNamespaceId} />
      </div>

      {activeOrderId === null ? (
        <ListView namespaceId={effectiveNamespaceId} onView={setActiveOrderId} />
      ) : (
        <DetailView
          orderId={activeOrderId}
          onBack={() => {
            setActiveOrderId(null)
          }}
        />
      )}
    </section>
  )
}
