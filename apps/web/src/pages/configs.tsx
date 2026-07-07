// 配置中心页（/configs）：改作用域配置（编辑 / 校验 / 版本管理），下发走变更单。
// 顶部 namespace 作用域 +「下发走变更单」提示，三视图切换：列表 / 回收站 / 详情。
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'

import { Button, SectionHeader } from '@beacon/ui'

import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './configs/list-view'
import TrashView from './configs/trash-view'
import DetailView from './configs/detail-view'

// 当前视图：列表 / 回收站 / 某文件详情
type View = { kind: 'list' } | { kind: 'trash' } | { kind: 'detail'; fileId: number }

export default function ConfigsPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const [view, setView] = useState<View>({ kind: 'list' })
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" title={t('delivery.configs.title')} />
        <NamespacePicker
          value={namespaceId}
          onChange={(id) => {
            setNamespaceId(id)
            // 切换 namespace 回到列表，避免残留其他 ns 的详情
            setView({ kind: 'list' })
          }}
        />
      </div>

      {/* 下发走变更单的显眼提示 + 跳转入口 */}
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-amber-500/40 bg-amber-500/10 px-4 py-2 text-sm">
        <span className="text-muted-foreground">{t('delivery.configs.deliveryHint')}</span>
        <Button asChild variant="outline" size="sm">
          <Link to="/changes">{t('delivery.configs.goChanges')}</Link>
        </Button>
      </div>

      {view.kind === 'list' && (
        <ListView
          namespaceId={effectiveNamespaceId}
          onOpenDetail={(fileId) => {
            setView({ kind: 'detail', fileId })
          }}
          onOpenTrash={() => {
            setView({ kind: 'trash' })
          }}
        />
      )}

      {view.kind === 'trash' && (
        <TrashView
          namespaceId={effectiveNamespaceId}
          onBack={() => {
            setView({ kind: 'list' })
          }}
        />
      )}

      {view.kind === 'detail' && (
        <DetailView
          fileId={view.fileId}
          onBack={() => {
            setView({ kind: 'list' })
          }}
        />
      )}
    </section>
  )
}
