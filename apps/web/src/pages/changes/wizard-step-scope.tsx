// 向导第 4 步：交付范围与批次编排。范围四模式（全量 / 按大区 / 按小区 / 单服，复用
// selector 形状），候选列表统一带搜索即输即滤与 全选 / 反选 / 清空 + Shift 连选；
// 批次编排窗口：一次性全量 / 分批推进（推荐金丝雀一键应用 + 增删批次行 + 台数 / 百分比
// 单位 + 每批实际台数与累计覆盖 + 总和校验红字）。
import { useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Plus, Sparkles, X } from 'lucide-react'

import { Button, Checkbox, Input, cn } from '@beacon/ui'

import { fetchServers, fetchZoneTree } from '../../api/cluster'
import {
  batchIssue,
  planBatchRows,
  recommendedBatch,
  type WizardBatch,
  type WizardScope,
} from './wizard-state'

interface StepScopeProps {
  namespaceId: number
  scope: WizardScope
  onScopeChange: (scope: WizardScope) => void
  batch: WizardBatch
  onBatchChange: (batch: WizardBatch) => void
  // 按当前范围估算的目标台数（null = 结构树未就绪，未知）
  targetEstimate: number | null
}

const SCOPE_MODES: WizardScope['mode'][] = ['all', 'regions', 'zones', 'servers']

export default function WizardStepScope({
  namespaceId,
  scope,
  onScopeChange,
  batch,
  onBatchChange,
  targetEstimate,
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

      {/* 批次编排 */}
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
          <BatchPlanEditor batch={batch} onBatchChange={onBatchChange} targetEstimate={targetEstimate} />
        )}
        <p className="text-xs text-ink-3">{t('delivery.changes.wizard.scope.batchNote')}</p>
      </div>
    </div>
  )
}

