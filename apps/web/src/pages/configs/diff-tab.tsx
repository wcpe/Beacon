// 差异对比 Tab：左右两侧各选一种描述（规格 §4.5，任意组合）——
// scope:<level>:<refId>（某层当前 head）/ version:<versionId>（历史版本，先选链再选版本）/
// effective:<targetType>:<targetId>（某目标的有效合并结果，server 走服务端搜索）。
// GET diff 后按 added/removed/changed 键级分组展示（敏感值已脱敏）。
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
import type { ConfigFileDetail, ConfigScopeLevel, ConfigScopeSummary } from '@beacon/contracts'

import { fetchConfigDiff, fetchConfigScopes, fetchConfigVersions } from '../../api/delivery-configs'
import { SCOPE_LEVELS } from './scope-levels'
import ScopeRefPicker, { type ScopeRef } from './scope-ref-picker'

interface DiffTabProps {
  fileId: number
  file: ConfigFileDetail
}

/** 一侧描述的草稿态 */
interface DiffSide {
  kind: 'scope' | 'version' | 'effective'
  // scope / version：所选贡献链（`${level}:${refId}`）
  scopeKey: string
  // version：所选历史版本 id
  versionId: number | null
  // effective：目标类型与实体
  targetType: ConfigScopeLevel
  targetRef: ScopeRef | null
}

const EMPTY_SIDE: DiffSide = { kind: 'scope', scopeKey: '', versionId: null, targetType: 'namespace', targetRef: null }

// 侧标识串：scope:<level>:<refId>
function scopeSpec(scope: ConfigScopeSummary): string {
  return `scope:${scope.scopeLevel}:${String(scope.scopeRefId)}`
}

/** 组装一侧的 diff 描述串；未选全返回 null */
function specOf(side: DiffSide, file: ConfigFileDetail): string | null {
  if (side.kind === 'scope') {
    return side.scopeKey === '' ? null : `scope:${side.scopeKey}`
  }
  if (side.kind === 'version') {
    return side.versionId === null ? null : `version:${String(side.versionId)}`
  }
  if (side.targetType === 'namespace') {
    return `effective:namespace:${String(file.namespaceId)}`
  }
  return side.targetRef === null ? null : `effective:${side.targetType}:${String(side.targetRef.id)}`
}

