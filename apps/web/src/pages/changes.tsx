// 变更单页（/changes，交付大域「发」）：变更单创建 / 影响预览 / 审批 / 灰度批次 / 生效观察。
// 顶部 namespace 作用域 +「交付流程」帮助卡入口；列表视图与详情视图切换（选中某单进详情）。
// 消费 ?order=<id> 深链（历史页「在变更单中打开」）：加载后自动选中该单，仅初始消费一次。
import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { CircleHelp } from 'lucide-react'

import { Button, PageHeader } from '@beacon/ui'
import type { ChangeOrderSummary } from '@beacon/contracts'

import { ApiClientError } from '../api/delivery'
import { fetchChangeOrder } from '../api/delivery-changes'
import MasterDetail from '../features/shared/master-detail'
import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './changes/list-view'
import DetailView from './changes/detail-view'
import FlowHelp from './changes/flow-help'

// 解析 ?order= 深链参数为合法单号（非法 / 缺省 → null）
function parseOrderParam(raw: string | null): number | null {
  if (raw === null) {
    return null
  }
  const id = Number.parseInt(raw, 10)
  return Number.isInteger(id) && id > 0 ? id : null
}

export default function ChangesPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  // 选中的变更单（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ChangeOrderSummary | null>(null)
  // 「交付流程」帮助卡开合（非模态说明卡）
  const [helpOpen, setHelpOpen] = useState(false)
  const effectiveNamespaceId = namespaceId ?? 0

  // ?order= 深链：取该单详情后自动选中；仅消费一次（用户关闭面板后不再重新打开）
  const [searchParams] = useSearchParams()
  const deepLinkId = parseOrderParam(searchParams.get('order'))
  const deepLinkConsumedRef = useRef(false)
  const deepLinkQuery = useQuery({
    queryKey: ['change-orders', 'deep-link', deepLinkId],
    queryFn: () => fetchChangeOrder(deepLinkId ?? 0),
    enabled: deepLinkId !== null && !deepLinkConsumedRef.current,
  })
  const deepLinkOrder = deepLinkQuery.data
  useEffect(() => {
    if (deepLinkOrder !== undefined && !deepLinkConsumedRef.current) {
      deepLinkConsumedRef.current = true
      setSelected(deepLinkOrder)
    }
  }, [deepLinkOrder])

  return (
    <section className="grid gap-4">
      <PageHeader
        title={t('delivery.changes.title')}
        actions={
          <>
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
            <NamespacePicker
              value={namespaceId}
              onChange={(id) => {
                setNamespaceId(id)
                setSelected(null)
              }}
            />
          </>
        }
      />

      {/* 深链目标加载失败：脱敏真因内联展示，不静默吞掉（列表仍可正常使用） */}
      {deepLinkQuery.isError && (
        <p className="text-sm text-destructive">
          {t('delivery.changes.deepLinkError', {
            id: deepLinkId,
            message:
              deepLinkQuery.error instanceof ApiClientError || deepLinkQuery.error instanceof Error
                ? deepLinkQuery.error.message
                : String(deepLinkQuery.error),
          })}
        </p>
      )}

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