// 批次编排窗口：单位切换（台数 / 百分比）、一键推荐、逐行编辑量值、
// 每行显示实际台数与累计覆盖，总和不符即时红字（父级同时据此禁「下一步」）。
function BatchPlanEditor({
  batch,
  onBatchChange,
  targetEstimate,
}: {
  batch: WizardBatch
  onBatchChange: (batch: WizardBatch) => void
  targetEstimate: number | null
}) {
  const { t } = useTranslation()
  const planRows = planBatchRows(batch, targetEstimate)
  const issue = batchIssue(batch, targetEstimate)
  const sum = batch.rows.reduce((acc, size) => acc + size, 0)

  const setRow = (index: number, raw: string): void => {
    const parsed = Number.parseInt(raw, 10)
    const rows = batch.rows.map((size, i) => (i === index ? (Number.isNaN(parsed) ? 0 : Math.max(0, parsed)) : size))
    onBatchChange({ ...batch, rows })
  }

  const removeRow = (index: number): void => {
    onBatchChange({ ...batch, rows: batch.rows.filter((_, i) => i !== index) })
  }

  // 增行默认补齐缺口（百分比补到 100、台数补到目标数），无缺口给最小量 1
  const addRow = (): void => {
    const gap = batch.unit === 'percent' ? 100 - sum : (targetEstimate ?? sum + 1) - sum
    onBatchChange({ ...batch, rows: [...batch.rows, Math.max(1, gap)] })
  }

  // 切单位重置为该单位下的推荐编排（数值跨单位无意义，不保留）
  const switchUnit = (unit: WizardBatch['unit']): void => {
    if (unit === batch.unit) {
      return
    }
    if (unit === 'percent') {
      onBatchChange({ mode: 'staged', unit: 'percent', rows: [10, 30, 60] })
      return
    }
    const recommended = recommendedBatch(targetEstimate)
    onBatchChange(
      recommended.unit === 'count' ? recommended : { mode: 'staged', unit: 'count', rows: [1] },
    )
  }

  const unitSuffix = batch.unit === 'percent' ? '%' : t('delivery.changes.wizard.scope.unitServers')

  return (
    <div className="grid gap-2 rounded-lg border border-border bg-surface-2/50 p-3">
      {/* 工具行：单位切换 + 一键推荐 + 目标数估算 */}
      <div className="flex flex-wrap items-center gap-2">
        <div className="flex overflow-hidden rounded-md border border-border" role="group" aria-label={t('delivery.changes.wizard.scope.unitLabel')}>
          {(['count', 'percent'] as const).map((unit) => (
            <button
              key={unit}
              type="button"
              aria-pressed={batch.unit === unit}
              onClick={() => {
                switchUnit(unit)
              }}
              className={cn(
                'px-2.5 py-1 text-xs transition-colors',
                batch.unit === unit ? 'bg-brand text-white' : 'bg-background text-ink-2 hover:bg-muted',
              )}
            >
              {t(`delivery.changes.wizard.scope.units.${unit}`)}
            </button>
          ))}
        </div>
        <Button
          size="sm"
          variant="outline"
          className="h-7"
          onClick={() => {
            onBatchChange(recommendedBatch(targetEstimate))
          }}
        >
          <Sparkles className="size-3.5" />
          {t('delivery.changes.wizard.scope.applyRecommended')}
        </Button>
        <span className="ml-auto text-xs text-ink-3">
          {targetEstimate === null
            ? t('delivery.changes.wizard.scope.estimateUnknown')
            : t('delivery.changes.wizard.scope.estimateTotal', { count: targetEstimate })}
        </span>
      </div>

      {/* 批次行 */}
      <ul className="grid gap-1.5">
        {batch.rows.map((size, index) => {
          const plan = planRows.at(index)
          return (
            <li key={index} className="flex flex-wrap items-center gap-2 text-sm">
              <span className="w-12 shrink-0 text-xs text-ink-3">
                {t('delivery.changes.wizard.scope.batchRowLabel', { no: index + 1 })}
              </span>
              <Input
                type="number"
                min={1}
                aria-label={t('delivery.changes.wizard.scope.batchRowInput', { no: index + 1 })}
                value={String(size)}
                onChange={(e) => {
                  setRow(index, e.target.value)
                }}
                className="h-8 w-20"
              />
              <span className="text-xs text-ink-2">{unitSuffix}</span>
              <span className="tnum text-xs text-ink-3">
                {plan?.count === null || plan?.count === undefined
                  ? t('delivery.changes.wizard.scope.batchRowUnknown')
                  : t('delivery.changes.wizard.scope.batchRowPlan', {
                      count: plan.count,
                      cumulative: plan.cumulative ?? plan.count,
                    })}
              </span>
              <Button
                size="sm"
                variant="ghost"
                className="ml-auto size-7 p-0"
                aria-label={t('delivery.changes.wizard.scope.removeBatch', { no: index + 1 })}
                disabled={batch.rows.length <= 1}
                onClick={() => {
                  removeRow(index)
                }}
              >
                <X className="size-3.5" />
              </Button>
            </li>
          )
        })}
      </ul>

      <div className="flex items-center gap-2">
        <Button size="sm" variant="ghost" className="h-7" onClick={addRow}>
          <Plus className="size-3.5" />
          {t('delivery.changes.wizard.scope.addBatch')}
        </Button>
      </div>

      {/* 总和校验红字 */}
      {issue !== null && (
        <p className="text-sm text-destructive" role="alert">
          {issue === 'invalid_row' && t('delivery.changes.wizard.scope.issue.invalidRow')}
          {issue === 'percent_sum' && t('delivery.changes.wizard.scope.issue.percentSum', { sum })}
          {issue === 'count_short' &&
            t('delivery.changes.wizard.scope.issue.countShort', { sum, diff: (targetEstimate ?? 0) - sum })}
          {issue === 'count_over' &&
            t('delivery.changes.wizard.scope.issue.countOver', { sum, diff: sum - (targetEstimate ?? 0) })}
        </p>
      )}
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
