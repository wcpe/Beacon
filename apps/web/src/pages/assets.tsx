// 文件资产页（/assets，只读）：看目录清单、哈希、内容预览与 diff。
// 顶部 namespace 作用域 + 扫描概要 + 文件清单（预览 / 触发重扫）+ 跨服比对 + 两侧 diff。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import NamespacePicker from '../features/delivery/namespace-picker'
import ScanPanel from './assets/scan-panel'
import ManifestPanel from './assets/manifest-panel'
import ComparePanel from './assets/compare-panel'
import DiffPanel from './assets/diff-panel'

export default function AssetsPage() {
  const { t } = useTranslation()
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const effectiveNamespaceId = namespaceId ?? 0

  return (
    <section className="grid gap-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <SectionHeader size="lg" title={t('delivery.assets.title')} />
        <NamespacePicker value={namespaceId} onChange={setNamespaceId} />
      </div>
      <ScanPanel namespaceId={effectiveNamespaceId} />
      <ManifestPanel namespaceId={effectiveNamespaceId} />
      <ComparePanel namespaceId={effectiveNamespaceId} />
      <DiffPanel />
    </section>
  )
}
