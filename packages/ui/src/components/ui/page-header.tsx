// 页面级标题：内容页统一的「图标 + 大标题 + 一行解释 + 右侧操作」页头。
//
// 与 SectionHeader 的分工：SectionHeader 是页内的区段标题（带底部细线），
// 本组件是整页的身份标识，一个页面只应出现一次，且标题固定用 h1。
//
// 用法：
//   <PageHeader
//     icon={<Truck />}
//     title={t('delivery.changes.title')}
//     description={t('delivery.changes.desc')}
//     actions={
//       <>
//         <Button size="sm">新建变更单</Button>
//         <DocHelp topic="changes" />
//       </>
//     }
//   />
//
// 布局取舍：
// 1) 外层 justify-between + flex-wrap：小屏放不下时右侧操作整体换到下一行，而不是压缩标题或裁切按钮。
// 2) 左块 min-w-0 + 标题 / 描述各自 truncate：文案过长时省略号降级，不会把右侧操作挤出可视区。
// 3) 图标与标题同处 h1 内，保证读屏时「图标」只是装饰、标题文本仍是一个整体标题。
import type { ReactNode } from 'react'

import { cn } from '../../lib/utils'

interface PageHeaderProps {
  // 页面图标（lucide 节点，弱色底方块内展示），可选
  icon?: ReactNode
  // 页面大标题（已解析文案，一般传 t('xxx.title')）
  title: ReactNode
  // 标题下方一行小字解释本页功能，可选
  description?: ReactNode
  // 右侧操作区（常用操作按钮、文档入口等），可选
  actions?: ReactNode
  className?: string
}

function PageHeader({ icon, title, description, actions, className }: PageHeaderProps) {
  return (
    <div className={cn('flex flex-wrap items-start justify-between gap-3', className)}>
      <div className="grid min-w-0 gap-1">
        <h1 className="flex min-w-0 items-center gap-2.5 text-[22px] leading-tight font-semibold tracking-tight text-ink-1">
          {icon != null && (
            <span
              aria-hidden
              className="grid size-8 shrink-0 place-items-center rounded-lg bg-brand-50 text-brand [&_svg]:size-[18px]"
            >
              {icon}
            </span>
          )}
          <span className="truncate">{title}</span>
        </h1>
        {description != null && <p className="truncate text-sm text-ink-3">{description}</p>}
      </div>
      {actions != null && (
        <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>
      )}
    </div>
  )
}

export { PageHeader }
