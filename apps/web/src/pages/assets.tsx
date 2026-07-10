// 文件资产页（/assets，只读）：看目录清单、哈希、内容预览与 diff。
// 顶部 namespace 作用域；四类视图独立切换（清单主从 / 扫描概要 / 跨服比对 / 两侧差异），
// 避免多块面板堆叠导致的超长页面。清单视图为主从布局：列表主列 + 右侧非模态详情面板。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { FolderTree } from 'lucide-react'

import { SectionHeader, Tabs, TabsContent, TabsList, TabsTrigger } from '@beacon/ui'

import NamespacePicker from '../features/delivery/namespace-picker'
import ScanPanel from './assets/scan-panel'
import ManifestPanel from './assets/manifest-panel'
import ComparePanel from './assets/compare-panel'
import DiffPanel from './assets/diff-panel'

export default function AssetsPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const [view, setView] = useState('manifest')
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader
          size="lg"
          icon={<FolderTree className="size-5" aria-hidden />}
          title={t('delivery.assets.title')}
        />
        <NamespacePicker value={namespaceId} onChange={setNamespaceId} />
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
