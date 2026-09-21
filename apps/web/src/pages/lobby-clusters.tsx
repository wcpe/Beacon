// 大厅集群页：独立大厅成员的查看与迁移，不向区服结构树写入大厅节点。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { RefreshCw, UserPlus } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  Label,
  PageHeader,
  Textarea,
  type DataTableColumn,
} from '@beacon/ui'
import type { LobbyMember, ServerItem } from '@beacon/contracts'

import {
  fetchIdentities,
  fetchLobbyClusterDetail,
  fetchLobbyClusters,
  fetchServers,
  transferServerPlacement,
} from '../api/cluster'
import NamespaceSelect from '../features/cluster/namespace-select'

function availability(member: LobbyMember): { label: string; variant: 'ok' | 'warn' | 'off' } {
  if (member.draining) {
    return { label: '排水中', variant: 'warn' }
  }
  if (member.schedulable) {
    return { label: '健康 / 可接入', variant: 'ok' }
  }
  return { label: '不可接入', variant: 'off' }
}

export default function LobbyClustersPage() {
  const [namespaceId, setNamespaceId] = useState<number | null>(null)
  const [moveInOpen, setMoveInOpen] = useState(false)
  const [candidate, setCandidate] = useState<ServerItem | null>(null)
  const [leaving, setLeaving] = useState<LobbyMember | null>(null)
  const [reason, setReason] = useState('')
  const queryClient = useQueryClient()

  const listQuery = useQuery({
    queryKey: ['lobby-clusters', namespaceId],
    queryFn: () => fetchLobbyClusters(namespaceId ?? 0),
    enabled: namespaceId !== null,
  })
  const lobby = listQuery.data?.items[0] ?? null
  const detailQuery = useQuery({
    queryKey: ['lobby-cluster', lobby?.id],
    queryFn: () => fetchLobbyClusterDetail(lobby?.id ?? 0),
    enabled: lobby !== null,
  })
  const candidatesServersQuery = useQuery({
    queryKey: ['servers', 'lobby-candidates', namespaceId],
    queryFn: () => fetchServers({ namespaceId: namespaceId ?? 0, kind: 'backend', pageSize: 100 }),
    enabled: moveInOpen && namespaceId !== null,
  })
  const candidatesIdentitiesQuery = useQuery({
    queryKey: ['identities', 'lobby-candidates', namespaceId],
    queryFn: () => fetchIdentities({ namespaceId: namespaceId ?? 0, pageSize: 100 }),
    enabled: moveInOpen && namespaceId !== null,
  })
  const candidates = useMemo(() => {
    const memberIds = new Set(detailQuery.data?.members.map((member) => member.serverId) ?? [])
    const approvedIds = new Set(
      (candidatesIdentitiesQuery.data?.items ?? [])
        .filter(
          (identity) =>
            identity.kind === 'backend' &&
            identity.serverId !== null &&
            (identity.status === 'active' || identity.status === 'disabled'),
        )
        .map((identity) => identity.serverId),
    )
    return (candidatesServersQuery.data?.items ?? []).filter(
      (server) => server.kind === 'backend' && approvedIds.has(server.serverId) && !memberIds.has(server.serverId),
    )
  }, [candidatesIdentitiesQuery.data, candidatesServersQuery.data, detailQuery.data])
  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['lobby-clusters'] })
    await queryClient.invalidateQueries({ queryKey: ['lobby-cluster'] })
    await queryClient.invalidateQueries({ queryKey: ['servers'] })
    await queryClient.invalidateQueries({ queryKey: ['zone-tree'] })
  }
  const moveIn = useMutation({
    mutationFn: (row: ServerItem) =>
      transferServerPlacement(row.serverId, { kind: 'lobby_cluster', id: lobby?.id ?? 0 }, reason.trim()),
    onSuccess: async () => {
      await invalidate()
      setCandidate(null)
      setReason('')
      setMoveInOpen(false)
    },
  })
  const moveOut = useMutation({
    mutationFn: (row: LobbyMember) => transferServerPlacement(row.serverId, null, reason.trim()),
    onSuccess: async () => {
      await invalidate()
      setLeaving(null)
      setReason('')
    },
  })

  const columns = useMemo<DataTableColumn<LobbyMember>[]>(
    () => [
      { header: '服务器', cell: (row) => <span className="font-mono font-semibold">{row.serverId}</span> },
      {
        header: '在线 / 玩家',
        cell: (row) => (row.online ? `在线 · ${String(row.playerCount)}` : '离线 · 0'),
      },
      {
        header: '健康 / 可接入',
        cell: (row) => {
          const status = availability(row)
          return <Badge variant={status.variant}>{status.label}</Badge>
        },
      },
      {
        header: '状态',
        cell: (row) => <Badge variant={row.draining ? 'warn' : 'brand'}>{row.draining ? '排水中' : '大厅成员'}</Badge>,
      },
      {
        header: '操作',
        cell: (row) => {
          const blocked = row.online && row.playerCount > 0
          return (
            <div className="flex items-center gap-2">
              <Button
                size="sm"
                variant="outline"
                disabled={blocked}
                title={blocked ? '在线且有玩家，需先排水' : undefined}
                onClick={() => {
                  setReason('')
                  setLeaving(row)
                }}
              >
                迁出
              </Button>
              {blocked && <span className="text-xs text-warn">在线且有玩家，需先排水</span>}
            </div>
          )
        },
      },
    ],
    [],
  )

  const detail = detailQuery.data
  const drainingCount = detail?.members.filter((member) => member.draining).length ?? 0
  const summary = detail
    ? `大厅成员 ${String(detail.memberTotal)} · 可接入 ${String(detail.schedulableCount)} · 排水中 ${String(drainingCount)} · 无候选 ${detail.ready ? '否' : '是'}`
    : null
  const loading = (namespaceId !== null && listQuery.isLoading) || detailQuery.isLoading
  const hasError = listQuery.isError || detailQuery.isError
  const error = listQuery.error ?? detailQuery.error

  return (
    <section className="grid gap-4">
      <PageHeader
        actions={
          <>
            <NamespaceSelect value={namespaceId} onChange={setNamespaceId} allowAll={false} />
            <Button
              size="sm"
              variant="outline"
              aria-label="刷新"
              disabled={listQuery.isFetching || detailQuery.isFetching || namespaceId === null}
              onClick={() => {
                void listQuery.refetch()
                if (lobby) void detailQuery.refetch()
              }}
            >
              <RefreshCw className="size-3.5" />
              刷新
            </Button>
          </>
        }
      />

      <AsyncSection
        isLoading={loading}
        isError={hasError}
        error={error}
        skeleton={<div className="h-36 animate-pulse rounded-xl bg-surface-2" />}
      >
        {detail ? (
          <div className="grid gap-3.5">
            <div className="rounded-xl border border-border bg-card p-4">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="grid gap-1">
                  <p className="text-sm font-semibold text-ink-1">入口就绪</p>
                  <p className="text-sm text-ink-3">{summary}</p>
                </div>
                <Badge variant={detail.ready ? 'ok' : 'crit'}>{detail.ready ? '可接入' : '无候选'}</Badge>
              </div>
            </div>

            <div className="rounded-xl border border-border bg-card p-4">
              <div className="mb-3 flex flex-wrap items-center justify-between gap-3">
                <div>
                  <h2 className="text-sm font-semibold text-ink-1">大厅成员（仅 Bukkit）</h2>
                  <p className="mt-1 text-xs text-ink-3">迁入会解除原小区归属；迁出后可迁往小区或保持未分配。</p>
                </div>
                <Button
                  size="sm"
                  onClick={() => {
                    setCandidate(null)
                    setReason('')
                    setMoveInOpen(true)
                  }}
                >
                  <UserPlus className="size-3.5" />
                  迁入大厅
                </Button>
              </div>
              {detail.members.length === 0 ? (
                <div className="rounded-lg border border-dashed border-border px-4 py-8 text-center text-sm text-ink-3">
                  无大厅成员时，BC 首次连接拒绝；先确认 Bukkit 身份，再迁入。
                </div>
              ) : (
                <DataTable
                  columns={columns}
                  rows={detail.members}
                  rowKey={(row) => row.serverId}
                  emptyText="暂无大厅成员"
                />
              )}
            </div>

            <p className="text-xs text-ink-3">
              待确认身份：仅在既有确认入口处置，审批必须显式填写 serverId。{' '}
              <Link className="text-brand underline-offset-2 hover:underline" to="/servers">
                前往待确认身份
              </Link>
            </p>
          </div>
        ) : namespaceId === null || (listQuery.isSuccess && lobby === null) ? (
          <div className="rounded-xl border border-dashed border-border px-4 py-12 text-center text-sm text-ink-3">
            无大厅成员时，BC 首次连接拒绝；先确认 Bukkit 身份，再迁入。
          </div>
        ) : null}
      </AsyncSection>

      <Dialog
        open={moveInOpen}
        onOpenChange={(open) => {
          setMoveInOpen(open)
          if (!open) {
            setCandidate(null)
            setReason('')
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>迁入大厅</DialogTitle>
          </DialogHeader>
          <div className="grid gap-3">
            <div className="grid gap-2">
              {candidates.map((row) => (
                <Button
                  key={row.id}
                  variant={candidate?.id === row.id ? 'default' : 'outline'}
                  className="justify-start font-mono"
                  onClick={() => {
                    setCandidate(row)
                  }}
                >
                  迁入 {row.serverId}
                </Button>
              ))}
              {!candidatesServersQuery.isLoading && !candidatesIdentitiesQuery.isLoading && candidates.length === 0 && (
                <p className="text-sm text-ink-3">暂无已确认且可迁入的 Bukkit 候选。</p>
              )}
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="lobby-move-in-reason">迁移原因</Label>
              <Textarea
                id="lobby-move-in-reason"
                value={reason}
                onChange={(event) => {
                  setReason(event.target.value)
                }}
                placeholder="说明迁入大厅的原因"
                rows={3}
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setMoveInOpen(false)
              }}
            >
              取消
            </Button>
            <Button
              disabled={candidate === null || reason.trim() === '' || moveIn.isPending}
              onClick={() => {
                if (candidate) moveIn.mutate(candidate)
              }}
            >
              确认迁入
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      <Dialog
        open={leaving !== null}
        onOpenChange={(open) => {
          if (!open) {
            setLeaving(null)
            setReason('')
          }
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>迁出大厅</DialogTitle>
          </DialogHeader>
          <div className="grid gap-3">
            <p className="text-sm text-ink-3">迁出 {leaving?.serverId ?? ''} 后，该服务器保持未分配，可再迁往业务小区。</p>
            <div className="grid gap-1.5">
              <Label htmlFor="lobby-move-out-reason">迁移原因</Label>
              <Textarea
                id="lobby-move-out-reason"
                value={reason}
                onChange={(event) => {
                  setReason(event.target.value)
                }}
                placeholder="说明迁出大厅的原因"
                rows={3}
              />
            </div>
          </div>
          <DialogFooter>
            <Button
              variant="outline"
              onClick={() => {
                setLeaving(null)
              }}
            >
              取消
            </Button>
            <Button
              disabled={moveOut.isPending || leaving === null || reason.trim() === ''}
              onClick={() => {
                if (leaving) moveOut.mutate(leaving)
              }}
            >
              确认迁出
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}
