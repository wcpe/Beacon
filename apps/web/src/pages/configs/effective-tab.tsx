// 有效配置 Tab：目标五选一（仅 namespace 基线 / bc_cluster / region / zone 假想目标 /
// server 真目标，server 走服务端搜索）→ GET effective。展示合并内容（敏感值已脱敏）、
// 有效哈希、逐键来源色块（五层五色 + 图例）、被删除的键（含执行删除层）。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { FileCheck } from 'lucide-react'

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
  cn,
} from '@beacon/ui'
import type { ConfigFileDetail, ConfigScopeLevel } from '@beacon/contracts'

import { fetchConfigEffective, type EffectiveQuery } from '../../api/delivery-configs'
import { LEVEL_CHIP, LEVEL_DOT, SCOPE_LEVELS } from './scope-levels'
import ScopeRefPicker, { type ScopeRef } from './scope-ref-picker'

interface EffectiveTabProps {
  fileId: number
  file: ConfigFileDetail
}

/** 由目标类型 + 实体组装 effective 查询参数（namespace 基线 = 空对象） */
function buildQuery(targetType: ConfigScopeLevel, ref: ScopeRef | null): EffectiveQuery {
  if (targetType === 'server' && ref !== null) {
    return { serverId: ref.name }
  }
  if (targetType === 'zone' && ref !== null) {
    return { zoneId: ref.id }
  }
  if (targetType === 'region' && ref !== null) {
    return { regionId: ref.id }
  }
  if (targetType === 'bc_cluster' && ref !== null) {
    return { bcClusterId: ref.id }
  }
  return {}
}

export default function EffectiveTab({ fileId, file }: EffectiveTabProps) {
  const { t } = useTranslation()
  // 草稿态：目标类型 + 实体；点「查询」后固化为 applied 触发请求
  const [targetType, setTargetType] = useState<ConfigScopeLevel>('namespace')
  const [pickedRef, setPickedRef] = useState<ScopeRef | null>(null)
  const [applied, setApplied] = useState<{ query: EffectiveQuery; label: string }>({ query: {}, label: '' })

  const query = useQuery({
    queryKey: ['configs', 'effective', fileId, applied.query],
    queryFn: () => fetchConfigEffective(fileId, applied.query),
  })

  const canRun = targetType === 'namespace' || pickedRef !== null

  return (
    <section className="grid gap-3">
      <SectionHeader
        icon={<FileCheck className="size-4" />}
        title={t('delivery.configs.detail.effective.title')}
      />

      <div className="flex flex-wrap items-end gap-2">
        <div className="space-y-1.5">
          <Label htmlFor="config-effective-target-type">
            {t('delivery.configs.detail.effective.targetTypeLabel')}
          </Label>
          <Select
            value={targetType}
            onValueChange={(value) => {
              setTargetType(value as ConfigScopeLevel)
              setPickedRef(null)
            }}
          >
            <SelectTrigger
              id="config-effective-target-type"
              className="w-56"
              aria-label={t('delivery.configs.detail.effective.targetTypeLabel')}
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
        {targetType !== 'namespace' && (
          <div className="w-64 space-y-1.5">
            <Label htmlFor="config-effective-target-ref">
              {t('delivery.configs.scopePicker.pickLabel')}
            </Label>
            <ScopeRefPicker
              id="config-effective-target-ref"
              namespaceId={file.namespaceId}
              level={targetType}
              value={pickedRef}
              onChange={setPickedRef}
            />
          </div>
        )}
        <Button
          variant="outline"
          disabled={!canRun}
          onClick={() => {
            setApplied({
              query: buildQuery(targetType, pickedRef),
              label:
                targetType === 'namespace'
                  ? t('delivery.configs.detail.effective.targetTypes.namespace')
                  : `${t(`delivery.configs.detail.scopes.levels.${targetType}`)} / ${pickedRef?.name ?? ''}`,
            })
          }}
        >
          {t('delivery.configs.detail.effective.run')}
        </Button>
      </div>

      <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
        {query.data && (
          <div className="grid gap-4">
            <p className="tnum text-sm text-ink-3">
              {applied.label !== '' && (
                <span className="mr-3">
                  {t('delivery.configs.detail.effective.current', { label: applied.label })}
                </span>
              )}
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
              {/* 五层五色图例（与作用域分组同色） */}
              <div className="flex flex-wrap items-center gap-3 text-[11px] text-ink-3">
                <span>{t('delivery.configs.detail.effective.legend')}</span>
                {SCOPE_LEVELS.map((level) => (
                  <span key={level} className="flex items-center gap-1">
                    <span className={cn('size-2 rounded-full', LEVEL_DOT[level])} aria-hidden />
                    {t(`delivery.configs.detail.scopes.levels.${level}`)}
                  </span>
                ))}
              </div>
              {query.data.provenance.length === 0 ? (
                <p className="text-sm text-ink-3">-</p>
              ) : (
                <div className="rounded-xl border border-border">
                  {query.data.provenance.map((entry) => (
                    <div
                      key={entry.path}
                      className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-1.5 last:border-b-0"
                    >
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink-1">{entry.path}</span>
                      <span
                        className={cn(
                          'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-[11px]',
                          LEVEL_CHIP[entry.scopeLevel],
                        )}
                      >
                        {t(`delivery.configs.detail.scopes.levels.${entry.scopeLevel}`)} / {entry.scopeName} · v
                        {String(entry.versionNo)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>

            <div className="grid gap-1.5">
              <SectionHeader title={t('delivery.configs.detail.effective.deletedKeys')} />
              {query.data.deletedKeys.length === 0 ? (
                <p className="text-sm text-ink-3">
                  {t('delivery.configs.detail.effective.noDeleted')}
                </p>
              ) : (
                <div className="rounded-xl border border-border">
                  {query.data.deletedKeys.map((k) => (
                    <div
                      key={`${k.path}:${k.scopeLevel}:${String(k.scopeRefId)}`}
                      className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-1.5 last:border-b-0"
                    >
                      <span className="min-w-0 flex-1 truncate font-mono text-xs text-ink-1 line-through">
                        {k.path}
                      </span>
                      <span className="text-[11px] text-ink-4">
                        {t('delivery.configs.detail.effective.deletedAt')}
                      </span>
                      <span
                        className={cn(
                          'inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 font-mono text-[11px]',
                          LEVEL_CHIP[k.scopeLevel],
                        )}
                      >
                        {t(`delivery.configs.detail.scopes.levels.${k.scopeLevel}`)} / {k.scopeName} · v
                        {String(k.versionNo)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
        )}
      </AsyncSection>
    </section>
  )
}