export default function DiffTab({ fileId, file }: DiffTabProps) {
  const { t } = useTranslation()
  const [left, setLeft] = useState<DiffSide>(EMPTY_SIDE)
  const [right, setRight] = useState<DiffSide>(EMPTY_SIDE)
  // 已应用的左右描述串（点「对比」后固化，触发查询）
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

  const leftSpec = specOf(left, file)
  const rightSpec = specOf(right, file)

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
        <div className="grid gap-3 lg:grid-cols-2">
          <SideSelector
            idPrefix="config-diff-left"
            label={t('delivery.configs.detail.diff.leftLabel')}
            side={left}
            onChange={setLeft}
            scopes={scopes}
            fileId={fileId}
            file={file}
          />
          <SideSelector
            idPrefix="config-diff-right"
            label={t('delivery.configs.detail.diff.rightLabel')}
            side={right}
            onChange={setRight}
            scopes={scopes}
            fileId={fileId}
            file={file}
          />
        </div>
        <div className="mt-3">
          <Button
            disabled={leftSpec === null || rightSpec === null}
            onClick={() => {
              if (leftSpec !== null && rightSpec !== null) {
                setApplied({ left: leftSpec, right: rightSpec })
              }
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
              <p className="font-mono text-xs text-ink-3">
                {applied.left} → {applied.right}
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

interface SideSelectorProps {
  idPrefix: string
  label: string
  side: DiffSide
  onChange: (side: DiffSide) => void
  scopes: ConfigScopeSummary[]
  fileId: number
  file: ConfigFileDetail
}

/** 一侧描述选择：描述类型（层 head / 历史版本 / 有效结果）+ 对应的链 / 版本 / 目标控件 */
function SideSelector({ idPrefix, label, side, onChange, scopes, fileId, file }: SideSelectorProps) {
  const { t } = useTranslation()

  // version 描述：所选链的版本列表（新 → 旧）
  const [scopeLevel, scopeRefIdRaw] = side.scopeKey.split(':')
  const versionsQuery = useQuery({
    queryKey: ['configs', 'versions', fileId, side.scopeKey, 'diff-picker'],
    queryFn: () =>
      fetchConfigVersions(fileId, {
        scopeLevel: scopeLevel as ConfigScopeLevel,
        scopeRefId: Number(scopeRefIdRaw),
        page: 1,
        pageSize: 50,
      }),
    enabled: side.kind === 'version' && side.scopeKey !== '',
  })

  return (
    <div className="grid gap-2 rounded-xl border border-border p-3">
      <span className="text-[13px] font-semibold text-ink-1">{label}</span>
      <div className="space-y-1.5">
        <Label htmlFor={`${idPrefix}-kind`}>{t('delivery.configs.detail.diff.sideType')}</Label>
        <Select
          value={side.kind}
          onValueChange={(value) => {
            onChange({ ...EMPTY_SIDE, kind: value as DiffSide['kind'] })
          }}
        >
          <SelectTrigger
            id={`${idPrefix}-kind`}
            className="w-full"
            aria-label={`${label}${t('delivery.configs.detail.diff.sideType')}`}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {(['scope', 'version', 'effective'] as const).map((kind) => (
              <SelectItem key={kind} value={kind}>
                {t(`delivery.configs.detail.diff.sideTypes.${kind}`)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      {(side.kind === 'scope' || side.kind === 'version') && (
        <div className="space-y-1.5">
          <Label htmlFor={`${idPrefix}-scope`}>{t('delivery.configs.detail.diff.pickScope')}</Label>
          <Select
            value={side.scopeKey}
            onValueChange={(value) => {
              onChange({ ...side, scopeKey: value, versionId: null })
            }}
          >
            <SelectTrigger
              id={`${idPrefix}-scope`}
              className="w-full"
              aria-label={`${label}${t('delivery.configs.detail.diff.pickScope')}`}
            >
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {scopes.map((s) => (
                <SelectItem key={scopeSpec(s)} value={`${s.scopeLevel}:${String(s.scopeRefId)}`}>
                  {s.scopeLevel} / {s.scopeName}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      )}

      {side.kind === 'version' && side.scopeKey !== '' && (
        <div className="space-y-1.5">
          <Label htmlFor={`${idPrefix}-version`}>{t('delivery.configs.detail.diff.pickVersion')}</Label>
          <AsyncSection
            isLoading={versionsQuery.isLoading}
            isError={versionsQuery.isError}
            error={versionsQuery.error}
          >
            <Select
              value={side.versionId === null ? '' : String(side.versionId)}
              onValueChange={(value) => {
                onChange({ ...side, versionId: Number(value) })
              }}
            >
              <SelectTrigger
                id={`${idPrefix}-version`}
                className="w-full"
                aria-label={`${label}${t('delivery.configs.detail.diff.pickVersion')}`}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {(versionsQuery.data?.items ?? []).map((v) => (
                  <SelectItem key={v.versionId} value={String(v.versionId)}>
                    v{String(v.versionNo)} · {v.contentHash.slice(0, 8)}
                    {v.isRemoval ? ` · ${t('delivery.configs.detail.versions.removal')}` : ''}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </AsyncSection>
        </div>
      )}

      {side.kind === 'effective' && (
        <>
          <div className="space-y-1.5">
            <Label htmlFor={`${idPrefix}-target-type`}>
              {t('delivery.configs.detail.effective.targetTypeLabel')}
            </Label>
            <Select
              value={side.targetType}
              onValueChange={(value) => {
                onChange({ ...side, targetType: value as ConfigScopeLevel, targetRef: null })
              }}
            >
              <SelectTrigger
                id={`${idPrefix}-target-type`}
                className="w-full"
                aria-label={`${label}${t('delivery.configs.detail.effective.targetTypeLabel')}`}
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {SCOPE_LEVELS.map((level) => (
                  <SelectItem key={level} value={level}>
                    {t(`delivery.configs.detail.effective.targetTypes.${level}`)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          {side.targetType !== 'namespace' && (
            <div className="space-y-1.5">
              <Label htmlFor={`${idPrefix}-target-ref`}>{t('delivery.configs.scopePicker.pickLabel')}</Label>
              <ScopeRefPicker
                id={`${idPrefix}-target-ref`}
                namespaceId={file.namespaceId}
                level={side.targetType}
                value={side.targetRef}
                onChange={(ref) => {
                  onChange({ ...side, targetRef: ref })
                }}
              />
            </div>
          )}
        </>
      )}
    </div>
  )
}
