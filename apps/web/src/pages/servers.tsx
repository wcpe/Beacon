// 服务器页（/servers）：注册待确认 + 服务器资产列表 + 健康详情抽屉。
// 高频任务「接入新服」的第一步（待确认），确认后到 /zones 分配。
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import { SectionHeader } from '@beacon/ui'

import AssetsPanel from './servers/assets-panel'
import HealthSheet from './servers/health-sheet'
import PendingPanel from './servers/pending-panel'

export default function ServersPage() {
  const { t } = useTranslation()
  // 健康详情抽屉当前查看的 serverId
  const [healthServerId, setHealthServerId] = useState<string | null>(null)

  return (
    <section className="grid gap-6">
      <SectionHeader size="lg" title={t('nav.servers')} />
      <PendingPanel />
      <AssetsPanel onViewHealth={setHealthServerId} />
      <HealthSheet
        serverId={healthServerId}
        onOpenChange={(open) => {
          if (!open) {
            setHealthServerId(null)
          }
        }}
      />
    </section>
  )
}
