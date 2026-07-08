// 有效配置 Tab：目标选择（默认按 namespace，可选填 serverId 演示）→ GET effective，
// 展示合并内容（敏感值已脱敏）、有效哈希、逐键来源表、被删除的键。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { FileCheck } from 'lucide-react'

import {
  AsyncSection,
  Button,
  DataTable,
  Input,
  Label,
  SectionHeader,
  type DataTableColumn,
} from '@beacon/ui'
import type { ConfigProvenanceEntry } from '@beacon/devmock'

import { fetchConfigEffective } from '../../api/delivery-configs'

interface EffectiveTabProps {
  fileId: number
}

export default function EffectiveTab({ fileId }: EffectiveTabProps) {
  const { t } = useTranslation()
  // serverIdDraft：输入中的目标 serverId；serverId：已应用的目标（空=按 namespace）
  const [serverIdDraft, setServerIdDraft] = useState('')
  const [serverId, setServerId] = useState('')

  const query = useQuery({
    queryKey: ['configs', 'effective', fileId, serverId],
    queryFn: () =>
      fetchConfigEffective(fileId, {
        serverId: serverId.trim() === '' ? undefined : serverId.trim(),
      }),
  })

  const provColumns: DataTableColumn<ConfigProvenanceEntry>[] = [
    {
      header: t('delivery.configs.detail.effective.provColumns.key'),
      cell: (row) => <span className="font-mono text-ink-1">{row.path}</span>,
    },
    {
      header: t('delivery.configs.detail.effective.provColumns.from'),
      cell: (row) => (
        <span className="font-mono text-xs text-ink-2">
          {row.scopeLevel} / {row.scopeName}
        </span>
      ),
    },
    {
      header: t('delivery.configs.detail.effective.provColumns.version'),
      cell: (row) => <span className="tnum text-ink-2">v{String(row.versionNo)}</span>,
    },
  ]

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<FileCheck className="size-4" />}
        title={t('delivery.configs.detail.effective.title')}
      />

      <div className="flex flex-wrap items-end gap-2">
        <div className="space-y-1.5">
          <Label htmlFor="config-effective-target">
            {t('delivery.configs.detail.effective.targetLabel')}
          </Label>
          <Input
            id="config-effective-target"
            aria-label={t('delivery.configs.detail.effective.targetLabel')}
            placeholder="serverId（留空=按 namespace）"
            value={serverIdDraft}
            onChange={(e) => {
              setServerIdDraft(e.target.value)
            }}
            className="w-64"
          />
        </div>
        <Button
          variant="outline"
          onClick={() => {
            setServerId(serverIdDraft)
          }}
        >
          {t('delivery.configs.detail.diff.run')}
        </Button>
      </div>

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {query.data && (
          <div className="grid gap-4">
            <p className="tnum text-sm text-ink-3">
              {t('delivery.configs.detail.effective.hash', { hash: query.data.effectiveHash.slice(0, 16) })}
            </p>

            <div className="grid gap-1.5">
              <SectionHeader title={t('delivery.configs.detail.effective.content')} />
              <pre className="overflow-x-auto rounded-xl border border-border bg-surface-2 p-3 font-mono text-xs whitespace-pre-wrap text-ink-2">
                {query.data.effectiveContent === '' ? '(空)' : query.data.effectiveContent}
              </pre>
            </div>

            <div className="grid gap-1.5">
              <SectionHeader title={t('delivery.configs.detail.effective.provenance')} />
              <DataTable
                columns={provColumns}
                rows={query.data.provenance}
                rowKey={(row) => row.path}
                emptyText="-"
                density="compact"
              />
            </div>

            <div className="grid gap-1.5">
              <SectionHeader title={t('delivery.configs.detail.effective.deletedKeys')} />
              {query.data.deletedKeys.length === 0 ? (
                <p className="text-sm text-ink-3">
                  {t('delivery.configs.detail.effective.noDeleted')}
                </p>
              ) : (
                <ul className="list-disc space-y-1 pl-5 text-sm text-ink-2">
                  {query.data.deletedKeys.map((k) => (
                    <li key={`${k.path}:${k.scopeLevel}:${String(k.scopeRefId)}`} className="font-mono">
                      {k.path}（{k.scopeLevel} · v{String(k.versionNo)}）
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
