// 向导第 3 步：选配置变更。列出配置中心文件（可多选），勾选时解析该文件首个贡献层的
// 最新版本作为目标版本（configToVersionId），上一版本作为回滚锚点（configFromVersionId）。
import { useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, Badge, Checkbox } from '@beacon/ui'
import type { ConfigFileItem } from '@beacon/devmock'

import { ApiClientError } from '../../api/delivery'
import { fetchConfigFiles, fetchConfigScopes, fetchConfigVersions } from '../../api/delivery-configs'
import type { WizardConfigPick } from './wizard-state'

interface StepConfigProps {
  namespaceId: number
  picks: WizardConfigPick[]
  onAdd: (pick: WizardConfigPick) => void
  onRemove: (fileId: number) => void
}

export default function WizardStepConfig({ namespaceId, picks, onAdd, onRemove }: StepConfigProps) {
  const { t } = useTranslation()
  const [errorText, setErrorText] = useState<string | null>(null)

  const filesQuery = useQuery({
    queryKey: ['change-orders', 'wizard-config-files', namespaceId],
    queryFn: () => fetchConfigFiles({ namespaceId, pageSize: 100 }),
  })

  // 勾选 → 解析目标版本：取首个未撤销贡献层的头版本；上一版本作 from 锚点
  const resolveMutation = useMutation({
    mutationFn: async (file: ConfigFileItem): Promise<WizardConfigPick> => {
      const { scopes } = await fetchConfigScopes(file.id)
      const scope = scopes.find((s) => !s.isRemoval)
      if (!scope) {
        throw new Error(t('delivery.changes.wizard.config.noVersion'))
      }
      const versions = await fetchConfigVersions(file.id, {
        scopeLevel: scope.scopeLevel,
        scopeRefId: scope.scopeRefId,
        pageSize: 2,
      })
      const head = versions.items.at(0)
      if (head === undefined) {
        throw new Error(t('delivery.changes.wizard.config.noVersion'))
      }
      return {
        fileId: file.id,
        fileName: file.name,
        format: file.format,
        scopeKind: scope.scopeLevel,
        scopeId: scope.scopeRefId,
        scopeName: scope.scopeName,
        fromVersionId: versions.items.at(1)?.versionId ?? null,
        toVersionId: head.versionId,
        toVersionNo: head.versionNo,
      }
    },
    onSuccess: (pick) => {
      onAdd(pick)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError || error instanceof Error ? error.message : String(error))
    },
  })

  // TanStack v5：isPending 分支下 variables 已被判别联合收窄为已赋值
  const resolvingId = resolveMutation.isPending ? resolveMutation.variables.id : null
  const files = filesQuery.data?.items ?? []
  const pickOf = (fileId: number): WizardConfigPick | undefined => picks.find((p) => p.fileId === fileId)

  const handleToggle = (file: ConfigFileItem, checked: boolean): void => {
    setErrorText(null)
    if (checked) {
      resolveMutation.mutate(file)
    } else {
      onRemove(file.id)
    }
  }

  return (
    <div className="grid gap-3">
      <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.config.lead')}</p>

      <AsyncSection isLoading={filesQuery.isLoading} isError={filesQuery.isError} error={filesQuery.error}>
        {files.length === 0 ? (
          <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
            {t('delivery.changes.wizard.config.empty')}
          </p>
        ) : (
          <div className="grid gap-2">
            {/* 表头 */}
            <div className="grid grid-cols-[auto_minmax(0,1fr)_5rem_minmax(0,14rem)] items-center gap-2 px-3 text-xs font-semibold text-ink-3">
              <span className="w-4" aria-hidden />
              <span>{t('delivery.changes.wizard.config.columns.name')}</span>
              <span>{t('delivery.changes.wizard.config.columns.format')}</span>
              <span>{t('delivery.changes.wizard.config.columns.version')}</span>
            </div>
            <ul className="max-h-56 divide-y divide-border overflow-y-auto rounded-lg border border-border">
              {files.map((file) => {
                const pick = pickOf(file.id)
                return (
                  <li
                    key={file.id}
                    className="grid grid-cols-[auto_minmax(0,1fr)_5rem_minmax(0,14rem)] items-center gap-2 px-3 py-1.5 text-sm"
                  >
                    <Checkbox
                      aria-label={file.name}
                      checked={pick !== undefined}
                      disabled={resolvingId === file.id}
                      onCheckedChange={(checked) => {
                        handleToggle(file, checked === true)
                      }}
                    />
                    <span className="min-w-0 truncate font-mono text-xs">{file.name}</span>
                    <Badge variant="outline" className="w-fit uppercase">
                      {file.format}
                    </Badge>
                    <span className="truncate text-xs text-ink-2">
                      {resolvingId === file.id
                        ? t('delivery.changes.wizard.config.resolving')
                        : pick !== undefined
                          ? t('delivery.changes.wizard.config.versionText', {
                              no: pick.toVersionNo,
                              scope: pick.scopeName,
                            })
                          : t('delivery.changes.wizard.config.unpickedVersion')}
                    </span>
                  </li>
                )
              })}
            </ul>
          </div>
        )}
      </AsyncSection>

      {errorText !== null && <p className="text-sm text-destructive">{errorText}</p>}
      {picks.length > 0 && (
        <p className="text-xs text-ink-3">
          {t('delivery.changes.wizard.config.pickedCount', { count: picks.length })}
        </p>
      )}
    </div>
  )
}
