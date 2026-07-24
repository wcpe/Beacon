// 二阶页眉（业务页顶栏）：标题区 + 可选副文案 + 右侧操作槽。
// 统一各页「SectionHeader lg + 右侧按钮/选择器」的拼装，避免间距与对齐漂移。
// 不依赖 i18n / 路由 / 业务 API，文案由调用方传入已解析节点。
import type { ReactNode } from 'react'
import { cn } from '../lib/utils'

interface PageHeaderProps {
  // 标题左侧图标（lucide 节点）
  icon?: ReactNode
  // 主标题
  title: ReactNode
  // 副文案 / 任务说明（小号弱色，可换行）
  description?: ReactNode
  // 右侧操作区（筛选、命名空间选择、主按钮等）
  actions?: ReactNode
  // 外层额外类名
  className?: string
}

export default function PageHeader({ icon, title, description, actions, className }: PageHeaderProps) {
  return (
    <header
      className={cn(
        'flex flex-wrap items-start justify-between gap-x-4 gap-y-3 border-b border-border pb-3',
        className,
      )}
    >
      <div className="flex min-w-0 flex-1 items-start gap-3">
        {icon != null && (
          <span
            aria-hidden
            className="mt-0.5 grid size-9 shrink-0 place-items-center rounded-xl bg-brand-50 text-brand shadow-[inset_0_0_0_1px_var(--brand-100)]"
          >
            <span className="[&_svg]:size-4 [&_svg]:shrink-0">{icon}</span>
          </span>
        )}
        <div className="min-w-0 grid gap-0.5">
          <h1 className="truncate text-lg font-semibold tracking-tight text-ink-1">{title}</h1>
          {description != null && (
            <p className="text-sm leading-snug text-ink-3 text-balance">{description}</p>
          )}
        </div>
      </div>
      {actions != null && (
        <div className="ml-auto flex flex-wrap items-center justify-end gap-2">{actions}</div>
      )}
    </header>
  )
}
