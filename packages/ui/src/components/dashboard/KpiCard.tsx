// KPI 卡（KpiCard）：对齐 B 版运维总览顶部指标卡——右上图标角标（浅底方块）+ 弱标签 +
// 26px 大数值（等宽）+ 可选趋势 / 副说明 + 可选底部细进度条或自定义可视化槽。
// 纯展示组件；数值与占比由父组件按真实字段算好传入，不含取数逻辑。

import type { ReactNode } from 'react'
import { cn } from '../../lib/utils'

// 图标角标语义色调：品牌靛蓝（默认）/ 正常 / 注意 / 危急 / 次要，映射到浅底 + 同色图标。
export type KpiTone = 'brand' | 'ok' | 'warn' | 'crit' | 'off'

// 趋势方向：up 向好（绿）/ down 变差（红）/ flat 持平（弱色）。仅决定趋势文字颜色与语义，不判断"高低是否是好"。
export type KpiTrend = 'up' | 'down' | 'flat'

interface KpiCardProps {
  // 指标标签（弱色小字）
  label: string
  // 主数值（已格式化文案；数字部分建议纯数字，单位走 unit）
  value: ReactNode
  // 数值后缀 / 单位（小一号弱色，如「/ 19 台」）
  unit?: ReactNode
  // 右上角图标（lucide）
  icon: ReactNode
  // 图标角标色调（缺省 brand）
  tone?: KpiTone
  // 底部说明行（趋势 + 文案），可选
  meta?: ReactNode
  // 底部可视化槽（细进度条 / sparkline 等），可选；给了则渲染在 meta 上方
  visual?: ReactNode
}

// 色调 → 图标角标类（浅底 + 同色图标）。
const TONE_CLASS: Record<KpiTone, string> = {
  brand: 'bg-brand-50 text-brand',
  ok: 'bg-ok-bg text-ok',
  warn: 'bg-warn-bg text-warn',
  crit: 'bg-crit-bg text-crit',
  off: 'bg-off-bg text-off',
}

export default function KpiCard({ label, value, unit, icon, tone = 'brand', meta, visual }: KpiCardProps) {
  return (
    <div className="flex flex-col gap-2 rounded-xl border border-border bg-card p-[15px] shadow-card">
      <div className="flex items-center justify-between">
        <span className="text-[11.5px] font-medium text-ink-3">{label}</span>
        <span className={cn('grid size-7 place-items-center rounded-lg', TONE_CLASS[tone])} aria-hidden>
          {icon}
        </span>
      </div>
      <div className="text-[26px] leading-none font-bold tracking-[-0.5px] text-ink-1 tnum">
        {value}
        {unit != null && <span className="text-[14px] font-semibold tracking-normal text-ink-4">{unit}</span>}
      </div>
      {visual}
      {meta != null && <div className="flex items-center gap-1.5 text-[11px] text-ink-3">{meta}</div>}
    </div>
  )
}
