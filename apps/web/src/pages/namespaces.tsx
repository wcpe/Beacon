// namespace 页（/namespaces）：主从布局——左主列（吸顶搜索 + 创建 + 自区滚列表 + 吸底分页），
// 点行 → 右侧非模态详情面板（该 ns 概要 + 互通信任关系 + 授予 / 收回入口）。
// 创建 ns（一次性 token）与授予 / 收回（原因必填）仍走模态。默认强隔离，仅显式授予后单向放通。
import { useMemo, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Boxes, Search, ShieldCheck } from 'lucide-react'

import {
  AsyncSection,
  Button,
  DataTable,
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Input,
  Label,
  SectionHeader,
  TableSkeleton,
  Textarea,
  type DataTableColumn,
} from '@beacon/ui'
import type { NamespaceItem, NamespaceTrustItem, TrustCapability } from '@beacon/devmock'

import {
  ApiClientError,
  createNamespace,
  fetchNamespaceList,
  fetchTrusts,
  grantTrust,
  revokeTrust,
  type GrantTrustBody,
} from '../api/system'
import SystemReasonDialog from '../features/system/reason-dialog'
import { formatIso } from '../features/system/format'
import ListCard from '../features/observability/list-card'
import MasterDetail from '../features/observability/master-detail'
import Pager from '../features/observability/pager'
import GrantDialog from './namespaces/grant-dialog'
import NamespaceDetailPanel from './namespaces/namespace-detail-panel'
import TokenDialog from './namespaces/token-dialog'

const PAGE_SIZE = 15

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

