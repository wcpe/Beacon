// 作用域概览 Tab：固定展示全部五层（含无贡献层，规格 §4.10 五层树）。各层列出贡献链
// （实体 / 头版本 / 哈希 / 更新人 / 时间，isRemoval 标已撤销），行「编辑本层」（拉取 head
// 内容与 versionId 后打开编辑器）/「撤销本层贡献」（原因必填）。无贡献实体经「添加本层配置」
// 首次贡献：namespace 直接开空白编辑器，其余层先选实体（server 走服务端搜索）。
import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Layers, Plus } from 'lucide-react'

import { AsyncSection, Badge, Button, SectionHeader, cn } from '@beacon/ui'
import type { ConfigFileDetail, ConfigScopeLevel, ConfigScopeSummary } from '@beacon/contracts'

import { ApiClientError } from '../../api/delivery'
import {
  fetchConfigScopes,
  fetchConfigVersion,
  fetchConfigVersions,
  revokeConfigScope,
} from '../../api/delivery-configs'
import { LEVEL_DOT, SCOPE_LEVELS } from './scope-levels'
import EditDialog, { type EditTarget } from './edit-dialog'
import ReasonDialog from './reason-dialog'
import AddScopeDialog from './add-scope-dialog'

interface ScopesTabProps {
  fileId: number
  file: ConfigFileDetail
}

export default function ScopesTab({ fileId, file }: ScopesTabProps) {
  const { t } = useTranslation()
  const queryClient = useQueryClient()

  const [editTarget, setEditTarget] = useState<EditTarget | null>(null)
  const [editLoading, setEditLoading] = useState<string | null>(null)
  const [revokeTarget, setRevokeTarget] = useState<ConfigScopeSummary | null>(null)
  const [revokeError, setRevokeError] = useState<string | null>(null)
  // 空层首次贡献：待选实体的层级（namespace 不经此弹窗）
  const [addLevel, setAddLevel] = useState<Exclude<ConfigScopeLevel, 'namespace'> | null>(null)

  const query = useQuery({
    queryKey: ['configs', 'scopes', fileId],
    queryFn: () => fetchConfigScopes(fileId),
  })
  const scopes = query.data?.scopes ?? []

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

  // 首次贡献入口：namespace 层唯一实体即文件归属 namespace，直接开空白编辑器；其余层先选实体
  const openFirstContribution = (level: ConfigScopeLevel): void => {
    if (level === 'namespace') {
      setEditTarget({
        scopeLevel: 'namespace',
        scopeRefId: file.namespaceId,
        scopeName: t('delivery.configs.detail.scopes.currentNamespace'),
        headVersionId: null,
        initialContent: '',
      })
      return
    }
    setAddLevel(level)
  }

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<Layers className="size-4" />}
        title={t('delivery.configs.detail.scopes.title')}
      />
      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        <div className="grid gap-2.5">
          {SCOPE_LEVELS.map((level) => {
            const rows = scopes.filter((s) => s.scopeLevel === level)
            // namespace 只有一个可选实体：已有链时编辑走行内入口，不再展示添加
            const showAdd = level !== 'namespace' || rows.length === 0
            return (
              <section key={level} className="overflow-hidden rounded-xl border border-border">
                <header className="flex flex-wrap items-center gap-2 border-b border-border bg-surface-2 px-3 py-1.5">
                  <span className={cn('size-2 rounded-full', LEVEL_DOT[level])} aria-hidden />
                  <span className="text-[13px] font-semibold text-ink-1">
                    {t(`delivery.configs.detail.scopes.levels.${level}`)}
                  </span>
                  <span className="font-mono text-[11px] text-ink-4">{level}</span>
                  {rows.length === 0 && (
                    <span className="text-xs text-ink-4">{t('delivery.configs.detail.scopes.noContribution')}</span>
                  )}
                  {showAdd && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="ml-auto"
                      onClick={() => {
                        openFirstContribution(level)
                      }}
                    >
                      <Plus className="size-3.5" aria-hidden />
                      {t('delivery.configs.detail.scopes.addContribution')}
                    </Button>
                  )}
                </header>
                {rows.map((row) => {
                  const key = `${row.scopeLevel}:${String(row.scopeRefId)}`
                  return (
                    <div
                      key={key}
                      className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-3 py-2 last:border-b-0"
                    >
                      <span className="flex items-center gap-1.5 font-mono text-sm text-ink-1">
                        {row.scopeName}
                        {row.isRemoval && (
                          <Badge variant="off" className="gap-1.5">
                            <span className="size-1.5 rounded-full bg-current" />
                            {t('delivery.configs.detail.scopes.removal')}
                          </Badge>
                        )}
                      </span>
                      <span className="tnum text-xs text-ink-2">v{String(row.headVersionNo)}</span>
                      <span className="tnum font-mono text-xs text-ink-3">{row.headHash.slice(0, 12)}</span>
                      <span className="text-xs text-ink-2">{row.updatedBy}</span>
                      <span className="text-xs text-ink-3">{new Date(row.updatedAt).toLocaleString()}</span>
                      <div className="ml-auto flex flex-wrap justify-end gap-1.5">
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
                    </div>
                  )
                })}
              </section>
            )
          })}
        </div>
      </AsyncSection>

      {addLevel && (
        <AddScopeDialog
          namespaceId={file.namespaceId}
          level={addLevel}
          excludeIds={scopes.filter((s) => s.scopeLevel === addLevel).map((s) => s.scopeRefId)}
          onOpenChange={(open) => {
            if (!open) {
              setAddLevel(null)
            }
          }}
          onPicked={(ref) => {
            setEditTarget({
              scopeLevel: addLevel,
              scopeRefId: ref.id,
              scopeName: ref.name,
              headVersionId: null,
              initialContent: '',
            })
            setAddLevel(null)
          }}
        />
      )}

      {editTarget && (
        <EditDialog
          fileId={fileId}
          sensitivePaths={file.sensitivePaths}
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
