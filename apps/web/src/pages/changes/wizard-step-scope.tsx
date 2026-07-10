// 向导第 4 步：交付范围与批次策略。范围四模式（全量 / 按大区 / 按小区 / 单服，复用
// selector 形状），候选列表统一带搜索即输即滤与 全选 / 反选 / 清空 + Shift 连选；
// 批次两模式（一次性 / 分批推进 + 每批台数），说明文案解释推进门与熔断。
import { useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button, Checkbox, Input, Label, cn } from '@beacon/ui'

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
  // 候选搜索关键字（切换范围模式时清空，避免带着上个模式的过滤看新列表）
  const [pickSearch, setPickSearch] = useState('')

  // 大区 / 小区候选来自结构树；子服候选服务端过滤
  const treeQuery = useQuery({
    queryKey: ['change-orders', 'wizard-zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    enabled: scope.mode === 'regions' || scope.mode === 'zones',
  })
  const serversQuery = useQuery({
    queryKey: ['change-orders', 'wizard-scope-servers', namespaceId, pickSearch],
    queryFn: () =>
      fetchServers({
        namespaceId,
        kind: 'backend',
        assigned: true,
        keyword: pickSearch.trim() === '' ? undefined : pickSearch.trim(),
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

  // 大区 / 小区在客户端按名称即输即滤（子服由服务端 keyword 过滤）
  const needle = pickSearch.trim().toLowerCase()
  const filteredRegions = needle === '' ? regions : regions.filter((r) => r.name.toLowerCase().includes(needle))
  const filteredZones = needle === '' ? zones : zones.filter((z) => z.name.toLowerCase().includes(needle))

  const handleModeChange = (mode: WizardScope['mode']): void => {
    setPickSearch('')
    onScopeChange({ ...scope, mode })
  }

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
                handleModeChange(mode)
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
          items={filteredRegions.map((r) => ({ key: String(r.id), label: r.name, extra: t('delivery.changes.wizard.scope.regionZones', { count: r.zoneCount }) }))}
          selectedKeys={scope.regions.map(String)}
          onSelectedChange={(keys) => {
            onScopeChange({ ...scope, regions: keys.map(Number) })
          }}
          searchValue={pickSearch}
          onSearchChange={setPickSearch}
        />
      )}
      {scope.mode === 'zones' && (
        <PickList
          label={t('delivery.changes.wizard.scope.pickZones')}
          emptyText={t('delivery.changes.wizard.scope.pickEmpty')}
          items={filteredZones.map((z) => ({ key: String(z.id), label: z.name }))}
          selectedKeys={scope.zones.map(String)}
          onSelectedChange={(keys) => {
            onScopeChange({ ...scope, zones: keys.map(Number) })
          }}
          searchValue={pickSearch}
          onSearchChange={setPickSearch}
        />
      )}
      {scope.mode === 'servers' && (
        <PickList
          label={t('delivery.changes.wizard.scope.pickServers')}
          emptyText={t('delivery.changes.wizard.scope.pickEmpty')}
          items={(serversQuery.data?.items ?? []).map((s) => ({
            key: s.serverId,
            label: s.serverId,
            extra: s.zoneName ?? undefined,
          }))}
          selectedKeys={scope.servers}
          onSelectedChange={(keys) => {
            onScopeChange({ ...scope, servers: keys })
          }}
          searchValue={pickSearch}
          onSearchChange={setPickSearch}
        />
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

// 带搜索与批量操作的复选列表（大区 / 小区 / 子服共用）：
// 搜索即输即滤（items 已由父级过滤）、全选 / 反选作用于当前可见项、清空清掉全部已选、
// 按住 Shift 点击按行下标连选区间。
function PickList({
  label,
  emptyText,
  items,
  selectedKeys,
  onSelectedChange,
  searchValue,
  onSearchChange,
}: {
  label: string
  emptyText: string
  items: { key: string; label: string; extra?: string }[]
  selectedKeys: string[]
  onSelectedChange: (next: string[]) => void
  searchValue: string
  onSearchChange: (next: string) => void
}) {
  const { t } = useTranslation()
  // Shift 连选：记录上一次点选的行下标与本次点击是否按住 Shift
  const lastIndexRef = useRef<number | null>(null)
  const shiftKeyRef = useRef(false)

  const selected = new Set(selectedKeys)

  // 应用一组键的选中状态（保持既有顺序：新增追加、取消过滤）
  const applyKeys = (keys: string[], next: boolean): void => {
    if (next) {
      const merged = [...selectedKeys]
      for (const key of keys) {
        if (!selected.has(key)) {
          merged.push(key)
        }
      }
      onSelectedChange(merged)
    } else {
      const removal = new Set(keys)
      onSelectedChange(selectedKeys.filter((key) => !removal.has(key)))
    }
  }

  const handleToggle = (index: number, next: boolean): void => {
    const anchor = shiftKeyRef.current ? (lastIndexRef.current ?? index) : index
    const [start, end] = anchor <= index ? [anchor, index] : [index, anchor]
    lastIndexRef.current = index
    applyKeys(items.slice(start, end + 1).map((item) => item.key), next)
  }

  // 反选仅作用于当前可见项：可见已选 → 取消，可见未选 → 选中（不可见项保持）
  const handleInvert = (): void => {
    const invisible = selectedKeys.filter((key) => !items.some((item) => item.key === key))
    const nowPicked = items.filter((item) => !selected.has(item.key)).map((item) => item.key)
    onSelectedChange([...invisible, ...nowPicked])
  }

  return (
    <div className="grid gap-1.5">
      <span className="text-xs text-ink-3">{label}</span>
      {/* 操作条：搜索 + 全选 / 反选 / 清空 + 已选计数 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.changes.wizard.scope.pickFilter')}
          placeholder={t('delivery.changes.wizard.scope.pickFilterPlaceholder')}
          value={searchValue}
          onChange={(e) => {
            onSearchChange(e.target.value)
          }}
          className="h-8 w-48"
        />
        <Button
          size="sm"
          variant="outline"
          className="h-8"
          disabled={items.length === 0}
          onClick={() => {
            applyKeys(items.map((item) => item.key), true)
          }}
        >
          {t('delivery.changes.wizard.scope.selectAll')}
        </Button>
        <Button size="sm" variant="outline" className="h-8" disabled={items.length === 0} onClick={handleInvert}>
          {t('delivery.changes.wizard.scope.invert')}
        </Button>
        <Button
          size="sm"
          variant="ghost"
          className="h-8"
          disabled={selectedKeys.length === 0}
          onClick={() => {
            onSelectedChange([])
          }}
        >
          {t('delivery.changes.wizard.scope.clear')}
        </Button>
        <span className="ml-auto text-xs text-ink-3">
          {t('delivery.changes.wizard.scope.selectedCount', { count: selectedKeys.length })}
        </span>
      </div>
      {items.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-sm text-muted-foreground">
          {emptyText}
        </p>
      ) : (
        <ul className="grid max-h-44 gap-0.5 overflow-y-auto rounded-lg border border-border p-1.5 sm:grid-cols-2">
          {items.map((item, index) => (
            <li
              key={item.key}
              // 在捕获阶段记录是否按住 Shift（Radix Checkbox 的 onCheckedChange 不带原生事件）
              onClickCapture={(event) => {
                shiftKeyRef.current = event.shiftKey
              }}
            >
              <label className="flex cursor-pointer items-center gap-2 rounded-md px-2 py-1 text-sm hover:bg-muted">
                <Checkbox
                  aria-label={item.label}
                  checked={selected.has(item.key)}
                  onCheckedChange={(next) => {
                    handleToggle(index, next === true)
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
