// 引导创建向导的共享类型与纯函数：步骤编排、范围 / 批次到契约字段的换算。
// 状态本体放在 guided-wizard.tsx，本文件不含任何 React 依赖，便于单测与复用。

import type { ChangeSelector, ConfigChangeInput } from '../../api/delivery-changes'

/** 交付内容类型：决定向导保留哪些步骤 */
export type WizardContent = 'files' | 'configs' | 'both'

/** 向导步骤 id（画布顺序固定，纯配置跳过 source、纯文件跳过 config） */
export type WizardStepId = 'content' | 'source' | 'config' | 'scope' | 'review'

/** 画布上的全部步骤（步骤条始终按此顺序展示 1-5） */
export const WIZARD_STEPS: WizardStepId[] = ['content', 'source', 'config', 'scope', 'review']

/** 第 3 步选中的配置变更（已解析出目标版本） */
export interface WizardConfigPick {
  fileId: number
  fileName: string
  format: string
  scopeKind: string
  scopeId: number
  scopeName: string
  fromVersionId: number | null
  fromVersionNo: number | null
  toVersionId: number
  toVersionNo: number
}

/** 第 4 步：交付范围（复用 selector 形状的单模式简化） */
export interface WizardScope {
  mode: 'all' | 'regions' | 'zones' | 'servers'
  regions: number[]
  zones: number[]
  servers: string[]
}

/** 第 4 步：批次编排（一次性全量 / 分批推进；分批按台数或百分比逐批给量） */
export interface WizardBatch {
  mode: 'single' | 'staged'
  unit: 'percent' | 'count'
  rows: number[]
}

/** 批次编排校验结论：null = 通过 */
export type BatchIssue = 'invalid_row' | 'percent_sum' | 'count_short' | 'count_over' | null

/** 批次划分预览行：size 为用户输入量，count / cumulative 为按目标数换算的实际台数 */
export interface BatchPlanRow {
  batchNo: number
  size: number
  count: number | null
  cumulative: number | null
}

/** zone-tree 拍扁后的目标数估算条目 */
export interface ZoneCountEntry {
  zoneId: number
  regionId: number
  serverCount: number
}

/** 估算所需的最小结构树形状（与 ZoneTreeResponse 结构兼容） */
interface ZoneTreeLike {
  clusters: { regions: { id: number; zones: { id: number; serverCount: number }[] }[] }[]
}

/** 内容类型是否含文件载荷 */
export function includesFiles(content: WizardContent): boolean {
  return content === 'files' || content === 'both'
}

/** 内容类型是否含配置载荷 */
export function includesConfigs(content: WizardContent): boolean {
  return content === 'configs' || content === 'both'
}

/** 当前内容类型下实际要走的步骤序列 */
export function activeSteps(content: WizardContent): WizardStepId[] {
  return WIZARD_STEPS.filter((step) => {
    if (step === 'source') {
      return includesFiles(content)
    }
    if (step === 'config') {
      return includesConfigs(content)
    }
    return true
  })
}

/** 范围 → selector JSON（§4.3.1 形状；单模式：其余维度置空） */
export function buildSelector(scope: WizardScope): ChangeSelector {
  return {
    all: scope.mode === 'all',
    regions: scope.mode === 'regions' ? scope.regions : [],
    zones: scope.mode === 'zones' ? scope.zones : [],
    servers: scope.mode === 'servers' ? scope.servers : [],
    excludes: [],
  }
}

/** 批次编排 → batchMode/batchSizes：一次性 = percent[100]；分批 = 按单位原样下发编排行 */
export function buildBatch(batch: WizardBatch): { batchMode: 'percent' | 'count'; batchSizes: number[] } {
  if (batch.mode === 'single') {
    return { batchMode: 'percent', batchSizes: [100] }
  }
  return { batchMode: batch.unit, batchSizes: batch.rows.map((size) => Math.max(1, Math.floor(size))) }
}

/** 结构树拍扁为小区计数条目（树未加载返回 undefined，表示目标数未知） */
export function flattenZoneCounts(tree: ZoneTreeLike | undefined): ZoneCountEntry[] | undefined {
  if (tree === undefined) {
    return undefined
  }
  return tree.clusters.flatMap((cluster) =>
    cluster.regions.flatMap((region) =>
      region.zones.map((zone) => ({ zoneId: zone.id, regionId: region.id, serverCount: zone.serverCount })),
    ),
  )
}

