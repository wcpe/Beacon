// 配置中心页（/configs）：改作用域配置（编辑 / 校验 / 版本管理），下发走变更单。
// 顶部 namespace 作用域 +「下发走变更单」提示，三视图切换：列表 / 回收站 / 详情。
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { FileCog, Info } from 'lucide-react'

import { Button, PageHeader } from '@beacon/ui'
import type { ConfigFileItem } from '@beacon/contracts'

import MasterDetail from '../features/shared/master-detail'
import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './configs/list-view'
import TrashView from './configs/trash-view'
import DetailView from './configs/detail-view'

export default function ConfigsPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  // 是否在回收站视图（回收站为整块视图切换，非详情面板）
  const [trashOpen, setTrashOpen] = useState(false)
  // 选中的配置文件（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ConfigFileItem | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
      <PageHeader
        icon={<FileCog className="size-4" />}
        title={t('delivery.configs.title')}
        actions={
          <NamespacePicker
            value={namespaceId}
            onChange={(id) => {
              setNamespaceId(id)
              // 切换 namespace 复位视图与选中，避免残留其他 ns 的详情
              setTrashOpen(false)
              setSelected(null)
            }}
          />
        }
      />

      {/* 下发走变更单的显眼提示 + 跳转入口（B 版 warn 提示条） */}
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-warn-bd bg-warn-bg px-4 py-2.5 text-sm text-warn">
        <span className="flex items-center gap-2">
          <Info className="size-4 shrink-0" aria-hidden />
          {t('delivery.configs.deliveryHint')}
        </span>
        <Button asChild variant="outline" size="sm">
          <Link to="/changes">{t('delivery.configs.goChanges')}</Link>
        </Button>
      </div>

      {trashOpen ? (
        <TrashView
          namespaceId={effectiveNamespaceId}
          onBack={() => {
            setTrashOpen(false)
          }}
        />
      ) : (
        <MasterDetail
          master={
            <ListView
              namespaceId={effectiveNamespaceId}
              selectedId={selected?.id ?? null}
              onSelect={setSelected}
              onOpenTrash={() => {
                setTrashOpen(true)
              }}
            />
          }
          detail={selected ? <DetailView fileId={selected.id} /> : null}
          detailTitle={selected ? <span className="font-mono">{selected.name}</span> : ''}
          closeLabel={t('delivery.configs.detail.backToList')}
          onClose={() => {
            setSelected(null)
          }}
        />
      )}
    </section>
  )
}
