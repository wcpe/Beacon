// 反向抓取受管任务进度纯逻辑层（FR-87，见 ADR-0037 spec 扩展）：
// 据任务状态 + 已用时长（elapsedSec）判定是否「疑似 agent 未响应」、格式化人类可读时长。
// 组件层只管渲染，判定逻辑全在这里——无副作用、可穷举单测。

// 非终态集合：仍可能因 agent 未响应而卡住的状态（终态不再卡）。
const ACTIVE_STATUS = new Set([
  'scanning',
  'pending-review',
  'fetching',
  'conflict-review',
  'ingesting',
])

// 卡死警示阈值（秒）：任务处非终态且已用时长超此值 → 标「疑似 agent 未响应」。
// 取值偏保守（120s）：scan/submit 读盘 + 回传通常秒级完成，停留两分钟以上多半是 agent 未响应 / 已掉线。
export const STUCK_THRESHOLD_SEC = 120

// isTaskActive 判断任务是否处非终态（仍在等 agent / 人工推进）。
export function isTaskActive(status: string): boolean {
  return ACTIVE_STATUS.has(status)
}

// isTaskStuck 判断任务是否疑似卡死：非终态 + 已用时长超阈值。
// pending-review / conflict-review 也算非终态，但这两态是「等人工」而非「等 agent」——
// 仍纳入警示：长时间无人处理同样值得运维注意（文案为通用「疑似未响应」）。
export function isTaskStuck(status: string, elapsedSec: number): boolean {
  return isTaskActive(status) && elapsedSec >= STUCK_THRESHOLD_SEC
}

// formatElapsed 把秒数格式化为人类可读时长：< 60s 显「Ns」、< 3600s 显「Nm」、否则「Nh Mm」。
// 负值归零（防御控制面与浏览器时钟漂移）。
export function formatElapsed(sec: number): string {
  const s = Math.max(0, Math.floor(sec))
  if (s < 60) return `${s}s`
  if (s < 3600) {
    const m = Math.floor(s / 60)
    return `${m}m`
  }
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  return m > 0 ? `${h}h ${m}m` : `${h}h`
}