/** 按当前范围估算目标台数（单服模式恒为已选数；其余模式树未加载时返回 null 未知） */
export function estimateTargetTotal(scope: WizardScope, zones: ZoneCountEntry[] | undefined): number | null {
  if (scope.mode === 'servers') {
    return scope.servers.length
  }
  if (zones === undefined) {
    return null
  }
  const sum = (entries: ZoneCountEntry[]): number => entries.reduce((acc, entry) => acc + entry.serverCount, 0)
  if (scope.mode === 'all') {
    return sum(zones)
  }
  if (scope.mode === 'regions') {
    return sum(zones.filter((zone) => scope.regions.includes(zone.regionId)))
  }
  return sum(zones.filter((zone) => scope.zones.includes(zone.zoneId)))
}

/** 推荐批次（金丝雀节奏）：目标数未知按 10% / 30% / 60%；已知 ≤5 台均分，>5 台首批约一成、次批三成、末批余量 */
export function recommendedBatch(targetTotal: number | null): WizardBatch {
  if (targetTotal === null || targetTotal <= 0) {
    return { mode: 'staged', unit: 'percent', rows: [10, 30, 60] }
  }
  if (targetTotal === 1) {
    return { mode: 'staged', unit: 'count', rows: [1] }
  }
  if (targetTotal <= 5) {
    const first = Math.floor(targetTotal / 2)
    return { mode: 'staged', unit: 'count', rows: [first, targetTotal - first] }
  }
  const first = Math.max(1, Math.ceil(targetTotal * 0.1))
  const second = Math.ceil(targetTotal * 0.3)
  return { mode: 'staged', unit: 'count', rows: [first, second, targetTotal - first - second] }
}

/** 校验批次编排：行必须为 ≥1 整数；百分比合计须 =100；台数在目标数已知时合计须 = 目标数 */
export function batchIssue(batch: WizardBatch, targetTotal: number | null): BatchIssue {
  if (batch.mode === 'single') {
    return null
  }
  if (batch.rows.length === 0 || batch.rows.some((size) => !Number.isInteger(size) || size < 1)) {
    return 'invalid_row'
  }
  const sum = batch.rows.reduce((acc, size) => acc + size, 0)
  if (batch.unit === 'percent') {
    return sum === 100 ? null : 'percent_sum'
  }
  if (targetTotal === null || targetTotal <= 0) {
    // 目标数未知（树未加载）或为 0（范围校验兜底）时不卡台数总和
    return null
  }
  if (sum < targetTotal) {
    return 'count_short'
  }
  if (sum > targetTotal) {
    return 'count_over'
  }
  return null
}

/** 批次划分预览：与控制面语义一致——percent 逐批向上取整、count 逐批固定台数，均不超过剩余 */
export function planBatchRows(batch: WizardBatch, targetTotal: number | null): BatchPlanRow[] {
  const unit = batch.mode === 'single' ? 'percent' : batch.unit
  const rows = batch.mode === 'single' ? [100] : batch.rows
  // 目标数未知：台数单位仍可直显输入量并累计，百分比无法换算
  if (targetTotal === null) {
    let cumulative = 0
    return rows.map((size, index) => {
      if (unit === 'count') {
        cumulative += Math.max(0, size)
        return { batchNo: index + 1, size, count: Math.max(0, size), cumulative }
      }
      return { batchNo: index + 1, size, count: null, cumulative: null }
    })
  }
  let remaining = targetTotal
  let cumulative = 0
  return rows.map((size, index) => {
    const raw = unit === 'percent' ? Math.ceil((targetTotal * size) / 100) : size
    const count = Math.max(0, Math.min(raw, remaining))
    remaining -= count
    cumulative += count
    return { batchNo: index + 1, size, count, cumulative }
  })
}

/** 范围是否已选出目标（非全量模式必须至少选一项） */
export function scopeReady(scope: WizardScope): boolean {
  switch (scope.mode) {
    case 'all':
      return true
    case 'regions':
      return scope.regions.length > 0
    case 'zones':
      return scope.zones.length > 0
    case 'servers':
      return scope.servers.length > 0
  }
}

/** 配置选择 → PATCH configChanges 输入 */
export function toConfigChanges(picks: WizardConfigPick[]): ConfigChangeInput[] {
  return picks.map((pick) => ({
    configScopeKind: pick.scopeKind,
    configScopeId: pick.scopeId,
    configFromVersionId: pick.fromVersionId,
    configToVersionId: pick.toVersionId,
  }))
}
