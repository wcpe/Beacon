// 区段标题轻分隔（FR-107 卡片降级）：把原先「Card 当区段容器」降级为
// 「区段标题（含图标）+ border-b 细线 + 内容」。Card 仅留给真正有边界的对象
// （单条详情 / 模态 / 可点磁贴）；纯粹包一段内容加边框 padding 的区段改用本组件。
//
// 用法：
//   <section className="space-y-3">
//     <SectionHeader icon={<Gauge className="size-4" />} title={t('xxx.title')} actions={<Btn/>} />
//     ……区段内容……
//   </section>

import type { ReactNode } from 'react'
import { cn } from '@/lib/utils'

interface SectionHeaderProps {
  // 区段图标（lucide 节点，弱色），可选
  icon?: ReactNode
  // 区段标题文案（已解析，一般传 t('xxx.title')）
  title: ReactNode
  // 标题右侧计数 / 副标题（小号弱色），可选
  count?: ReactNode
  // 右槽：区段级操作 / 控件（如解锁开关、时间窗切换），可选
  actions?: ReactNode
  // 标题字号档位：base（默认，区段级）/ lg（页内主区段）
  size?: 'base' | 'lg'
  className?: string
}

export default function SectionHeader({
  icon,
  title,
  count,
  actions,
  size = 'base',
  className,
}: SectionHeaderProps) {
  return (
    <div className={cn('flex items-center gap-3 border-b pb-2', className)}>
      <h2
        className={cn(
          'flex items-center gap-2 font-semibold',
          size === 'lg' ? 'text-lg' : 'text-base',
        )}
      >
        {icon && (
          <span aria-hidden className="text-muted-foreground">
            {icon}
          </span>
        )}
        {title}
      </h2>
      {count != null && <span className="text-sm text-muted-foreground">{count}</span>}
      {actions != null && <div className="ml-auto flex items-center gap-2">{actions}</div>}
    </div>
  )
}
