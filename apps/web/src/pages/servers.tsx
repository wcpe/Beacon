// 服务器页（/servers）：主从布局。页面主体是服务器资产列表（吸顶筛选 + 分页表），
// 注册待确认收敛为吸顶条上的「待确认 N」入口 → 右侧抽屉里处理，健康详情走右侧抽屉。
// 高频任务「接入新服」的第一步（待确认），确认后到 /zones 分配。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Server } from 'lucide-react'

import { SectionHeader } from '@beacon/ui'

import { fetchIdentities } from '../api/cluster'
import AssetsPanel from './servers/assets-panel'
import HealthSheet from './servers/health-sheet'
import PendingSheet from './servers/pending-sheet'

export default function ServersPage() {
  const { t } = useTranslation()
  // 健康详情抽屉当前查看的 serverId
  const [healthServerId, setHealthServerId] = useState<string | null>(null)
  // 注册待确认抽屉开关
  const [pendingOpen, setPendingOpen] = useState(false)

  // 待确认数：吸顶入口徽标用（列表主体不再为它让版面）
  const pendingQuery = useQuery({
    queryKey: ['identities', 'pending', undefined],
    queryFn: () => fetchIdentities({ status: 'pending', pageSize: 100 }),
  })
  const pendingCount = pendingQuery.data?.items.length ?? 0

  return (
    <section className="grid gap-3.5">
      <SectionHeader size="lg" icon={<Server className="size-5" />} title={t('nav.servers')} />
      <AssetsPanel
        onViewHealth={setHealthServerId}
        onOpenPending={() => {
          setPendingOpen(true)
        }}
        pendingCount={pendingCount}
      />
      <PendingSheet open={pendingOpen} onOpenChange={setPendingOpen} />
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
