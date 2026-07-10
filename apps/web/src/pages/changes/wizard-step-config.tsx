// 向导第 3 步：选配置变更。列出配置中心文件，支持逐项勾选、Shift 连选区间、全选 / 清空；
// 勾选时解析该文件首个贡献层的最新版本作为目标版本（configToVersionId），上一版本作回滚锚点。
// 每行可展开「预览」：目标版本与当前版本的行级 diff（不打断选择流）。
import { useRef, useState } from 'react'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { AsyncSection, Badge, Button, Checkbox, cn } from '@beacon/ui'
import type { ConfigFileItem } from '@beacon/devmock'

import { ApiClientError } from '../../api/delivery'
import { fetchConfigScopes, fetchConfigFiles, fetchConfigVersions } from '../../api/delivery-configs'
import ConfigVersionDiff from '../../features/delivery/config-version-diff'
import type { WizardConfigPick } from './wizard-state'

interface StepConfigProps {
  namespaceId: number
  picks: WizardConfigPick[]
  onAddMany: (picks: WizardConfigPick[]) => void
  onRemoveMany: (fileIds: number[]) => void
}

/** 解析文件的目标版本：取首个未撤销贡献层的头版本；上一版本作 from 锚点 */
async function resolvePick(file: ConfigFileItem, noVersionMessage: string): Promise<WizardConfigPick> {
  const { scopes } = await fetchConfigScopes(file.id)
  const scope = scopes.find((s) => !s.isRemoval)
  if (!scope) {
    throw new Error(noVersionMessage)
  }
  const versions = await fetchConfigVersions(file.id, {
    scopeLevel: scope.scopeLevel,
    scopeRefId: scope.scopeRefId,
    pageSize: 2,
  })
  const head = versions.items.at(0)
  if (head === undefined) {
    throw new Error(noVersionMessage)
  }
  const previous = versions.items.at(1)
  return {
    fileId: file.id,
    fileName: file.name,
    format: file.format,
    scopeKind: scope.scopeLevel,
    scopeId: scope.scopeRefId,
    scopeName: scope.scopeName,
    fromVersionId: previous?.versionId ?? null,
    fromVersionNo: previous?.versionNo ?? null,
    toVersionId: head.versionId,
    toVersionNo: head.versionNo,
  }
}

