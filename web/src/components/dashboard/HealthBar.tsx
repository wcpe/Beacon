// 分段健康条（HealthBar）：把若干健康分段（在线/亚健康/失联/离线）按计数占比排成一条横条。
// 各段宽度 = 该段计数 / 总数，颜色按健康等级。一眼看清集群整体健康构成。
// 纯展示组件；分段与计数由父组件按真实字段算好传入。总数为 0 时显示空态轨道。

import { cn } from '@/lib/utils'
import { levelSolid, type HealthLevel } from './health'

// 单个健康分段：标签 + 计数 + 等级（决定颜色）。
export interface HealthSegment {
  // 分段标签（如「在线」/「失联」）
  label: string
  // 该段计数
  count: number
  // 健康等级（决定段色）
  level: HealthLevel
}

interface HealthBarProps {
  // 分段列表（按展示顺序）
  segments: ReadonlyArray<HealthSegment>
  // 条高样式类（默认 h-2.5）；大屏可传更高。
  className?: string
}

export default function HealthBar({ segments, className }: HealthBarProps) {
  const total = segments.reduce((sum, s) => sum + Math.max(s.count, 0), 0)

  return (
    <div
      className={cn('flex h-2.5 w-full overflow-hidden rounded-full bg-muted', className)}
      role="img"
      aria-label={segments.map((s) => `${s.label} ${s.count}`).join(' · ')}
    >
      {total > 0 &&
        segments.map((s) =>
          s.count > 0 ? (
            <div
              key={s.label}
              className={cn('h-full', levelSolid(s.level))}
              style={{ width: `${(s.count / total) * 100}%` }}
            />
          ) : null,
        )}
    </div>
  )
}
