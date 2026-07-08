// API 密钥页（/api-keys）：密钥列表（生效 / 过期 / 吊销）+ 创建（一次性明文）+ 吊销 + 重置（一次性新明文）。
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
import CreateDialog from './api-keys/create-dialog'
import PlaintextDialog from './api-keys/plaintext-dialog'

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

export default function ApiKeysPage() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [createOpen, setCreateOpen] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<ConfirmAction | null>(null)
  const [plaintext, setPlaintext] = useState<PlaintextView | null>(null)

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

  // 密钥状态 → 语义药丸变体：生效绿 / 已过期灰 / 已吊销红。
  const statusTone = (status: ApiKeyItem['status']): 'ok' | 'off' | 'crit' =>
    status === 'active' ? 'ok' : status === 'expired' ? 'off' : 'crit'

  const columns = useMemo<DataTableColumn<ApiKeyItem>[]>(
    () => [
      { header: t('system.apiKeys.columns.name'), cell: (row) => row.name },
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
      { header: t('system.apiKeys.columns.createdAt'), cell: (row) => formatIso(row.createdAt) },
      {
        header: t('system.apiKeys.columns.expiresAt'),
        cell: (row) => (row.expiresAt === null ? t('system.apiKeys.never') : formatIso(row.expiresAt)),
      },
      {
        header: t('system.apiKeys.columns.lastUsedAt'),
        cell: (row) => (row.lastUsedAt === null ? t('system.apiKeys.neverUsed') : formatIso(row.lastUsedAt)),
      },
      {
        header: t('system.apiKeys.columns.actions'),
        cell: (row) =>
          row.status === 'active' ? (
            <div className="flex gap-1.5">
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setConfirm({ kind: 'reset', row })
                }}
              >
                {t('system.apiKeys.reset')}
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setConfirm({ kind: 'revoke', row })
                }}
              >
                {t('system.apiKeys.revoke')}
              </Button>
            </div>
          ) : (
            <span className="text-xs text-muted-foreground">-</span>
          ),
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

  return (
    <section className="grid gap-4">
      <SectionHeader
        size="lg"
        icon={<KeyRound className="size-5" />}
        title={t('nav.apiKeys')}
        actions={
          <Button
            onClick={() => {
              setCreateError(null)
              setCreateOpen(true)
            }}
          >
            {t('system.apiKeys.create')}
          </Button>
        }
      />

      {items.length > 0 && <SummaryStrip items={summary} />}

      <AsyncSection
        isLoading={query.isLoading}
        isError={query.isError}
        error={query.error}
        skeleton={<TableSkeleton columns={8} rows={4} />}
      >
        <DataTable
          columns={columns}
          rows={query.data?.items}
          rowKey={(row) => String(row.id)}
          emptyText={t('system.apiKeys.empty')}
          density="compact"
        />
      </AsyncSection>

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
