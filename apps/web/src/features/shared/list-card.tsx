// 列表卡共用「固定工具条 + 内部滚动列表 + 固定分页」结构。
// 默认沿用最大高度滚动；固定视口页面通过 fill 填满剩余高度并使用内部滚动。

import type { ReactNode } from 'react'

import { Card, CardContent, CardFooter, CardHeader } from '@beacon/ui'

interface ListCardProps {
  // 是否填满父级剩余高度并将列表限定在卡片内部滚动区
  fill?: boolean
  // 吸顶工具条（标题 / 筛选 / 批量操作 / 导出）
  toolbar: ReactNode
  // 列表主体（DataTable + AsyncSection）
  children: ReactNode
  // 吸底分页控件；无分页时不传
  footer?: ReactNode
}

// 列表卡：工具条吸顶、列表自区滚动、分页吸底，整卡高度可控（超大量不无限增高）。
export default function ListCard({ toolbar, children, footer, fill = false }: ListCardProps) {
  return (
    <Card className={`grid grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden py-0 ${fill ? 'md:min-h-0 md:flex-1' : ''}`}>
      {/* 吸顶工具条：列表滚动时保持可见 */}
      <CardHeader className="sticky top-0 z-10 border-b border-border bg-card/95 px-4 py-3 backdrop-blur supports-backdrop-filter:bg-card/80 [.border-b]:pb-3">
        {toolbar}
      </CardHeader>
      {/* 列表区：自身滚动，翻页/搜索入口始终在吸顶/吸底可见 */}
      <CardContent
        data-slot="list-card-scroll"
        className={fill ? 'md:min-h-0 md:overflow-y-auto px-4 pt-1 pb-1' : 'max-h-[calc(100vh-22rem)] overflow-y-auto px-4 pt-1 pb-1'}
      >
        {children}
      </CardContent>
      {/* 吸底分页 */}
      {footer !== undefined && <CardFooter className="border-t border-border bg-card px-4 py-2">{footer}</CardFooter>}
    </Card>
  )
}
