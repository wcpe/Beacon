// 服务器页（/servers）：主从布局。页面主体是服务器资产列表（吸顶筛选 + 分页表），
// 注册待确认收敛为吸顶条上的「待确认 N」入口 → 右侧抽屉里处理，健康详情走右侧抽屉。
// 高频任务「接入新服」的第一步（待确认），确认后到 /zones 分配。
// FR-178：待确认计数与抽屉列表跟随顶栏 env 作用域。
// URL ?keyword= 承接区服树「查看健康详情」跳转，预填搜索。
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router-dom'
import { Server } from 'lucide-react'

import { PageHeader } from '@beacon/ui'

import { fetchIdentities } from '../api/cluster'
import { isDemoMode } from '../demo-mode'
import { fetchPagedItemsByEnvScope, useEnvNamespaceScope } from '../features/env/use-env-scope'
import LifecycleMockReview from '../features/lifecycle/mock-review'
import AssetsPanel from './servers/assets-panel'
import HealthSheet from './servers/health-sheet'
import PendingSheet from './servers/pending-sheet'

export default function ServersPage() {
  const { t } = useTranslation()
  const [searchParams] = useSearchParams()
  // 区服树 / 告警互跳：?keyword=serverId 预填资产搜索
  const initialKeyword = searchParams.get('keyword') ?? ''
  // 健康详情抽屉当前查看的 serverId
  const [healthServerId, setHealthServerId] = useState<string | null>(null)
  // 注册待确认抽屉开关
  const [pendingOpen, setPendingOpen] = useState(false)
  const envScope = useEnvNamespaceScope()

  // 待确认数：按每个命名空间受限请求并汇总，绝不拉全量后过滤。
  const pendingQuery = useQuery({
    queryKey: ['identities', 'pending', envScope],
    queryFn: () =>
      fetchPagedItemsByEnvScope(envScope, (namespaceId) =>
        fetchIdentities({ status: 'pending', namespaceId, pageSize: 100 }),
      ),
  })
  const pendingCount = useMemo(() => pendingQuery.data?.items.length ?? 0, [pendingQuery.data])

  return (
    <section className="grid gap-4">
      <PageHeader
        icon={<Server className="size-4" />}
        title={t('nav.servers')}
      />
      <AssetsPanel
        initialKeyword={initialKeyword}
        onViewHealth={setHealthServerId}
        onOpenPending={() => {
          setPendingOpen(true)
        }}
        pendingCount={pendingCount}
      />
      {isDemoMode() && <LifecycleMockReview subject="server" />}
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
