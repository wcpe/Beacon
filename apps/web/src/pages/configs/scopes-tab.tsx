// 作用域概览 Tab：GET scopes 列出各层贡献（层级 / 头版本 / 哈希 / 更新人 / 时间，isRemoval 标已撤销），
// 行「编辑本层」（拉取 head 内容与 versionId 后打开编辑器）/「撤销本层贡献」（原因必填）。
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Layers } from 'lucide-react'

import { AsyncSection, Badge, Button, DataTable, SectionHeader, type DataTableColumn } from '@beacon/ui'
import type { ConfigScopeSummary } from '@beacon/devmock'

import { ApiClientError } from '../../api/delivery'
import {
  fetchConfigScopes,
  fetchConfigVersion,
  fetchConfigVersions,
  revokeConfigScope,
} from '../../api/delivery-configs'
import EditDialog, { type EditTarget } from './edit-dialog'
import ReasonDialog from './reason-dialog'

interface ScopesTabProps {
  fileId: number
}

export default function ScopesTab({ fileId }: ScopesTabProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [editTarget, setEditTarget] = useState<EditTarget | null>(null)
  const [editLoading, setEditLoading] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ConfigScopeSummary | null>(null)
  const [revokeError, setRevokeError] = useState<string | null>(null)

  const query = useQuery({
    queryKey: ['configs', 'scopes', fileId],
    queryFn: () => fetchConfigScopes(fileId),
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['configs'] })

  const revokeMutation = useMutation({
    mutationFn: (vars: { scope: ConfigScopeSummary; reason: string }) =>
      revokeConfigScope(fileId, vars.scope.scopeLevel, vars.scope.scopeRefId, vars.reason),
    onSuccess: async () => {
      await invalidate()
      setRevokeTarget(null)
    },
    onError: (error) => {
      setRevokeError(messageOf(error))
    },
  })

  // 打开编辑器前先取该层 head 的 versionId 与内容（作 basedOnVersionId 与初始草稿）
  const openEditor = async (scope: ConfigScopeSummary): Promise<void> => {
    const key = `${scope.scopeLevel}:${String(scope.scopeRefId)}`
    setEditLoading(key)
    try {
      const versions = await fetchConfigVersions(fileId, {
        scopeLevel: scope.scopeLevel,
        scopeRefId: scope.scopeRefId,
        page: 1,
        pageSize: 1,
      })
      // 该层来自现存贡献链，必有至少一个版本；取最新版（列表新→旧）作 head
      const head = versions.items[0]
      const initialContent = (await fetchConfigVersion(head.versionId)).content
      setEditTarget({
        scopeLevel: scope.scopeLevel,
        scopeRefId: scope.scopeRefId,
        scopeName: scope.scopeName,
        headVersionId: head.versionId,
        initialContent,
      })
    } finally {
      setEditLoading(null)
    }
  }

  const columns: DataTableColumn<ConfigScopeSummary>[] = [
    {
      header: t('delivery.configs.detail.scopes.columns.scope'),
      cell: (row) => (
        <span className="flex items-center gap-1.5 font-mono text-ink-1">
          {row.scopeLevel} / {row.scopeName}
          {row.isRemoval && (
            <Badge variant="off" className="gap-1.5">
              <span className="size-1.5 rounded-full bg-current" />
              {t('delivery.configs.detail.scopes.removal')}
            </Badge>
          )}
        </span>
      ),
    },
    {
      header: t('delivery.configs.detail.scopes.columns.version'),
      cell: (row) => <span className="tnum text-ink-2">v{String(row.headVersionNo)}</span>,
    },
    {
      header: t('delivery.configs.detail.scopes.columns.hash'),
      cell: (row) => <span className="tnum font-mono text-xs text-ink-3">{row.headHash.slice(0, 12)}</span>,
    },
    {
      header: t('delivery.configs.detail.scopes.columns.updatedBy'),
      cell: (row) => <span className="text-ink-2">{row.updatedBy}</span>,
    },
    {
      header: t('delivery.configs.detail.scopes.columns.updatedAt'),
      cell: (row) => <span className="text-ink-3">{new Date(row.updatedAt).toLocaleString()}</span>,
    },
    {
      header: '',
      cell: (row) => {
        const key = `${row.scopeLevel}:${String(row.scopeRefId)}`
        return (
          <div className="flex flex-wrap justify-end gap-1.5">
            <Button
              size="sm"
              variant="ghost"
              disabled={editLoading !== null}
              onClick={() => {
                void openEditor(row)
              }}
            >
              {editLoading === key ? '…' : t('delivery.configs.detail.scopes.edit')}
            </Button>
            {!row.isRemoval && (
              <Button
                size="sm"
                variant="ghost"
                onClick={() => {
                  setRevokeError(null)
                  setRevokeTarget(row)
                }}
              >
                {t('delivery.configs.detail.scopes.revoke')}
              </Button>
            )}
          </div>
        )
      },
    },
  ]

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<Layers className="size-4" />}
        title={t('delivery.configs.detail.scopes.title')}
      />
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <DataTable
          columns={columns}
          rows={query.data?.scopes}
          rowKey={(row) => `${row.scopeLevel}:${String(row.scopeRefId)}`}
          emptyText={t('delivery.configs.detail.scopes.empty')}
          density="compact"
        />
      </AsyncSection>

      {editTarget && (
        <EditDialog
          fileId={fileId}
          target={editTarget}
          onOpenChange={(open) => {
            if (!open) {
              setEditTarget(null)
            }
          }}
          onSaved={() => {
            setEditTarget(null)
            void invalidate()
          }}
        />
      )}

      {revokeTarget && (
        <ReasonDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setRevokeTarget(null)
            }
          }}
          title={t('delivery.configs.revoke.title')}
          description={t('delivery.configs.revoke.desc')}
          confirmLabel={t('delivery.configs.revoke.confirm')}
          impacts={[`${revokeTarget.scopeLevel} / ${revokeTarget.scopeName}`]}
          pending={revokeMutation.isPending}
          errorText={revokeError}
          onConfirm={(reason) => {
            revokeMutation.mutate({ scope: revokeTarget, reason })
          }}
        />
      )}
    </section>
  )
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}
