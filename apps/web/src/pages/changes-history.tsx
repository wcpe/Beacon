// 交付历史页（/changes/history，交付大域「溯」）：任务 / 批次 / 单服状态与整单回滚。
// 顶部 namespace 作用域；列表视图与详情视图切换（选中某单进详情，可整单回滚 / 结束回滚）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'
import type { ChangeOrderSummary } from '@beacon/devmock'

import MasterDetail from '../features/shared/master-detail'
import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './changes-history/list-view'
import DetailView from './changes-history/detail-view'

export default function ChangesHistoryPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  // 选中的历史变更单（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ChangeOrderSummary | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" title={t('delivery.changesHistory.title')} />
        <NamespacePicker
          value={namespaceId}
          onChange={(id) => {
            setNamespaceId(id)
            setSelected(null)
          }}
        />
      </div>

      <MasterDetail
        master={
          <ListView
            namespaceId={effectiveNamespaceId}
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
    </section>
  )
}
