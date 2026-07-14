// 文件资产页（/assets，只读）：看目录清单、哈希、内容预览与 diff。
// 顶部 namespace 作用域 +「差异经变更单交付」去向提示；四类视图独立切换（清单主从 /
// 扫描概要 / 跨服比对 / 两侧差异），避免多块面板堆叠导致的超长页面。
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { FolderTree, Info, ShieldAlert } from 'lucide-react'

import { Button, SectionHeader, Tabs, TabsContent, TabsList, TabsTrigger } from '@beacon/ui'

import NamespacePicker from '../features/delivery/namespace-picker'
import ScanPanel from './assets/scan-panel'
import ManifestPanel from './assets/manifest-panel'
import ComparePanel from './assets/compare-panel'
import DiffPanel from './assets/diff-panel'
import SensitiveRulesDialog from './assets/sensitive-rules-dialog'

export default function AssetsPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const [view, setView] = useState('manifest')
  const [rulesOpen, setRulesOpen] = useState(false)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader
          size="lg"
          icon={<FolderTree className="size-5" aria-hidden />}
          title={t('delivery.assets.title')}
        />
        <div className="flex items-center gap-2">
          {/* 敏感路径规则编辑（FR-164，非结构性小面板）：命中 glob 的文件预览 / diff 需原因放行 */}
          <Button
            variant="outline"
            size="sm"
            onClick={() => {
              setRulesOpen(true)
            }}
          >
            <ShieldAlert className="size-4" aria-hidden />
            {t('delivery.assets.sensitiveRules.manage')}
          </Button>
          <NamespacePicker value={namespaceId} onChange={setNamespaceId} />
        </div>
      </div>

      <SensitiveRulesDialog open={rulesOpen} onOpenChange={setRulesOpen} />

      {/* 去向提示：文件差异的下发统一走变更单（与 /configs 提示同款式） */}
      <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-warn-bd bg-warn-bg px-4 py-2.5 text-sm text-warn">
        <span className="flex items-center gap-2">
          <Info className="size-4 shrink-0" aria-hidden />
          {t('delivery.assets.deliveryHint')}
        </span>
        <Button asChild variant="outline" size="sm">
          <Link to="/changes">{t('delivery.assets.goChanges')}</Link>
        </Button>
      </div>

      <Tabs value={view} onValueChange={setView}>
        <TabsList>
          <TabsTrigger value="manifest">{t('delivery.assets.list.title')}</TabsTrigger>
          <TabsTrigger value="scan">{t('delivery.assets.scan.title')}</TabsTrigger>
          <TabsTrigger value="compare">{t('delivery.assets.compare.title')}</TabsTrigger>
          <TabsTrigger value="diff">{t('delivery.assets.diff.title')}</TabsTrigger>
        </TabsList>
        <TabsContent value="manifest" className="pt-4">
          <ManifestPanel namespaceId={effectiveNamespaceId} />
        </TabsContent>
        <TabsContent value="scan" className="pt-4">
          <ScanPanel namespaceId={effectiveNamespaceId} />
        </TabsContent>
        <TabsContent value="compare" className="pt-4">
          <ComparePanel namespaceId={effectiveNamespaceId} />
        </TabsContent>
        <TabsContent value="diff" className="pt-4">
          <DiffPanel />
        </TabsContent>
      </Tabs>
    </section>
  )
}
