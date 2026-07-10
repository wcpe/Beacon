// API 密钥页（/api-keys）：主从布局——左主列（吸顶操作条含「创建密钥」+ 自区滚列表 + 吸底分页），
// 点行 → 右侧非模态详情面板（用途 / 角色 / 状态 / 前缀 / 过期 / 最近使用 + 吊销 / 重置）。
// 创建（一次性明文）/ 吊销 / 重置的确认表单仍走模态。
import { useMemo, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { KeyRound } from 'lucide-react'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  DestructiveConfirmDialog,
  SectionHeader,
  SummaryStrip,
  TableSkeleton,
  type DataTableColumn,
  type SummaryItem,
} from '@beacon/ui'
import type { ApiKeyItem } from '@beacon/devmock'

import {
  ApiClientError,
  createApiKey,
  fetchApiKeys,
  resetApiKey,
  revokeApiKey,
  type CreateApiKeyBody,
} from '../api/system'
import { formatIso } from '../features/system/format'
import ListCard from '../features/observability/list-card'
import MasterDetail from '../features/observability/master-detail'
import Pager from '../features/observability/pager'
import CreateDialog from './api-keys/create-dialog'
import DetailPanel from './api-keys/detail-panel'
import PlaintextDialog from './api-keys/plaintext-dialog'

const PAGE_SIZE = 12

// 明文弹窗内容：区分创建 / 重置标题
interface PlaintextView {
  title: string
  plaintext: string
}

// 二次确认意图
type ConfirmAction = { kind: 'revoke'; row: ApiKeyItem } | { kind: 'reset'; row: ApiKeyItem }

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}

// 密钥状态 → 语义药丸变体：生效绿 / 已过期灰 / 已吊销红。
function statusTone(status: ApiKeyItem['status']): 'ok' | 'off' | 'crit' {
  return status === 'active' ? 'ok' : status === 'expired' ? 'off' : 'crit'
}

