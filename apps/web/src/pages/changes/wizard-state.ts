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

/** 第 4 步：批次策略（一次性 / 分批推进 + 每批台数） */
export interface WizardBatch {
  mode: 'single' | 'staged'
  perBatch: number
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

/** 批次策略 → batchMode/batchSizes：一次性 = percent[100]；分批 = count[n,n,n]（剩余进末批） */
export function buildBatch(batch: WizardBatch): { batchMode: 'percent' | 'count'; batchSizes: number[] } {
  if (batch.mode === 'single') {
    return { batchMode: 'percent', batchSizes: [100] }
  }
  const per = Math.max(1, Math.floor(batch.perBatch))
  return { batchMode: 'count', batchSizes: [per, per, per] }
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
