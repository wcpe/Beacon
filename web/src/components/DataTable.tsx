// 列定义驱动的数据表格：收敛各列表页重复的 <Table>…<TableBody> 骨架与空态行。
// 仅做表格呈现（含空态行），加载/错误三态交给外层 AsyncSection，职责单一便于测试。

import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { cn } from '@/lib/utils'

// 单列定义
export interface DataTableColumn<T> {
  // 表头文案
  header: ReactNode
  // 单元格渲染函数
  cell: (row: T) => ReactNode
  // 单元格额外类名
  className?: string
  // 表头额外类名
  headClassName?: string
}

interface DataTableProps<T> {
  // 列定义
  columns: DataTableColumn<T>[]
  // 行数据（undefined 视为空）
  rows: T[] | undefined
  // 行唯一键（提供索引以兜底无天然唯一键的数据）
  rowKey: (row: T, index: number) => string
  // 空态文案（默认取 i18n common.emptyData）
  emptyText?: ReactNode
  // 行点击回调（提供时行显示手型光标）
  onRowClick?: (row: T) => void
  // 行额外类名（按行计算，如未分配高亮）
  rowClassName?: (row: T) => string | undefined
}

export default function DataTable<T>({
  columns,
  rows,
  rowKey,
  emptyText,
  onRowClick,
  rowClassName,
}: DataTableProps<T>) {
  const { t } = useTranslation()
  // 表格采用底层 table.tsx 的默认（舒适）间距：表头 h-10、单元格 p-2。
  const list = rows ?? []
  return (
    <Table>
      <TableHeader>
        <TableRow>
          {columns.map((col, i) => (
            <TableHead key={i} className={col.headClassName}>
              {col.header}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {list.length > 0 ? (
          list.map((row, index) => (
            <TableRow
              key={rowKey(row, index)}
              className={cn(onRowClick && 'cursor-pointer', rowClassName?.(row))}
              onClick={onRowClick ? () => onRowClick(row) : undefined}
            >
              {columns.map((col, i) => (
                <TableCell key={i} className={col.className}>
                  {col.cell(row)}
                </TableCell>
              ))}
            </TableRow>
          ))
        ) : (
          <TableRow>
            <TableCell colSpan={columns.length} className="text-center text-muted-foreground">
              {emptyText ?? t('common.emptyData')}
            </TableCell>
          </TableRow>
        )}
      </TableBody>
    </Table>
  )
}
