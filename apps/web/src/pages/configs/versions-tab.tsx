// 版本链 Tab：选作用域（来自 scopes）→ GET versions 表（版本 / 哈希 / 备注 / 创建人 / 时间），
// 行「查看内容」（弹窗）/「回退到此版本」（确认 → POST rollback，无变化 400 内联）。
import { useEffect, useState } from 'react'
import { keepPreviousData, useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import {
  AsyncSection,
  Badge,
  Button,
  DataTable,
  Label,
  SectionHeader,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
  type DataTableColumn,
} from '@beacon/ui'
import type { ConfigScopeLevel, ConfigScopeSummary } from '@beacon/devmock'

import Pager from '../../features/delivery/pager'
import { ApiClientError } from '../../api/delivery'
import {
  fetchConfigScopes,
  fetchConfigVersions,
  rollbackConfigVersion,
  type ConfigVersionListItem,
} from '../../api/delivery-configs'
import ReasonDialog from './reason-dialog'
import VersionViewDialog from './version-view-dialog'

const PAGE_SIZE = 15

interface VersionsTabProps {
  fileId: number
}

// 作用域下拉值：<level>:<refId>
function scopeKey(scope: { scopeLevel: ConfigScopeLevel; scopeRefId: number }): string {
  return `${scope.scopeLevel}:${String(scope.scopeRefId)}`
}

export default function VersionsTab({ fileId }: VersionsTabProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [selectedKey, setSelectedKey] = useState('')
  const [page, setPage] = useState(1)
  const [viewVersionId, setViewVersionId] = useState<number | null>(null)
  const [rollbackTarget, setRollbackTarget] = useState<ConfigVersionListItem | null>(null)
  const [rollbackError, setRollbackError] = useState<string | null>(null)

  const scopesQuery = useQuery({
    queryKey: ['configs', 'scopes', fileId],
    queryFn: () => fetchConfigScopes(fileId),
  })
  const scopes = scopesQuery.data?.scopes ?? []

  // 作用域到达后默认选中第一层
  useEffect(() => {
    if (selectedKey === '' && scopes.length > 0) {
      setSelectedKey(scopeKey(scopes[0]))
    }
  }, [selectedKey, scopes])

  const selectedScope: ConfigScopeSummary | undefined = scopes.find((s) => scopeKey(s) === selectedKey)

  const versionsQuery = useQuery({
    queryKey: ['configs', 'versions', fileId, selectedKey, page],
    queryFn: () =>
      fetchConfigVersions(fileId, {
        scopeLevel: selectedScope?.scopeLevel ?? 'namespace',
        scopeRefId: selectedScope?.scopeRefId ?? 0,
        page,
        pageSize: PAGE_SIZE,
      }),
    enabled: selectedScope !== undefined,
    placeholderData: keepPreviousData,
  })

  const invalidate = () => queryClient.invalidateQueries({ queryKey: ['configs'] })

  const rollbackMutation = useMutation({
    mutationFn: (vars: { versionId: number; reason: string }) =>
      rollbackConfigVersion(vars.versionId, vars.reason),
    onSuccess: async () => {
      await invalidate()
      setRollbackTarget(null)
    },
    onError: (error) => {
      setRollbackError(messageOf(error))
    },
  })

  const total = versionsQuery.data?.total ?? 0

  const columns: DataTableColumn<ConfigVersionListItem>[] = [
    {
      header: t('delivery.configs.detail.versions.columns.version'),
      cell: (row) => (
        <span className="flex items-center gap-1.5">
          v{String(row.versionNo)}
          {row.isRemoval && (
            <Badge variant="secondary">{t('delivery.configs.detail.versions.removal')}</Badge>
          )}
        </span>
      ),
    },
    {
      header: t('delivery.configs.detail.versions.columns.hash'),
      cell: (row) => <span className="font-mono text-xs">{row.contentHash.slice(0, 12)}</span>,
    },
    {
      header: t('delivery.configs.detail.versions.columns.remark'),
      cell: (row) => row.remark || '-',
    },
    {
      header: t('delivery.configs.detail.versions.columns.createdBy'),
      cell: (row) => row.createdBy,
    },
    {
      header: t('delivery.configs.detail.versions.columns.createdAt'),
      cell: (row) => new Date(row.createdAt).toLocaleString(),
    },
    {
      header: '',
      cell: (row) => (
        <div className="flex flex-wrap justify-end gap-1.5">
          <Button
            size="sm"
            variant="ghost"
            onClick={() => {
              setViewVersionId(row.versionId)
            }}
          >
            {t('delivery.configs.detail.versions.view')}
          </Button>
          {!row.isRemoval && (
            <Button
              size="sm"
              variant="ghost"
              onClick={() => {
                setRollbackError(null)
                setRollbackTarget(row)
              }}
            >
              {t('delivery.configs.detail.versions.rollback')}
            </Button>
          )}
        </div>
      ),
    },
  ]

  return (
    <section className="grid gap-3">
      <SectionHeader title={t('delivery.configs.detail.versions.title')} />

      <div className="flex flex-wrap items-end gap-2">
        <div className="space-y-1.5">
          <Label htmlFor="config-versions-scope">
            {t('delivery.configs.detail.versions.pickScope')}
          </Label>
          <Select
            value={selectedKey}
            onValueChange={(value) => {
              setSelectedKey(value)
              setPage(1)
            }}
          >
            <SelectTrigger
              id="config-versions-scope"
              className="w-72"
              aria-label={t('delivery.configs.detail.versions.pickScope')}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {scopes.map((s) => (
                <SelectItem key={scopeKey(s)} value={scopeKey(s)}>
                  {s.scopeLevel} / {s.scopeName}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      <AsyncSection
        isLoading={scopesQuery.isLoading || versionsQuery.isLoading}
        isError={scopesQuery.isError || versionsQuery.isError}
        error={scopesQuery.error ?? versionsQuery.error}
      >
        <DataTable
          columns={columns}
          rows={versionsQuery.data?.items}
          rowKey={(row) => String(row.versionId)}
          emptyText={t('delivery.configs.detail.versions.empty')}
          density="compact"
        />
      </AsyncSection>

      <Pager
        page={page}
        total={total}
        pageSize={PAGE_SIZE}
        onPageChange={(next) => {
          setPage(next)
        }}
      />

      {viewVersionId !== null && (
        <VersionViewDialog
          versionId={viewVersionId}
          onOpenChange={(open) => {
            if (!open) {
              setViewVersionId(null)
            }
          }}
        />
      )}

      {rollbackTarget && (
        <ReasonDialog
          open
          onOpenChange={(open) => {
            if (!open) {
              setRollbackTarget(null)
            }
          }}
          title={t('delivery.configs.rollback.title')}
          description={t('delivery.configs.rollback.desc')}
          confirmLabel={t('delivery.configs.rollback.confirm')}
          impacts={[`v${String(rollbackTarget.versionNo)}`]}
          pending={rollbackMutation.isPending}
          errorText={rollbackError}
          onConfirm={(reason) => {
            rollbackMutation.mutate({ versionId: rollbackTarget.versionId, reason })
          }}
        />
      )}
    </section>
  )
}

function messageOf(error: unknown): string {
  return error instanceof ApiClientError ? error.message : String(error)
}
