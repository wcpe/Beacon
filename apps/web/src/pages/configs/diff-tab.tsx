// 差异对比 Tab：左右两侧各选一个作用域层（构造 scope:<level>:<refId>）→ GET diff，
// 用 added/removed/changed 键级分组表展示。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { SplitSquareHorizontal } from 'lucide-react'

import {
  AsyncSection,
  Button,
  Label,
  SectionHeader,
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@beacon/ui'
import type { ConfigScopeSummary } from '@beacon/devmock'

import { fetchConfigDiff, fetchConfigScopes } from '../../api/delivery-configs'

interface DiffTabProps {
  fileId: number
}

// 侧标识串：scope:<level>:<refId>
function scopeSpec(scope: ConfigScopeSummary): string {
  return `scope:${scope.scopeLevel}:${String(scope.scopeRefId)}`
}

export default function DiffTab({ fileId }: DiffTabProps) {
  const { t } = useTranslation()
  const [left, setLeft] = useState('')
  const [right, setRight] = useState('')
  // 已应用的左右侧（点「对比」后固化，触发查询）
  const [applied, setApplied] = useState<{ left: string; right: string } | null>(null)

  const scopesQuery = useQuery({
    queryKey: ['configs', 'scopes', fileId],
    queryFn: () => fetchConfigScopes(fileId),
  })
  const scopes = scopesQuery.data?.scopes ?? []

  const diffQuery = useQuery({
    queryKey: ['configs', 'diff', fileId, applied?.left, applied?.right],
    queryFn: () => fetchConfigDiff(fileId, applied?.left ?? '', applied?.right ?? ''),
    enabled: applied !== null,
  })

  const scopeLabel = (spec: string): string => {
    const found = scopes.find((s) => scopeSpec(s) === spec)
    return found ? `${found.scopeLevel} / ${found.scopeName}` : spec
  }

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<SplitSquareHorizontal className="size-4" />}
        title={t('delivery.configs.detail.diff.title')}
      />

      <AsyncSection
        isLoading={scopesQuery.isLoading}
        isError={scopesQuery.isError}
        error={scopesQuery.error}
      >
        <div className="flex flex-wrap items-end gap-3">
          <div className="space-y-1.5">
            <Label htmlFor="config-diff-left">{t('delivery.configs.detail.diff.leftLabel')}</Label>
            <Select value={left} onValueChange={setLeft}>
              <SelectTrigger
                id="config-diff-left"
                className="w-64"
                aria-label={t('delivery.configs.detail.diff.leftLabel')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {scopes.map((s) => (
                  <SelectItem key={scopeSpec(s)} value={scopeSpec(s)}>
                    {s.scopeLevel} / {s.scopeName}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-1.5">
            <Label htmlFor="config-diff-right">{t('delivery.configs.detail.diff.rightLabel')}</Label>
            <Select value={right} onValueChange={setRight}>
              <SelectTrigger
                id="config-diff-right"
                className="w-64"
                aria-label={t('delivery.configs.detail.diff.rightLabel')}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {scopes.map((s) => (
                  <SelectItem key={scopeSpec(s)} value={scopeSpec(s)}>
                    {s.scopeLevel} / {s.scopeName}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <Button
            disabled={left === '' || right === ''}
            onClick={() => {
              setApplied({ left, right })
            }}
          >
            {t('delivery.configs.detail.diff.run')}
          </Button>
        </div>
      </AsyncSection>

      {applied === null ? (
        <p className="text-sm text-ink-3">{t('delivery.configs.detail.diff.pickHint')}</p>
      ) : (
        <AsyncSection isLoading={diffQuery.isLoading} isError={diffQuery.isError} error={diffQuery.error}>
          {diffQuery.data && (
            <div className="grid gap-3">
              <p className="text-sm text-ink-3">
                {scopeLabel(applied.left)} → {scopeLabel(applied.right)}
              </p>
              {diffQuery.data.added.length === 0 &&
              diffQuery.data.removed.length === 0 &&
              diffQuery.data.changed.length === 0 ? (
                <p className="text-sm text-ink-3">
                  {t('delivery.configs.detail.diff.identical')}
                </p>
              ) : (
                <div className="grid gap-3">
                  {diffQuery.data.added.length > 0 && (
                    <div className="grid gap-1">
                      <SectionHeader title={t('delivery.configs.detail.diff.added')} />
                      <ul className="rounded-xl border border-ok-bd bg-ok-bg p-2.5 font-mono text-xs text-ok">
                        {diffQuery.data.added.map((a) => (
                          <li key={a.path}>
                            + {a.path}: {a.right}
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                  {diffQuery.data.removed.length > 0 && (
                    <div className="grid gap-1">
                      <SectionHeader title={t('delivery.configs.detail.diff.removed')} />
                      <ul className="rounded-xl border border-crit-bd bg-crit-bg p-2.5 font-mono text-xs text-crit">
                        {diffQuery.data.removed.map((r) => (
                          <li key={r.path}>
                            - {r.path}: {r.left}
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                  {diffQuery.data.changed.length > 0 && (
                    <div className="grid gap-1">
                      <SectionHeader title={t('delivery.configs.detail.diff.changed')} />
                      <ul className="rounded-xl border border-border bg-surface-2 p-2.5 font-mono text-xs text-ink-2">
                        {diffQuery.data.changed.map((c) => (
                          <li key={c.path}>
                            {c.path}: {c.left} → {c.right}
                          </li>
                        ))}
                      </ul>
                    </div>
                  )}
                </div>
              )}
            </div>
          )}
        </AsyncSection>
      )}
    </section>
  )
}
