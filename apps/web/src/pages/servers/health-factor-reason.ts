// 健康因子「不适用原因」推导（FR-228 §4）。
//
// 背景：控制面只下发 `applicable: bool`，不区分成因；而运维看到统一的「不适用」无法判断
// 是「该角色本就没有这个指标」还是「agent 太旧 / 没采到值」——前者无需求动作，后者要排查。
//
// 本模块按 FR-32 §4.4 的因子适用矩阵 + 后端 health_calc.go 的 applicable 判定，用前端已持有的
// `kind` 与因子名反推成因（纯函数，不改契约、不改后端）：
//   - tps      ：仅 backend 适用           → proxy 不适用 = 角色不适用
//   - capacity ：仅 backend 适用；backend 下 `maxOnline<=0` 才不适用 = 未上报
//   - conn     ：仅 proxy 适用             → backend 不适用 = 角色不适用
//   - latency  ：两者皆适用，`rtt<0` 不可得 = 数据不可得
//   - cpu      ：两者皆适用，`cpuPct<0`（窗口全毛刺）= 数据不可得
//   - alert    ：恒适用，不会走到这里

/** 因子不适用的成因：角色本就不适用 / 数据未上报或不可得。 */
export type FactorInapplicableReason = 'role' | 'missing'

/** 因子名（与后端 healthview.Factor.Factor 逐字一致）。 */
const FACTOR_TPS = 'tps'
const FACTOR_CAPACITY = 'capacity'
const FACTOR_CONN = 'conn'

/**
 * 推导某因子「为何不适用」。仅在该因子 `applicable === false` 时有意义。
 *
 * @param factorName 因子名（如 `capacity`）
 * @param kind 服务器角色（`backend` / `proxy`）
 */
export function inapplicableReasonOf(factorName: string, kind: string): FactorInapplicableReason {
  const isBackend = kind === 'backend'
  switch (factorName) {
    // 仅 backend 适用 → proxy 上是角色不适用；backend 上 tps 恒适用，不会不适用
    case FACTOR_TPS:
      return 'role'
    // 仅 proxy 适用 → backend 上是角色不适用；proxy 上 conn 恒适用（connSoftLimit 校验 >0）
    case FACTOR_CONN:
      return 'role'
    // 仅 backend 适用；backend 不为「未上报」以外的原因不适用（maxOnline<=0）
    case FACTOR_CAPACITY:
      return isBackend ? 'missing' : 'role'
    // latency / cpu 两者皆适用，不适用只可能是数据不可得
    default:
      return 'missing'
  }
}