export default function NamespacesPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [keyword, setKeyword] = useState('')
  const [page, setPage] = useState(1)
  const [selectedId, setSelectedId] = useState<number | null>(null)

  // 创建 ns 表单态
  const [createOpen, setCreateOpen] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [createError, setCreateError] = useState<string | null>(null)
  const [token, setToken] = useState<string | null>(null)

  // 授予 / 收回态
  const [grantOpen, setGrantOpen] = useState(false)
  const [grantError, setGrantError] = useState<string | null>(null)
  const [revoking, setRevoking] = useState<NamespaceTrustItem | null>(null)
  const [revokeError, setRevokeError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['namespaces', 'list', keyword, page],
    queryFn: () =>
      fetchNamespaceList({
        keyword: keyword.trim() === '' ? undefined : keyword.trim(),
        page,
        pageSize: PAGE_SIZE,
      }),
    placeholderData: keepPreviousData,
  })

  // 全量信任行：既供列表行的出入度，也供详情面板过滤
  const trustsQuery = useQuery({
    queryKey: ['namespace-trusts', 'list', 'all'],
    queryFn: () => fetchTrusts({ pageSize: 100 }),
  })

  // 授予表单的 namespace 候选（全量）
  const namespacesQuery = useQuery({
    queryKey: ['namespaces', 'options'],
    queryFn: () => fetchNamespaceList({ pageSize: 100 }),
  })

  const total = query.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const items = query.data?.items ?? []
  const trusts = useMemo(() => trustsQuery.data?.items ?? [], [trustsQuery.data])
  const selected = useMemo(() => items.find((row) => row.id === selectedId) ?? null, [items, selectedId])

  // 按 ns 统计生效信任的出 / 入度（用于列表行前置）
  const degree = useMemo(() => {
    const map = new Map<number, { out: number; in: number }>()
    for (const tr of trusts) {
      if (tr.status !== 'active') {
        continue
      }
      const from = map.get(tr.fromNamespaceId) ?? { out: 0, in: 0 }
      from.out += 1
      map.set(tr.fromNamespaceId, from)
      const to = map.get(tr.toNamespaceId) ?? { out: 0, in: 0 }
      to.in += 1
      map.set(tr.toNamespaceId, to)
    }
    return map
  }, [trusts])

  const capabilityLabel = (cap: TrustCapability): string => {
    if (cap === 'schedule') {
      return t('system.namespaces.trusts.capabilitySchedule')
    }
    if (cap === 'message') {
      return t('system.namespaces.trusts.capabilityMessage')
    }
    return t('system.namespaces.trusts.capabilityAgentOps')
  }

  const invalidateAll = async () => {
    await queryClient.invalidateQueries({ queryKey: ['namespaces'] })
    await queryClient.invalidateQueries({ queryKey: ['namespace-trusts'] })
  }

  const createMutation = useMutation({
    mutationFn: () => createNamespace({ name: name.trim(), description: description.trim() || undefined }),
    onSuccess: async (created) => {
      await invalidateAll()
      setCreateOpen(false)
      setToken(created.accessToken)
    },
    onError: (error) => {
      setCreateError(messageOf(error))
    },
  })

  const grantMutation = useMutation({
    mutationFn: (body: GrantTrustBody) => grantTrust(body),
    onSuccess: async () => {
      await invalidateAll()
      setGrantOpen(false)
    },
    onError: (error) => {
      setGrantError(messageOf(error))
    },
  })

  const revokeMutation = useMutation({
    mutationFn: ({ id, reason }: { id: number; reason: string }) => revokeTrust(id, reason),
    onSuccess: async () => {
      await invalidateAll()
      setRevoking(null)
    },
    onError: (error) => {
      setRevokeError(messageOf(error))
    },
  })

  const openCreate = () => {
    setName('')
    setDescription('')
    setCreateError(null)
    setCreateOpen(true)
  }

  // 行内前置：名称 / 描述 / 服务器数 / 信任出入度 / 创建时间
  const columns = useMemo<DataTableColumn<NamespaceItem>[]>(
    () => [
      { header: t('system.namespaces.columns.name'), cell: (row) => <span className="font-medium">{row.name}</span> },
      { header: t('system.namespaces.columns.description'), cell: (row) => row.description || '-' },
      { header: t('system.namespaces.columns.serverCount'), cell: (row) => row.serverCount },
      {
        header: t('system.namespaces.trustDegreeHeader'),
        cell: (row) => {
          const d = degree.get(row.id) ?? { out: 0, in: 0 }
          return <span className="tnum text-ink-2">{t('system.namespaces.trustDegree', { out: d.out, in: d.in })}</span>
        },
      },
      { header: t('system.namespaces.columns.createdAt'), cell: (row) => formatIso(row.createdAt) },
    ],
    [t, degree],
  )

  const toolbar = (
    <div className="grid gap-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
          <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
            <Boxes className="size-[15px]" />
          </span>
          {t('system.namespaces.listTitle')}
        </span>
        {total > 0 && <span className="text-xs text-ink-3">{t('observability.common.total', { count: total })}</span>}
        <Button className="ml-auto" onClick={openCreate}>
          {t('system.namespaces.create')}
        </Button>
      </div>
      <div className="relative w-64 max-w-full">
        <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-ink-4" aria-hidden />
        <Input
          aria-label={t('system.namespaces.keyword')}
          placeholder={t('system.namespaces.keyword')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
            setPage(1)
          }}
          className="pl-8"
        />
      </div>
    </div>
  )

  const master = (
    <ListCard
      toolbar={toolbar}
      footer={total > PAGE_SIZE ? <Pager page={page} pageCount={pageCount} total={total} onPageChange={setPage} /> : undefined}
    >
      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={columns.length} rows={6} />}
      >
        <DataTable
          columns={columns}
          rows={items}
          rowKey={(row) => String(row.id)}
          emptyText={t('system.namespaces.empty')}
          density="compact"
          onRowClick={(row) => {
            setSelectedId(row.id)
          }}
          rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
        />
      </AsyncSection>
    </ListCard>
  )

  return (
    <section className="grid gap-4">
      <SectionHeader size="lg" icon={<ShieldCheck className="size-5" />} title={t('nav.namespaces')} />
      {/* 隔离原则提示：品牌浅底 + 盾牌图标，突出「默认强隔离、显式授予放通」 */}
      <div className="flex items-start gap-2.5 rounded-xl border border-brand-100 bg-brand-50 px-4 py-3">
        <ShieldCheck className="mt-0.5 size-4 shrink-0 text-brand" aria-hidden />
        <p className="text-sm text-ink-2">{t('system.namespaces.isolationHint')}</p>
      </div>

      <MasterDetail
        master={master}
        detail={
          selected ? (
            <NamespaceDetailPanel
              item={selected}
              trusts={trusts}
              onGrant={() => {
                setGrantError(null)
                setGrantOpen(true)
              }}
              onRevoke={(tr) => {
                setRevokeError(null)
                setRevoking(tr)
              }}
            />
          ) : null
        }
        detailTitle={t('system.namespaces.detailTitle')}
        closeLabel={t('system.common.close')}
        onClose={() => {
          setSelectedId(null)
        }}
      />

      {/* 创建 namespace 弹窗 */}
      <Dialog open={createOpen} onOpenChange={setCreateOpen}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t('system.namespaces.createTitle')}</DialogTitle>
            <DialogDescription>{t('system.namespaces.isolationHint')}</DialogDescription>
          </DialogHeader>
          <div className="grid gap-4">
            <div className="grid gap-1.5">
              <Label htmlFor="namespace-name">{t('system.namespaces.nameLabel')}</Label>
              <Input
                id="namespace-name"
                value={name}
                onChange={(e) => {
                  setName(e.target.value)
                }}
                placeholder={t('system.namespaces.namePlaceholder')}
              />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="namespace-desc">{t('system.namespaces.descLabel')}</Label>
              <Textarea
                id="namespace-desc"
                value={description}
                onChange={(e) => {
                  setDescription(e.target.value)
                }}
                rows={2}
              />
            </div>
            {createError && <p className="text-sm text-destructive">{createError}</p>}
          </div>
          <DialogFooter>
            <Button
              disabled={name.trim() === '' || createMutation.isPending}
              onClick={() => {
                setCreateError(null)
                createMutation.mutate()
              }}
            >
              {createMutation.isPending ? t('system.namespaces.creating') : t('system.namespaces.createConfirm')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* 一次性接入 token */}
      <TokenDialog
        open={token !== null}
        onOpenChange={(open) => {
          if (!open) {
            setToken(null)
          }
        }}
        token={token ?? ''}
      />

      {/* 授予单向信任（原因必填） */}
      <GrantDialog
        open={grantOpen}
        onOpenChange={setGrantOpen}
        namespaces={namespacesQuery.data?.items ?? []}
        pending={grantMutation.isPending}
        errorText={grantError}
        onSubmit={(body) => {
          setGrantError(null)
          grantMutation.mutate(body)
        }}
      />

      {/* 收回信任（原因必填，即时生效） */}
      <SystemReasonDialog
        open={revoking !== null}
        onOpenChange={(open) => {
          if (!open) {
            setRevoking(null)
          }
        }}
        title={t('system.namespaces.trusts.confirmRevokeTitle')}
        description={t('system.namespaces.trusts.confirmRevokeDesc')}
        confirmLabel={t('system.namespaces.trusts.confirmRevoke')}
        cancelLabel={t('system.common.cancel')}
        reasonLabel={t('system.namespaces.trusts.revokeReasonLabel')}
        reasonPlaceholder={t('system.namespaces.trusts.revokeReasonPlaceholder')}
        impacts={
          revoking
            ? [`${revoking.fromNamespaceName} → ${revoking.toNamespaceName} · ${capabilityLabel(revoking.capability)}`]
            : undefined
        }
        pending={revokeMutation.isPending}
        errorText={revokeError}
        onConfirm={(reason) => {
          if (revoking) {
            revokeMutation.mutate({ id: revoking.id, reason })
          }
        }}
      />
    </section>
  )
}
