// 配置中心页（/configs）：改作用域配置（编辑 / 校验 / 版本管理），下发走变更单。
// 顶部 namespace 作用域 +「下发走变更单」提示；主区为列表 + 非模态详情面板，回收站为右侧滑出抽屉。
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { Info } from 'lucide-react'

import { Button, Sheet, SheetContent } from '@beacon/ui'
import type { ConfigFileItem } from '@beacon/contracts'

import MasterDetail from '../features/shared/master-detail'
import NamespacePicker from '../features/delivery/namespace-picker'
import ListView from './configs/list-view'
import TrashView from './configs/trash-view'
import DetailView from './configs/detail-view'

export default function ConfigsPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  // 回收站抽屉开关（右侧滑出，不替换页面主体布局）
  const [trashOpen, setTrashOpen] = useState(false)
  // 选中的配置文件（打开右侧非模态详情面板）
  const [selected, setSelected] = useState<ConfigFileItem | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
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

      <MasterDetail
        master={
          <ListView
            namespaceId={effectiveNamespaceId}
            selectedId={selected?.id ?? null}
            onSelect={setSelected}
            onOpenTrash={() => {
              setTrashOpen(true)
            }}
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
            namespaceId={effectiveNamespaceId}
            onBack={() => {
              setTrashOpen(false)
            }}
          />
        </SheetContent>
      </Sheet>
    </section>
  )
}
