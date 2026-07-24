// 身份冲突页（/identity-conflicts，FR-177）：复制整服目录导致的同 identityId 并发双实例，卡片平铺。
// 一屏看清有哪几个冲突、各自原因与冲突双方明细（bootId/IP/时间）；处置就地完成——保留一方或解绑，
// 唯一模态是「保留 / 解绑」的原因必填二次确认（遵 docs/UX.md 全局范式：详情非模态、模态仅高风险确认）。
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Copy, ShieldCheck } from 'lucide-react'

import { AsyncSection, CardGridSkeleton, PageHeader } from '@beacon/ui'

import { fetchIdentities, fetchNamespaces } from '../api/cluster'
import ConflictCard from './identity-conflicts/conflict-card'

export default function IdentityConflictsPage() {
  const { t } = useTranslation()

  const query = useQuery({
    queryKey: ['identities', 'conflict', undefined],
    queryFn: () => fetchIdentities({ status: 'conflict', pageSize: 100 }),
  })
  // namespace id → 名称，供卡头展示「serverId · namespace」
  const nsQuery = useQuery({ queryKey: ['namespaces'], queryFn: fetchNamespaces })
  const nsNames = useMemo(() => {
    const map = new Map<number, string>()
    for (const ns of nsQuery.data?.items ?? []) {
      map.set(ns.id, ns.name)
    }
    return map
  }, [nsQuery.data])

  const conflicts = query.data?.items ?? []

  return (
    <section className="grid gap-5">
      <PageHeader
        icon={<Copy className="size-5" />}
        title={t('nav.identityConflicts')}
        description={t('cluster.identityConflicts.mission')}
      />
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<CardGridSkeleton count={2} heightClass="h-48" gridClass="grid gap-3.5" />}
      >
        {conflicts.length === 0 ? (
          // 空态：友好非警示（一切正常），不用红色
          <div className="grid place-items-center gap-3 rounded-xl border border-dashed border-border bg-card py-16 text-center">
            <span className="grid size-12 place-items-center rounded-full bg-ok-bg text-ok">
              <ShieldCheck className="size-6" />
            </span>
            <p className="text-[14px] font-semibold text-ink-1">{t('cluster.identityConflicts.empty')}</p>
            <p className="max-w-md text-[12.5px] text-ink-3">{t('cluster.identityConflicts.emptyHint')}</p>
          </div>
        ) : (
          <div className="grid gap-3.5">
            {conflicts.map((identity) => (
              <ConflictCard
                key={identity.identityId}
                identity={identity}
                namespaceName={nsNames.get(identity.namespaceId) ?? `#${String(identity.namespaceId)}`}
              />
            ))}
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
