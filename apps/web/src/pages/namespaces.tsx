// namespace 页（/namespaces）：namespace 列表 + 创建（一次性接入 token）；互通信任关系列表 + 授予 / 收回。
// 强调 namespace 之间默认强隔离，仅在显式授予单向信任后放通指定能力。
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import NamespacePanel from './namespaces/namespace-panel'
import TrustPanel from './namespaces/trust-panel'

export default function NamespacesPage() {
  const { t } = useTranslation()

  return (
    <section className="grid gap-6">
      <SectionHeader size="lg" title={t('system.namespaces.title')} />
      <p className="rounded-md bg-muted/50 px-4 py-3 text-sm text-muted-foreground">
        {t('system.namespaces.isolationHint')}
      </p>
      <NamespacePanel />
      <TrustPanel />
    </section>
  )
}
