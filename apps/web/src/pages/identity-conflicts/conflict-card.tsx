// 单个身份冲突卡（FR-177）：卡头 serverId · namespace + 冲突原因徽标；
// 卡体左右两栏平铺冲突双方（bootId / 来源地址 / 最后活跃，差异处高亮）；
// 每栏「保留实例 X」、卡底「解绑此身份」。保留 / 解绑为高风险 → 各弹一次原因必填确认（唯一模态）。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Copy, Network, Server, ShieldCheck, Unlink } from 'lucide-react'

import { AsyncSection, Badge, Button, cn } from '@beacon/ui'
import type { AgentIdentityItem, ConflictPeer } from '@beacon/contracts'

import {
  ApiClientError,
  fetchIdentityDetail,
  resolveConflictIdentity,
  unbindIdentity,
} from '../../api/cluster'
import ReasonDialog from '../servers/reason-dialog'

type CardAction = { kind: 'keep'; peer: ConflictPeer; label: string } | { kind: 'unbind' }

interface ConflictCardProps {
  identity: AgentIdentityItem
  namespaceName: string
}

export default function ConflictCard({ identity, namespaceName }: ConflictCardProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const [action, setAction] = useState<CardAction | null>(null)
  const [errorText, setErrorText] = useState<string | null>(null)

  // 冲突双方明细只在详情端点返回（列表项不带 conflictPeers）：按需拉取本身份详情
  const detailQuery = useQuery({
    queryKey: ['identity-detail', identity.identityId],
    queryFn: () => fetchIdentityDetail(identity.identityId),
    enabled: identity.status === 'conflict',
  })
  const peers = useMemo(() => detailQuery.data?.conflictPeers ?? [], [detailQuery.data])
  // 来源地址是否存在差异（bootId 必然不同、恒高亮；地址差异才高亮，指向两台不同机器）
  const addrDiffers = useMemo(() => new Set(peers.map((p) => p.lastAddr)).size > 1, [peers])

  const invalidate = async () => {
    await queryClient.invalidateQueries({ queryKey: ['identities'] })
    await queryClient.invalidateQueries({ queryKey: ['servers'] })
  }
  const onError = (error: unknown) => {
    setErrorText(error instanceof ApiClientError ? error.message : String(error))
  }
  const onDone = async () => {
    await invalidate()
    setAction(null)
  }

  const resolveMutation = useMutation({
    mutationFn: ({ bootId, reason }: { bootId: string; reason: string }) =>
      resolveConflictIdentity(identity.identityId, bootId, reason),
    onSuccess: onDone,
    onError,
  })
  const unbindMutation = useMutation({
    mutationFn: (reason: string) => unbindIdentity(identity.identityId, reason),
    onSuccess: onDone,
    onError,
  })

  const isProxy = identity.kind === 'proxy'
  const reasonText =
    identity.conflictReason === 'duplicate-boot-id'
      ? t('cluster.identityConflicts.reason.duplicateBootId')
      : t('cluster.identityConflicts.reason.fallback')
  const keeping = action?.kind === 'keep' ? action : null

  return (
    <div className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      {/* 卡头：serverId · namespace + 冲突原因徽标 */}
      <div className="flex flex-wrap items-center gap-2">
        <span
          className={cn(
            'grid size-6 place-items-center rounded-md',
            isProxy ? 'bg-brand-100 text-brand-600' : 'bg-brand-50 text-brand',
          )}
          aria-hidden
        >
          {isProxy ? <Network className="size-3.5" /> : <Server className="size-3.5" />}
        </span>
        <span className="font-mono text-[14px] font-semibold text-ink-1">{identity.serverId}</span>
        <span className="text-ink-4">·</span>
        <span className="text-[12.5px] text-ink-3">{namespaceName}</span>
        <Badge variant="crit" className="ml-auto gap-1.5">
          <Copy className="size-3" />
          {reasonText}
        </Badge>
      </div>

      {/* 卡体：左右两栏平铺冲突双方 */}
      <AsyncSection
        isLoading={detailQuery.isLoading}
        isError={detailQuery.isError}
        error={detailQuery.error}
        skeleton={<div className="h-28 animate-pulse rounded-lg bg-muted" />}
      >
        {peers.length === 0 ? (
          <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-[12.5px] text-ink-4">
            {t('cluster.identityConflicts.card.peersEmpty')}
          </p>
        ) : (
          <div className="grid gap-3 sm:grid-cols-2">
            {peers.map((peer, index) => {
              const label = String.fromCharCode(65 + index)
              const isCurrent = peer.bootId === identity.bootId
              return (
                <div key={peer.bootId} className="grid content-start gap-2.5 rounded-lg border border-border bg-surface-2/40 p-3">
                  <div className="flex items-center gap-2">
                    <span className="grid size-5 place-items-center rounded-md bg-ink-1/90 text-[11px] font-bold text-white">
                      {label}
                    </span>
                    <span className="text-[12.5px] font-semibold text-ink-1">
                      {t('cluster.identityConflicts.card.instance', { label })}
                    </span>
                    {isCurrent && (
                      <Badge variant="off" className="ml-auto">
                        {t('cluster.identityConflicts.card.current')}
                      </Badge>
                    )}
                  </div>
                  <PeerField label={t('cluster.identityConflicts.card.bootId')} value={peer.bootId} highlight mono />
                  <PeerField
                    label={t('cluster.identityConflicts.card.lastAddr')}
                    value={peer.lastAddr}
                    highlight={addrDiffers}
                    mono
                  />
                  <PeerField
                    label={t('cluster.identityConflicts.card.lastSeen')}
                    value={new Date(peer.lastSeenAt).toLocaleString()}
                  />
                  <Button
                    size="sm"
                    className="mt-1 w-full gap-1.5"
                    onClick={() => {
                      setErrorText(null)
                      setAction({ kind: 'keep', peer, label })
                    }}
                  >
                    <ShieldCheck className="size-3.5" />
                    {t('cluster.identityConflicts.card.keep', { label })}
                  </Button>
                </div>
              )
            })}
          </div>
        )}
      </AsyncSection>

      {/* 卡底：解绑此身份 */}
      <div className="flex justify-end border-t border-border pt-3">
        <Button
          size="sm"
          variant="ghost"
          className="gap-1.5 text-ink-3 hover:text-crit"
          onClick={() => {
            setErrorText(null)
            setAction({ kind: 'unbind' })
          }}
        >
          <Unlink className="size-3.5" />
          {t('cluster.identityConflicts.card.unbind')}
        </Button>
      </div>

      {/* 保留一方（高风险）：原因必填 + 落败方 409 指引 */}
      <ReasonDialog
        open={keeping !== null}
        onOpenChange={(isOpen) => {
          if (!isOpen) {
            setAction(null)
          }
        }}
        title={t('cluster.identityConflicts.resolve.title')}
        description={
          keeping
            ? t('cluster.identityConflicts.resolve.desc', { label: keeping.label, bootId: keeping.peer.bootId })
            : ''
        }
        confirmLabel={t('cluster.identityConflicts.resolve.confirm')}
        pending={resolveMutation.isPending}
        errorText={errorText}
        onConfirm={(reason) => {
          if (keeping) {
            resolveMutation.mutate({ bootId: keeping.peer.bootId, reason })
          }
        }}
      >
        <p className="rounded-md border border-warn-bd bg-warn-bg px-3 py-2 text-[11.5px] leading-relaxed text-warn">
          {t('cluster.identityConflicts.resolve.guidance')}
        </p>
      </ReasonDialog>

      {/* 解绑（高风险）：原因必填 */}
      <ReasonDialog
        open={action?.kind === 'unbind'}
        onOpenChange={(isOpen) => {
          if (!isOpen) {
            setAction(null)
          }
        }}
        title={t('cluster.identityConflicts.unbind.title')}
        description={t('cluster.identityConflicts.unbind.desc')}
        confirmLabel={t('cluster.identityConflicts.unbind.confirm')}
        pending={unbindMutation.isPending}
        errorText={errorText}
        impacts={[`serverId ${identity.serverId}`]}
        onConfirm={(reason) => {
          unbindMutation.mutate(reason)
        }}
      />
    </div>
  )
}

// PeerField 一栏内单个字段：标签 + 值；highlight 时值用高亮胶囊突出差异。
function PeerField({
  label,
  value,
  highlight = false,
  mono = false,
}: {
  label: string
  value: string
  highlight?: boolean
  mono?: boolean
}) {
  return (
    <div className="grid gap-0.5">
      <span className="text-[10.5px] tracking-[0.3px] text-ink-4 uppercase">{label}</span>
      <span
        className={cn(
          'w-fit max-w-full truncate rounded px-1.5 py-0.5 text-[12px]',
          mono && 'font-mono',
          highlight ? 'bg-warn-bg font-semibold text-warn' : 'text-ink-2',
        )}
        title={value}
      >
        {value}
      </span>
    </div>
  )
}
