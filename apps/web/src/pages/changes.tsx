// 变更单页（/changes，交付大域「发」）：变更单创建 / 影响预览 / 审批 / 灰度批次 / 生效观察。
// 顶部 namespace 作用域 +「交付流程」帮助卡入口；列表视图与详情视图切换（选中某单进详情）。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { CircleHelp } from 'lucide-react'

import { Button, SectionHeader } from '@beacon/ui'
import type { ChangeOrderSummary } from '@beacon/contracts'

import MasterDetail from '../features/shared/master-detail'
import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './changes/list-view'
import DetailView from './changes/detail-view'
import FlowHelp from './changes/flow-help'

export default function ChangesPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  // 选中的变更单（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ChangeOrderSummary | null>(null)
  // 「交付流程」帮助卡开合（非模态说明卡）
  const [helpOpen, setHelpOpen] = useState(false)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <SectionHeader size="lg" title={t('delivery.changes.title')} />
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              setHelpOpen((prev) => !prev)
            }}
          >
            <CircleHelp className="size-4" />
            {t('delivery.changes.flow.open')}
          </Button>
        </div>
        <NamespacePicker
          value={namespaceId}
          onChange={(id) => {
            setNamespaceId(id)
            setSelected(null)
          }}
        />
      </div>

      {helpOpen && (
        <FlowHelp
          onClose={() => {
            setHelpOpen(false)
          }}
        />
      )}

      <MasterDetail
        master={
          <ListView
            namespaceId={effectiveNamespaceId}
            selectedId={selected?.id ?? null}
            onOpen={setSelected}
          />
        }
        detail={
          selected ? (
            <DetailView
              orderId={selected.id}
              onBack={() => {
                setSelected(null)
              }}
            />
          ) : null
        }
        detailTitle={selected?.title ?? ''}
        closeLabel={t('delivery.changes.detail.backToList')}
        onClose={() => {
          setSelected(null)
        }}
      />
    </section>
  )
}
