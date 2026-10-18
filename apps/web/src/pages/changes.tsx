// 变更单页（/changes，交付大域「发」）：变更单创建 / 影响预览 / 审批 / 灰度批次 / 生效观察。
// 页头统一为「图标 + 大标题 + 一行解释 + 右侧常用操作」（PageHeader）：引导创建 / 高级创建、
// 「交付流程」帮助与文档入口；列表视图与详情视图切换（选中某单进详情）。
// 写目标命名空间取自页眉观测范围选择器，页内不再有自己的命名空间选择器（全站统一一套）。
// 消费 ?order=<id> 深链（历史页「在变更单中打开」）：加载后自动选中该单，仅初始消费一次。
import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { CircleHelp, GitPullRequestArrow, Plus, Sparkles } from 'lucide-react'

import { Button, PageHeader, TableSkeleton } from '@beacon/ui'
import type { ChangeOrderSummary } from '@beacon/contracts'

import { ApiClientError } from '../api/delivery'
import { fetchChangeOrder } from '../api/delivery-changes'
import MasterDetail from '../features/shared/master-detail'
import DocHelp from '../features/delivery/doc-help'
import { useDefaultObservationNamespaceState } from '../features/env/use-default-namespace'
import ListView from './changes/list-view'
import DetailView from './changes/detail-view'
import FlowHelp from './changes/flow-help'
import type { WizardContent } from './changes/wizard-state'

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
  // 选中的变更单（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ChangeOrderSummary | null>(null)
  // 新建弹窗开合由页面持有（页头两个创建按钮驱动，弹窗本体在列表视图内）
  const [createOpen, setCreateOpen] = useState(false)
  // 引导向导开合与预选交付内容（空态任务卡带类型进入，页头按钮固定从文件任务起步）
  const [wizardOpen, setWizardOpen] = useState(false)
  const [wizardPreset, setWizardPreset] = useState<WizardContent>('files')
  // 「交付流程」帮助卡开合（非模态说明卡）
  const [helpOpen, setHelpOpen] = useState(false)

  // 命名空间范围唯一真源是页眉的观测范围选择器（页内不再有自己的选择器）。
  // 进页面时若页眉还停在「全部命名空间」，由 useDefaultObservationNamespaceState 自动补第一个
  // 可用命名空间并写回页眉范围；用户已选定的范围不会被覆盖。
  // 本页含建单 / 提审 / 放行 / 回滚等写操作，写目标必须明确：namespaceId 仍为 0（解析中或
  // 确实没有可用命名空间）时禁写并给出引导。
  const { namespaceId, pending: scopePending } = useDefaultObservationNamespaceState()
  const scopeReady = namespaceId > 0

  // 页眉命名空间变化时清空右侧选中：避免详情停在上一个命名空间的单据上。
  // 首次解析（0 → 具体命名空间）属于「初始范围」而非用户切换，故跳过（深链 ?order= 也在此后才落地）。
  const prevNamespaceIdRef = useRef(0)
  useEffect(() => {
    const prev = prevNamespaceIdRef.current
    prevNamespaceIdRef.current = namespaceId
    if (prev === 0 || prev === namespaceId) {
      return
    }
    setSelected(null)
  }, [namespaceId])

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
    <section className="grid gap-4 md:flex md:h-full md:min-h-0 md:flex-col">
      {/* 页头：左=图标 + 大标题 + 一句话说明本页做什么；右=创建入口 + 流程与文档 */}
      <PageHeader
        icon={<GitPullRequestArrow />}
        title={t('nav.changes')}
        description={t('delivery.changes.pageDesc')}
        actions={
          <>
            {/* 新手主入口：五步向导；高级创建保留原有直建表单。
                未选具体命名空间时两个写入口都禁用：建单目标不明确，不得对「全部命名空间」执行 */}
            <Button
              size="sm"
              disabled={!scopeReady}
              onClick={() => {
                setWizardPreset('files')
                setWizardOpen(true)
              }}
            >
              <Sparkles className="size-3.5" />
              {t('delivery.changes.list.createGuided')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              disabled={!scopeReady}
              onClick={() => {
                setCreateOpen(true)
              }}
            >
              <Plus className="size-3.5" />
              {t('delivery.changes.list.createAdvanced')}
            </Button>
            {/* 五步流程说明仍单独保留（与文档站互补：这里是速览，文档站是细节） */}
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
            {/* 页头文档入口：本页功能解释由 description 承担，这里只负责跳使用者文档 */}
            <DocHelp topic="changes" />
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

      <FlowHelp open={helpOpen} onOpenChange={setHelpOpen} />

      {scopeReady ? (
        <MasterDetail
          fill
          master={
            <ListView
              namespaceId={namespaceId}
              selectedId={selected?.id ?? null}
              onOpen={setSelected}
              createOpen={createOpen}
              onCreateOpenChange={setCreateOpen}
              wizardOpen={wizardOpen}
              onWizardOpenChange={setWizardOpen}
              wizardPreset={wizardPreset}
              onWizardPresetChange={setWizardPreset}
            />
          }
          detail={
            selected ? (
              <DetailView orderId={selected.id} />
            ) : null
          }
          detailTitle={selected?.title ?? ''}
          closeLabel={t('delivery.changes.detail.backToList')}
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
        // 确实没有可用命名空间（或页眉范围失效）：不渲染列表与详情（列表按命名空间取数、详情内含
        // 提审 / 放行 / 回滚等写操作），也不伪造「全部命名空间」的数据，直接把用户指回页眉选择器
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