export default function WizardStepConfig({ namespaceId, picks, onAddMany, onRemoveMany }: StepConfigProps) {
  const { t } = useTranslation()
  const [errorText, setErrorText] = useState<string | null>(null)
  // 展开预览的文件 id（同行再点收起）
  const [previewId, setPreviewId] = useState<number | null>(null)
  // Shift 连选：记录上一次点选的行下标与本次点击是否按住 Shift
  const lastIndexRef = useRef<number | null>(null)
  const shiftKeyRef = useRef(false)

  const filesQuery = useQuery({
    queryKey: ['change-orders', 'wizard-config-files', namespaceId],
    queryFn: () => fetchConfigFiles({ namespaceId, pageSize: 100 }),
  })
  const files = filesQuery.data?.items ?? []
  const pickOf = (fileId: number): WizardConfigPick | undefined => picks.find((p) => p.fileId === fileId)
  const noVersionMessage = t('delivery.changes.wizard.config.noVersion')

  // 勾选（单个或 Shift 区间）→ 并行解析目标版本后整批加入
  const resolveMutation = useMutation({
    mutationFn: (targets: ConfigFileItem[]) =>
      Promise.all(targets.map((file) => resolvePick(file, noVersionMessage))),
    onSuccess: (resolved) => {
      onAddMany(resolved)
    },
    onError: (error) => {
      setErrorText(error instanceof ApiClientError || error instanceof Error ? error.message : String(error))
    },
  })
  // TanStack v5：isPending 分支下 variables 已被判别联合收窄为已赋值
  const resolvingIds = resolveMutation.isPending ? resolveMutation.variables.map((f) => f.id) : []

  // 计算本次点击的作用区间：按住 Shift 且有锚点 → [锚点, 当前] 闭区间，否则仅当前行
  const rangeOf = (index: number): ConfigFileItem[] => {
    const anchor = shiftKeyRef.current ? (lastIndexRef.current ?? index) : index
    const [start, end] = anchor <= index ? [anchor, index] : [index, anchor]
    return files.slice(start, end + 1)
  }

  const handleToggle = (index: number, checked: boolean): void => {
    setErrorText(null)
    const range = rangeOf(index)
    lastIndexRef.current = index
    if (checked) {
      const unpicked = range.filter((file) => pickOf(file.id) === undefined)
      if (unpicked.length > 0) {
        resolveMutation.mutate(unpicked)
      }
    } else {
      onRemoveMany(range.map((file) => file.id))
    }
  }

  const handleSelectAll = (): void => {
    setErrorText(null)
    const unpicked = files.filter((file) => pickOf(file.id) === undefined)
    if (unpicked.length > 0) {
      resolveMutation.mutate(unpicked)
    }
  }

  const handleClear = (): void => {
    setErrorText(null)
    onRemoveMany(picks.map((p) => p.fileId))
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
            {/* 批量操作条：全选 / 清空 + 已选计数 + Shift 连选提示 */}
            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" variant="outline" disabled={resolveMutation.isPending} onClick={handleSelectAll}>
                {t('delivery.changes.wizard.config.selectAll')}
              </Button>
              <Button size="sm" variant="ghost" disabled={picks.length === 0} onClick={handleClear}>
                {t('delivery.changes.wizard.config.clear')}
              </Button>
              <span className="text-xs text-ink-3">
                {t('delivery.changes.wizard.config.pickedCount', { count: picks.length })}
              </span>
              <span className="ml-auto text-xs text-ink-3/80">
                {t('delivery.changes.wizard.config.shiftHint')}
              </span>
            </div>

            {/* 表头 */}
            <div className="grid grid-cols-[auto_minmax(0,1fr)_5rem_minmax(0,12rem)_auto] items-center gap-2 px-3 text-xs font-semibold text-ink-3">
              <span className="w-4" aria-hidden />
              <span>{t('delivery.changes.wizard.config.columns.name')}</span>
              <span>{t('delivery.changes.wizard.config.columns.format')}</span>
              <span>{t('delivery.changes.wizard.config.columns.version')}</span>
              <span className="sr-only">{t('delivery.changes.wizard.config.columns.preview')}</span>
            </div>
            <ul className="max-h-64 divide-y divide-border overflow-y-auto rounded-lg border border-border">
              {files.map((file, index) => {
                const pick = pickOf(file.id)
                const resolving = resolvingIds.includes(file.id)
                const previewing = previewId === file.id
                return (
                  <li
                    key={file.id}
                    // 在捕获阶段记录是否按住 Shift（Radix Checkbox 的 onCheckedChange 不带原生事件）
                    onClickCapture={(event) => {
                      shiftKeyRef.current = event.shiftKey
                    }}
                  >
                    <div className="grid grid-cols-[auto_minmax(0,1fr)_5rem_minmax(0,12rem)_auto] items-center gap-2 px-3 py-1.5 text-sm">
                      <Checkbox
                        aria-label={file.name}
                        checked={pick !== undefined}
                        disabled={resolving}
                        onCheckedChange={(checked) => {
                          handleToggle(index, checked === true)
                        }}
                      />
                      <span className="min-w-0 truncate font-mono text-xs">{file.name}</span>
                      <Badge variant="outline" className="w-fit uppercase">
                        {file.format}
                      </Badge>
                      <span className="truncate text-xs text-ink-2">
                        {resolving
                          ? t('delivery.changes.wizard.config.resolving')
                          : pick !== undefined
                            ? t('delivery.changes.wizard.config.versionText', {
                                no: pick.toVersionNo,
                                scope: pick.scopeName,
                              })
                            : t('delivery.changes.wizard.config.unpickedVersion')}
                      </span>
                      <Button
                        size="sm"
                        variant="ghost"
                        className={cn('h-7 px-2 text-xs', previewing && 'bg-brand-50 text-brand')}
                        aria-expanded={previewing}
                        onClick={() => {
                          setPreviewId(previewing ? null : file.id)
                        }}
                      >
                        {previewing
                          ? t('delivery.changes.wizard.config.previewClose')
                          : t('delivery.changes.wizard.config.preview')}
                      </Button>
                    </div>
                    {previewing && <PreviewPanel file={file} pick={pick} />}
                  </li>
                )
              })}
            </ul>
          </div>
        )}
      </AsyncSection>

      {errorText !== null && <p className="text-sm text-destructive">{errorText}</p>}
    </div>
  )
}

/** 行内展开的版本 diff 预览：未勾选的行也可预览（独立解析目标版本，不影响选择） */
function PreviewPanel({ file, pick }: { file: ConfigFileItem; pick: WizardConfigPick | undefined }) {
  const { t } = useTranslation()
  const noVersionMessage = t('delivery.changes.wizard.config.noVersion')

  // 已勾选直接复用解析结果；未勾选按同一规则独立解析（结果按文件缓存）
  const resolveQuery = useQuery({
    queryKey: ['change-orders', 'wizard-config-preview', file.id],
    queryFn: () => resolvePick(file, noVersionMessage),
    enabled: pick === undefined,
  })
  const resolved = pick ?? resolveQuery.data

  return (
    <div className="grid gap-2 border-t border-dashed border-border bg-surface-2/50 px-3 py-2.5">
      <AsyncSection
        isLoading={pick === undefined && resolveQuery.isLoading}
        isError={pick === undefined && resolveQuery.isError}
        error={resolveQuery.error}
      >
        {resolved !== undefined && (
          <>
            <p className="text-xs text-ink-3">
              {resolved.fromVersionNo === null
                ? t('delivery.changes.wizard.config.previewNew', {
                    no: resolved.toVersionNo,
                    scope: resolved.scopeName,
                  })
                : t('delivery.changes.wizard.config.previewRange', {
                    from: resolved.fromVersionNo,
                    to: resolved.toVersionNo,
                    scope: resolved.scopeName,
                  })}
            </p>
            <ConfigVersionDiff
              fromVersionId={resolved.fromVersionId}
              toVersionId={resolved.toVersionId}
              fromLabel={
                resolved.fromVersionNo === null
                  ? t('delivery.preview.versionDiff.fromEmpty')
                  : t('delivery.preview.versionDiff.fromLabel', { no: resolved.fromVersionNo })
              }
              toLabel={t('delivery.preview.versionDiff.toLabel', { no: resolved.toVersionNo })}
            />
          </>
        )}
      </AsyncSection>
    </div>
  )
}
