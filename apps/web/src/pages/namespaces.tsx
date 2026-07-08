// namespace 页（/namespaces）：namespace 列表 + 创建（一次性接入 token）；互通信任关系列表 + 授予 / 收回。
// 强调 namespace 之间默认强隔离，仅在显式授予单向信任后放通指定能力。
import { useTranslation } from 'react-i18next'
import { ShieldCheck } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'

import NamespacePanel from './namespaces/namespace-panel'
import TrustPanel from './namespaces/trust-panel'

export default function NamespacesPage() {
  const { t } = useTranslation()

  return (
    <section className="grid gap-6">
      <SectionHeader size="lg" icon={<ShieldCheck className="size-5" />} title={t('nav.namespaces')} />
      {/* 隔离原则提示：品牌浅底 + 盾牌图标，突出「默认强隔离、显式授予放通」 */}
      <div className="flex items-start gap-2.5 rounded-xl border border-brand-100 bg-brand-50 px-4 py-3">
        <ShieldCheck className="mt-0.5 size-4 shrink-0 text-brand" aria-hidden />
        <p className="text-sm text-ink-2">{t('system.namespaces.isolationHint')}</p>
      </div>
      <NamespacePanel />
      <TrustPanel />
    </section>
  )
}
