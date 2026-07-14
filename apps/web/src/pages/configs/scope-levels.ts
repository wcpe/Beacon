// 五层作用域的顺序与配色 token（图表分类色板 chart-1..chart-5）。
// 作用域概览的层分组、有效预览的逐键来源色块与图例共用，保证同层同色。
import type { ConfigScopeLevel } from '@beacon/contracts'

/** 覆盖链固定顺序：低 → 高 */
export const SCOPE_LEVELS: readonly ConfigScopeLevel[] = ['namespace', 'bc_cluster', 'region', 'zone', 'server']

/** 层色点（图例 / 分组头） */
export const LEVEL_DOT: Record<ConfigScopeLevel, string> = {
  namespace: 'bg-chart-1',
  bc_cluster: 'bg-chart-2',
  region: 'bg-chart-3',
  zone: 'bg-chart-4',
  server: 'bg-chart-5',
}

/** 层色块（provenance 行内 chip：浅底 + 同色描边与文字） */
export const LEVEL_CHIP: Record<ConfigScopeLevel, string> = {
  namespace: 'border-chart-1/40 bg-chart-1/10 text-chart-1',
  bc_cluster: 'border-chart-2/40 bg-chart-2/10 text-chart-2',
  region: 'border-chart-3/40 bg-chart-3/10 text-chart-3',
  zone: 'border-chart-4/40 bg-chart-4/10 text-chart-4',
  server: 'border-chart-5/40 bg-chart-5/10 text-chart-5',
}
