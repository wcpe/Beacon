// 可观测列表页共用「列表卡」：吸顶工具条（筛选 / 操作）+ 自身滚动的列表区 + 吸底分页。
// 筛选与分页 sticky/常驻可见，列表在 max-h + overflow-auto 的自区内滚动，翻页/搜索无需滚到底。

import type { ReactNode } from 'react'

interface ListCardProps {
  // 吸顶工具条（标题 / 筛选 / 批量操作 / 导出）
  toolbar: ReactNode
  // 列表主体（DataTable + AsyncSection）
  children: ReactNode
  // 吸底分页控件；无分页时不传
  footer?: ReactNode
}

// 列表卡：工具条吸顶、列表自区滚动、分页吸底，整卡高度可控（超大量不无限增高）。
export default function ListCard({ toolbar, children, footer }: ListCardProps) {
  return (
    <div className="grid grid-rows-[auto_minmax(0,1fr)_auto] overflow-hidden rounded-xl border border-border bg-card shadow-card">
      {/* 吸顶工具条：列表滚动时保持可见 */}
      <div className="sticky top-0 z-10 border-b border-border bg-card/95 px-4 py-3 backdrop-blur supports-backdrop-filter:bg-card/80">
        {toolbar}
      </div>
      {/* 列表区：自身滚动，翻页/搜索入口始终在吸顶/吸底可见 */}
      <div className="max-h-[calc(100vh-22rem)] overflow-y-auto px-4 pt-1 pb-1">{children}</div>
      {/* 吸底分页 */}
      {footer !== undefined && <div className="border-t border-border px-4 py-2">{footer}</div>}
    </div>
  )
}
