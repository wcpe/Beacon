// 变更单页（/changes，交付大域「发」）：变更单创建 / 影响预览 / 审批 / 灰度批次 / 生效观察。
// 顶部 namespace 作用域；列表视图与详情视图切换（选中某单进详情）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './changes/list-view'
import DetailView from './changes/detail-view'

export default function ChangesPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const [openId, setOpenId] = useState<number | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" title={t('delivery.changes.title')} />
        <NamespacePicker value={namespaceId} onChange={setNamespaceId} />
      </div>

      {openId === null ? (
        <ListView namespaceId={effectiveNamespaceId} onOpen={setOpenId} />
      ) : (
        <DetailView
          orderId={openId}
          onBack={() => {
            setOpenId(null)
          }}
        />
      )}
    </section>
  )
}
