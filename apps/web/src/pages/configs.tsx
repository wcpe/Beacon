// 配置中心页（/configs）：改作用域配置（编辑 / 校验 / 版本管理），下发走变更单。
// 页头统一为「图标 + 大标题 + 一行解释 + 右侧常用操作」（PageHeader）：新建、回收站、
// 前往变更单与文档入口；主区为列表 + 非模态详情面板，回收站为右侧滑出抽屉。
// 写目标命名空间取自页眉观测范围选择器，页内不再有自己的命名空间选择器（全站统一一套）。
import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { FileSliders, Trash2 } from 'lucide-react'

import { Button, PageHeader, Sheet, SheetContent, TableSkeleton } from '@beacon/ui'
import type { ConfigFileItem } from '@beacon/contracts'

import MasterDetail from '../features/shared/master-detail'
import DocHelp from '../features/delivery/doc-help'
import { useObservationNamespaceState } from '../features/env/use-default-namespace'
import DeliveryScopeError from '../features/delivery/scope-error'
import ListView from './configs/list-view'
import TrashView from './configs/trash-view'
import DetailView from './configs/detail-view'

export default function ConfigsPage() {
  const { t } = useTranslation()
  // 回收站抽屉开关（右侧滑出，不替换页面主体布局）
  const [trashOpen, setTrashOpen] = useState(false)
  // 新建配置文件弹窗开关：按钮在页头，弹窗本体在列表视图内（与空态引导共用一个开关）
  const [createOpen, setCreateOpen] = useState(false)
  // 选中的配置文件（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ConfigFileItem | null>(null)

  // 命名空间范围唯一真源是页眉观测范围选择器；未选具体命名空间时保留全局范围并禁用写操作。
  // 本页含创建 / 删除 / 恢复 / 彻底删除等写操作，写目标必须明确。
  const {
    namespaceId,
    pending: scopePending,
    error: scopeError,
    retry: retryScopeOptions,
  } = useObservationNamespaceState()
  const scopeReady = namespaceId > 0

  // 页眉命名空间变化时清空右侧选中与回收站抽屉：避免残留上一个命名空间的详情。
  // 首次解析（0 → 具体命名空间）属于「初始范围」而非用户切换，故跳过。
  const prevNamespaceIdRef = useRef(0)
  useEffect(() => {
    const prev = prevNamespaceIdRef.current
    prevNamespaceIdRef.current = namespaceId
    if (prev === 0 || prev === namespaceId) {
      return
    }
    setSelected(null)
    setTrashOpen(false)
  }, [namespaceId])

  return (
    <section className="grid gap-4 md:flex md:h-full md:min-h-0 md:flex-col">
      {/* 页头：左=图标 + 大标题 + 一句话说明本页做什么；右=常用操作 + 文档入口 */}
      <PageHeader
        icon={<FileSliders />}
        title={t('nav.configs')}
        description={t('delivery.configs.pageDesc')}
        actions={
          <>
            {/* 未选具体命名空间时禁写：写目标不明确，不得对「全部命名空间」执行（原因见内容区引导） */}
            <Button
              size="sm"
              disabled={!scopeReady}
              title={scopeReady || scopeError !== null ? undefined : t('delivery.scope.requiredTitle')}
              onClick={() => {
                setCreateOpen(true)
              }}
            >
              {t('delivery.configs.list.create')}
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={!scopeReady}
              title={scopeReady || scopeError !== null ? undefined : t('delivery.scope.requiredTitle')}
              onClick={() => {
                setTrashOpen(true)
              }}
            >
              <Trash2 className="size-3.5" aria-hidden />
              {t('delivery.configs.list.trash')}
            </Button>
            {/* 改完只是存版本：真要下发得走变更单，去向入口留在页头 */}
            <Button asChild variant="outline" size="sm">
              <Link to="/changes">{t('delivery.configs.goChanges')}</Link>
            </Button>
            {/* 页头文档入口：本页功能解释由 description 承担，这里只负责跳使用者文档 */}
            <DocHelp topic="configs" />
          </>
        }
      />

      {scopeError !== null ? (
        <DeliveryScopeError error={scopeError} onRetry={retryScopeOptions} />
      ) : scopeReady ? (
        <>
          <MasterDetail
            fill
            master={
              <ListView
                namespaceId={namespaceId}
                selectedId={selected?.id ?? null}
                onSelect={setSelected}
                createOpen={createOpen}
                onCreateOpenChange={setCreateOpen}
              />
            }
            detail={selected ? <DetailView fileId={selected.id} /> : null}
            detailTitle={selected ? <span className="font-mono">{selected.name}</span> : ''}
            closeLabel={t('delivery.configs.detail.backToList')}
            onClose={() => {
              setSelected(null)
            }}
          />

          {/* 回收站：右侧滑出抽屉处理，不替换页面主体（避免整页布局被切换掉） */}
          <Sheet open={trashOpen} onOpenChange={setTrashOpen}>
            <SheetContent className="w-full gap-0 overflow-y-auto p-4 sm:max-w-3xl">
              <TrashView
                namespaceId={namespaceId}
                onBack={() => {
                  setTrashOpen(false)
                }}
              />
            </SheetContent>
          </Sheet>
        </>
      ) : scopePending ? (
        // 环境选项加载中：给骨架，不要提前闪出「请先选命名空间」的引导
        <div className="md:min-h-0 md:flex-1 md:overflow-y-auto" data-slot="delivery-scope-pending">
          <TableSkeleton columns={4} rows={6} />
        </div>
      ) : (
        // 尚未选具体命名空间：不渲染列表与详情（列表按命名空间取数、详情内含写操作），
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
