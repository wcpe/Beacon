// 控制面健康页（/system，只读）：Beacon 自身运行时（版本 / 协程 / 堆 / CPU）+ 子系统健康仪表与明细。
// 数据来自 Legacy /admin/v1/system/status 与 /system/observability，定期轮询刷新。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, CardGridSkeleton, SectionHeader } from '@beacon/ui'

import { fetchSystemObservability, fetchSystemStatus } from '../api/system'
import RuntimeCard from './system/runtime-card'
import SubsystemPanel from './system/subsystem-panel'

const REFETCH_MS = 5000

export default function SystemPage() {
  const { t } = useTranslation()

  const statusQuery = useQuery({
    queryKey: ['system', 'status'],
    queryFn: fetchSystemStatus,
    refetchInterval: REFETCH_MS,
  })

  const observabilityQuery = useQuery({
    queryKey: ['system', 'observability'],
    queryFn: fetchSystemObservability,
    refetchInterval: REFETCH_MS,
  })

  return (
    <section className="grid gap-6">
      <SectionHeader size="lg" title={t('system.health.title')} />

      <AsyncSection
        isLoading={statusQuery.isLoading}
        isError={statusQuery.isError}
        error={statusQuery.error}
        skeleton={<CardGridSkeleton count={1} />}
      >
        {statusQuery.data && <RuntimeCard status={statusQuery.data} />}
      </AsyncSection>

      <AsyncSection
        isLoading={observabilityQuery.isLoading}
        isError={observabilityQuery.isError}
        error={observabilityQuery.error}
        skeleton={<CardGridSkeleton count={3} />}
      >
        {observabilityQuery.data && <SubsystemPanel observability={observabilityQuery.data} />}
      </AsyncSection>
    </section>
  )
}
