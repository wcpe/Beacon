// 列表 / 卡片首屏加载骨架：贴近真实内容形状（表格行 / 卡片块），替掉空白与裸文字「加载中」。
// 复用 shadcn Skeleton（animate-pulse + bg-muted），无新依赖；供各主数据页在 React Query 加载态接入。

import { Skeleton } from '@/components/ui/skeleton'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'

interface TableSkeletonProps {
  // 列数（与真实表格列定义对齐，骨架列宽随列数均分）
  columns: number
  // 骨架行数（默认 6 行，够撑首屏视觉）
  rows?: number
}

// 表格骨架：表头占位 + 若干行单元格条，行高与真实表格一致（h-10 表头 / p-2 单元格）。
export function TableSkeleton({ columns, rows = 6 }: TableSkeletonProps) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          {Array.from({ length: columns }).map((_, i) => (
            <TableHead key={i}>
              <Skeleton className="h-4 w-16" />
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {Array.from({ length: rows }).map((_, r) => (
          <TableRow key={r}>
            {Array.from({ length: columns }).map((_, c) => (
              <TableCell key={c}>
                <Skeleton className="h-4 w-full max-w-24" />
              </TableCell>
            ))}
          </TableRow>
        ))}
      </TableBody>
    </Table>
  )
}

interface CardGridSkeletonProps {
  // 卡片块数（如 KPI 卡 4 块）
  count: number
  // 单块高度类（默认 h-20，贴近 KPI 卡）
  heightClass?: string
  // 网格列类（默认 sm:2 / xl:4 列，贴近 KPI 卡布局）
  gridClass?: string
}

// 卡片网格骨架：若干等高卡片块，用于 KPI / 概览卡的首屏占位。
export function CardGridSkeleton({
  count,
  heightClass = 'h-20',
  gridClass = 'grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4',
}: CardGridSkeletonProps) {
  return (
    <div className={gridClass}>
      {Array.from({ length: count }).map((_, i) => (
        <Skeleton key={i} className={`w-full ${heightClass} rounded-xl`} />
      ))}
    </div>
  )
}

interface TileGridSkeletonProps {
  // 瓷砖块数（状态墙占位）
  count?: number
  // 网格列类（默认贴近状态墙 auto-fill 布局）
  gridClass?: string
  // 单块高度类
  heightClass?: string
}

// 瓷砖网格骨架：状态墙 / 服务器瓷砖首屏占位。
export function TileGridSkeleton({
  count = 8,
  gridClass = "grid grid-cols-[repeat(auto-fill,minmax(11rem,1fr))] gap-2.5",
  heightClass = 'h-24',
}: TileGridSkeletonProps) {
  return (
    <div className={gridClass}>
      {Array.from({ length: count }).map((_, i) => (
        <Skeleton key={i} className={`w-full ${heightClass} rounded-lg`} />
      ))}
    </div>
  )
}
