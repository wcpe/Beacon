// 页面操作行（业务页顶栏）：仅承载右侧操作槽（筛选器 / 命名空间选择 / 主按钮）。
// E5：页面身份已上移到页眉面包屑（shell/breadcrumb，E3），内容区不再渲染图标与大标题；
// 本组件随之退化为「有操作则渲染一行、无操作则不渲染」，以免删除后留下空洞。
// 不依赖 i18n / 路由 / 业务 API，操作节点由调用方传入。
import type { ReactNode } from 'react'
import { cn } from '../lib/utils'

interface PageHeaderProps {
  // 右侧操作区（筛选、命名空间选择、主按钮等）；缺省时不渲染任何内容
  actions?: ReactNode
  // 外层额外类名
  className?: string
}

export default function PageHeader({ actions, className }: PageHeaderProps) {
  if (actions == null) {
    return null
  }
  return (
    <header className={cn('flex flex-wrap items-center justify-end gap-2', className)}>
      {actions}
    </header>
  )
}