export default function ApiKeysPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [createOpen, setCreateOpen] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<ConfirmAction | null>(null)
  const [plaintext, setPlaintext] = useState<PlaintextView | null>(null)
  const [selectedId, setSelectedId] = useState<number | null>(null)
  const [page, setPage] = useState(1)

  const query = useQuery({
    queryKey: ['api-keys'],
    queryFn: fetchApiKeys,
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['api-keys'] })

  const createMutation = useMutation({
    mutationFn: (body: CreateApiKeyBody) => createApiKey(body),
    onSuccess: async (created) => {
      await invalidate()
      setCreateOpen(false)
      setPlaintext({ title: t('system.apiKeys.plaintextTitle'), plaintext: created.key })
    },
    onError: (error) => {
      setCreateError(messageOf(error))
    },
  })

  const revokeMutation = useMutation({
    mutationFn: (row: ApiKeyItem) => revokeApiKey(row.id),
    onSuccess: async () => {
      await invalidate()
      setConfirm(null)
    },
  })

  const resetMutation = useMutation({
    mutationFn: (row: ApiKeyItem) => resetApiKey(row.id),
    onSuccess: async (reset) => {
      await invalidate()
      setConfirm(null)
      setPlaintext({ title: t('system.apiKeys.resetTitle'), plaintext: reset.key })
    },
  })

  // 行内前置字段：名称 / 角色 / 前缀 / 状态药丸 / 过期时间 / 最近使用（操作移入详情面板）。
  const columns = useMemo<DataTableColumn<ApiKeyItem>[]>(
    () => [
      { header: t('system.apiKeys.columns.name'), cell: (row) => <span className="font-medium">{row.name}</span> },
      { header: t('system.apiKeys.columns.role'), cell: (row) => t(`system.apiKeys.role.${row.role}`) },
      {
        header: t('system.apiKeys.columns.keyPrefix'),
        cell: (row) => <span className="font-mono text-xs">{row.keyPrefix}…</span>,
      },
      {
        header: t('system.apiKeys.columns.status'),
        cell: (row) => (
          <Badge variant={statusTone(row.status)} className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" />
            {t(`system.apiKeys.status.${row.status}`)}
          </Badge>
        ),
      },
      {
        header: t('system.apiKeys.columns.expiresAt'),
        cell: (row) => (row.expiresAt === null ? t('system.apiKeys.never') : formatIso(row.expiresAt)),
      },
      {
        header: t('system.apiKeys.columns.lastUsedAt'),
        cell: (row) => (row.lastUsedAt === null ? t('system.apiKeys.neverUsed') : formatIso(row.lastUsedAt)),
      },
    ],
    [t],
  )

  const revoking = confirm?.kind === 'revoke' ? confirm.row : null
  const resetting = confirm?.kind === 'reset' ? confirm.row : null

  // 顶部汇总：总数 / 生效 / 已过期 / 已吊销
  const items = query.data?.items ?? []
  const summary: SummaryItem[] = useMemo(() => {
    const rows = query.data?.items ?? []
    if (rows.length === 0) {
      return []
    }
    return [
      { label: t('system.apiKeys.summary.total'), value: rows.length },
      {
        label: t('system.apiKeys.summary.active'),
        value: rows.filter((r) => r.status === 'active').length,
        tone: 'success',
      },
      {
        label: t('system.apiKeys.summary.expired'),
        value: rows.filter((r) => r.status === 'expired').length,
        tone: 'muted',
      },
      {
        label: t('system.apiKeys.summary.revoked'),
        value: rows.filter((r) => r.status === 'revoked').length,
        tone: 'danger',
      },
    ]
  }, [query.data, t])

  // 客户端分页（密钥列表端点一次返回全量），分页入口始终吸底可见
  const total = items.length
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))
  const pageItems = useMemo(() => items.slice((page - 1) * PAGE_SIZE, page * PAGE_SIZE), [items, page])
  const selected = useMemo(() => items.find((row) => row.id === selectedId) ?? null, [items, selectedId])

  const toolbar = (
    <div className="flex flex-wrap items-center gap-2">
      <span className="mr-1 flex items-center gap-2 text-[13px] font-semibold text-ink-1">
        <span className="grid size-[26px] place-items-center rounded-lg bg-brand-50 text-brand">
          <KeyRound className="size-[15px]" />
        </span>
        {t('system.apiKeys.listTitle')}
      </span>
      {total > 0 && <span className="text-xs text-ink-3">{t('observability.common.total', { count: total })}</span>}
      <Button
        className="ml-auto"
        onClick={() => {
          setCreateError(null)
          setCreateOpen(true)
        }}
      >
        {t('system.apiKeys.create')}
      </Button>
    </div>
  )

  const master = (
    <div className="grid gap-3.5">
      {items.length > 0 && <SummaryStrip items={summary} />}
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
            rows={pageItems}
            rowKey={(row) => String(row.id)}
            emptyText={t('system.apiKeys.empty')}
            density="compact"
            onRowClick={(row) => {
              setSelectedId(row.id)
            }}
            rowClassName={(row) => (row.id === selectedId ? 'bg-brand-50/60' : undefined)}
          />
        </AsyncSection>
      </ListCard>
    </div>
  )

  return (
    <section className="grid gap-4">
      <SectionHeader size="lg" icon={<KeyRound className="size-5" />} title={t('nav.apiKeys')} />

      <MasterDetail
        master={master}
        detail={
          selected ? (
            <DetailPanel
              item={selected}
              onRevoke={(row) => {
                setConfirm({ kind: 'revoke', row })
              }}
              onReset={(row) => {
                setConfirm({ kind: 'reset', row })
              }}
            />
          ) : null
        }
        detailTitle={t('system.apiKeys.detailTitle')}
        closeLabel={t('system.common.close')}
        onClose={() => {
          setSelectedId(null)
        }}
      />

      <CreateDialog
        open={createOpen}
        onOpenChange={setCreateOpen}
        pending={createMutation.isPending}
        errorText={createError}
        onSubmit={(body) => {
          setCreateError(null)
          createMutation.mutate(body)
        }}
      />

      {/* 吊销确认（破坏性，不可恢复） */}
      <DestructiveConfirmDialog
        open={revoking !== null}
        onOpenChange={(open) => {
          if (!open) {
            setConfirm(null)
          }
        }}
        title={t('system.apiKeys.confirmRevokeTitle', { name: revoking?.name ?? '' })}
        description={t('system.apiKeys.confirmRevokeDesc')}
        confirmLabel={t('system.apiKeys.confirmRevoke')}
        cancelLabel={t('system.common.cancel')}
        impacts={revoking ? [`${revoking.keyPrefix}…`] : undefined}
        pending={revokeMutation.isPending}
        onConfirm={() => {
          if (revoking) {
            revokeMutation.mutate(revoking)
          }
        }}
      />

      {/* 重置确认（旧明文立即失效） */}
      <DestructiveConfirmDialog
        open={resetting !== null}
        onOpenChange={(open) => {
          if (!open) {
            setConfirm(null)
          }
        }}
        title={t('system.apiKeys.confirmResetTitle', { name: resetting?.name ?? '' })}
        description={t('system.apiKeys.confirmResetDesc')}
        confirmLabel={t('system.apiKeys.confirmReset')}
        cancelLabel={t('system.common.cancel')}
        impacts={resetting ? [`${resetting.keyPrefix}…`] : undefined}
        pending={resetMutation.isPending}
        onConfirm={() => {
          if (resetting) {
            resetMutation.mutate(resetting)
          }
        }}
      />

      {/* 一次性明文展示（创建 / 重置） */}
      <PlaintextDialog
        open={plaintext !== null}
        onOpenChange={(open) => {
          if (!open) {
            setPlaintext(null)
          }
        }}
        title={plaintext?.title ?? ''}
        plaintext={plaintext?.plaintext ?? ''}
      />
    </section>
  )
}
