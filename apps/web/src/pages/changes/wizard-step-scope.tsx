// 向导第 4 步：交付范围与批次策略。范围四模式（全量 / 按大区 / 按小区 / 单服，复用
// selector 形状），批次两模式（一次性 / 分批推进 + 每批台数），说明文案解释推进门与熔断。
import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Checkbox, Input, Label, cn } from '@beacon/ui'

import { fetchServers, fetchZoneTree } from '../../api/cluster'
import type { WizardBatch, WizardScope } from './wizard-state'

interface StepScopeProps {
  namespaceId: number
  scope: WizardScope
  onScopeChange: (scope: WizardScope) => void
  batch: WizardBatch
  onBatchChange: (batch: WizardBatch) => void
}

const SCOPE_MODES: WizardScope['mode'][] = ['all', 'regions', 'zones', 'servers']

export default function WizardStepScope({
  namespaceId,
  scope,
  onScopeChange,
  batch,
  onBatchChange,
}: StepScopeProps) {
  const { t } = useTranslation()
  const [serverKeyword, setServerKeyword] = useState('')

  // 大区 / 小区候选来自结构树；子服候选服务端过滤
  const treeQuery = useQuery({
    queryKey: ['change-orders', 'wizard-zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    enabled: scope.mode === 'regions' || scope.mode === 'zones',
  })
  const serversQuery = useQuery({
    queryKey: ['change-orders', 'wizard-scope-servers', namespaceId, serverKeyword],
    queryFn: () =>
      fetchServers({
        namespaceId,
        kind: 'backend',
        assigned: true,
        keyword: serverKeyword.trim() === '' ? undefined : serverKeyword.trim(),
        pageSize: 50,
      }),
    enabled: scope.mode === 'servers',
  })

  const regions = useMemo(
    () => (treeQuery.data?.clusters ?? []).flatMap((c) => c.regions.map((r) => ({ id: r.id, name: r.name, zoneCount: r.zones.length }))),
    [treeQuery.data],
  )
  const zones = useMemo(
    () =>
      (treeQuery.data?.clusters ?? []).flatMap((c) =>
        c.regions.flatMap((r) => r.zones.map((z) => ({ id: z.id, name: `${r.name} / ${z.name}`, serverCount: z.serverCount }))),
      ),
    [treeQuery.data],
  )

  return (
    <div className="grid gap-4">
      <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.scope.lead')}</p>

      {/* 范围模式 */}
      <div className="grid gap-2">
        <span className="text-[13px] font-semibold text-ink-2">{t('delivery.changes.wizard.scope.modeLabel')}</span>
        <div className="grid gap-2 sm:grid-cols-4">
          {SCOPE_MODES.map((mode) => (
            <ModeCard
              key={mode}
              title={t(`delivery.changes.wizard.scope.modes.${mode}`)}
              hint={t(`delivery.changes.wizard.scope.modeHints.${mode}`)}
              selected={scope.mode === mode}
              onClick={() => {
                onScopeChange({ ...scope, mode })
              }}
            />
          ))}
        </div>
      </div>

      {/* 按模式出现的多选列表 */}
      {scope.mode === 'regions' && (
        <PickList
          label={t('delivery.changes.wizard.scope.pickRegions')}
          emptyText={t('delivery.changes.wizard.scope.pickEmpty')}
          items={regions.map((r) => ({ key: String(r.id), label: r.name, extra: t('delivery.changes.wizard.scope.regionZones', { count: r.zoneCount }) }))}
          checked={(key) => scope.regions.includes(Number(key))}
          onToggle={(key, next) => {
            onScopeChange({ ...scope, regions: toggle(scope.regions, Number(key), next) })
          }}
          selectedCount={scope.regions.length}
        />
      )}
      {scope.mode === 'zones' && (
        <PickList
          label={t('delivery.changes.wizard.scope.pickZones')}
          emptyText={t('delivery.changes.wizard.scope.pickEmpty')}
          items={zones.map((z) => ({ key: String(z.id), label: z.name }))}
          checked={(key) => scope.zones.includes(Number(key))}
          onToggle={(key, next) => {
            onScopeChange({ ...scope, zones: toggle(scope.zones, Number(key), next) })
          }}
          selectedCount={scope.zones.length}
        />
      )}
      {scope.mode === 'servers' && (
        <div className="grid gap-2">
          <Input
            aria-label={t('delivery.changes.wizard.scope.serverFilter')}
            placeholder={t('delivery.changes.wizard.scope.serverFilter')}
            value={serverKeyword}
            onChange={(e) => {
              setServerKeyword(e.target.value)
            }}
            className="w-56"
          />
          <PickList
            label={t('delivery.changes.wizard.scope.pickServers')}
            emptyText={t('delivery.changes.wizard.scope.pickEmpty')}
            items={(serversQuery.data?.items ?? []).map((s) => ({
              key: s.serverId,
              label: s.serverId,
              extra: s.zoneName ?? undefined,
            }))}
            checked={(key) => scope.servers.includes(key)}
            onToggle={(key, next) => {
              onScopeChange({ ...scope, servers: toggle(scope.servers, key, next) })
            }}
            selectedCount={scope.servers.length}
          />
        </div>
      )}

      {/* 批次策略 */}
      <div className="grid gap-2">
        <span className="text-[13px] font-semibold text-ink-2">{t('delivery.changes.wizard.scope.batchLabel')}</span>
        <div className="grid gap-2 sm:grid-cols-2">
          <ModeCard
            title={t('delivery.changes.wizard.scope.batchSingle')}
            hint={t('delivery.changes.wizard.scope.batchSingleHint')}
            selected={batch.mode === 'single'}
            onClick={() => {
              onBatchChange({ ...batch, mode: 'single' })
            }}
          />
          <ModeCard
            title={t('delivery.changes.wizard.scope.batchStaged')}
            hint={t('delivery.changes.wizard.scope.batchStagedHint')}
            selected={batch.mode === 'staged'}
            onClick={() => {
              onBatchChange({ ...batch, mode: 'staged' })
            }}
          />
        </div>
        {batch.mode === 'staged' && (
          <div className="flex items-center gap-2">
            <Label htmlFor="wizard-per-batch">{t('delivery.changes.wizard.scope.perBatchLabel')}</Label>
            <Input
              id="wizard-per-batch"
              type="number"
              min={1}
              value={String(batch.perBatch)}
              onChange={(e) => {
                const parsed = Number.parseInt(e.target.value, 10)
                onBatchChange({ ...batch, perBatch: Number.isNaN(parsed) ? 1 : Math.max(1, parsed) })
              }}
              className="w-24"
            />
          </div>
        )}
        <p className="text-xs text-ink-3">{t('delivery.changes.wizard.scope.batchNote')}</p>
      </div>
    </div>
  )
}

// 数组内切换成员（保持原顺序）
function toggle<T>(list: T[], value: T, next: boolean): T[] {
  if (next) {
    return list.includes(value) ? list : [...list, value]
  }
  return list.filter((v) => v !== value)
}

// 可选中的说明卡（范围模式 / 批次模式共用）
function ModeCard({
  title,
  hint,
  selected,
  onClick,
}: {
  title: string
  hint: string
  selected: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      aria-pressed={selected}
      onClick={onClick}
      className={cn(
        'grid gap-0.5 rounded-lg border px-3 py-2 text-left transition-colors',
        selected ? 'border-brand bg-brand-50/60 ring-1 ring-brand' : 'border-border hover:border-brand-200',
      )}
    >
      <span className="text-[13px] font-semibold text-ink-1">{title}</span>
      <span className="text-xs text-ink-3">{hint}</span>
    </button>
  )
}

// 带计数的复选列表（大区 / 小区 / 子服共用）
function PickList({
  label,
  emptyText,
  items,
  checked,
  onToggle,
  selectedCount,
}: {
  label: string
  emptyText: string
  items: { key: string; label: string; extra?: string }[]
  checked: (key: string) => boolean
  onToggle: (key: string, next: boolean) => void
  selectedCount: number
}) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-1.5">
      <span className="flex items-center justify-between text-xs text-ink-3">
        {label}
        <span>{t('delivery.changes.wizard.scope.selectedCount', { count: selectedCount })}</span>
      </span>
      {items.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-sm text-muted-foreground">
          {emptyText}
        </p>
      ) : (
        <ul className="grid max-h-44 gap-0.5 overflow-y-auto rounded-lg border border-border p-1.5 sm:grid-cols-2">
          {items.map((item) => (
            <li key={item.key}>
              <label className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1 text-sm hover:bg-muted">
                <Checkbox
                  aria-label={item.label}
                  checked={checked(item.key)}
                  onCheckedChange={(next) => {
                    onToggle(item.key, next === true)
                  }}
                />
                <span className="min-w-0 flex-1 truncate">{item.label}</span>
                {item.extra !== undefined && <span className="shrink-0 text-xs text-ink-3">{item.extra}</span>}
              </label>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
