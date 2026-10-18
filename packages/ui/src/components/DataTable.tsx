// 列定义驱动的数据表格：收敛各列表页重复的 <Table>…<TableBody> 骨架与空态行。
// 仅做表格呈现（含空态行），加载/错误三态交给外层 AsyncSection，职责单一便于测试。

import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { ChevronRight } from 'lucide-react'

import { Button } from './ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from './ui/table'
import { cn } from '../lib/utils'

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
  // 可选分页大小；不传时保持原来的全量渲染
  pageSize?: number
  // 表格密度：默认保持原舒适间距，紧凑档用于高密度运维页
  density?: 'comfortable' | 'compact'
  // 启用后表头吸顶，纵向滚动由外层列表容器承担
  stickyHeader?: boolean
}

export default function DataTable<T>({
  columns,
  rows,
  rowKey,
  emptyText,
  onRowClick,
  rowClassName,
  pageSize,
  density = 'comfortable',
  stickyHeader = false,
}: DataTableProps<T>) {
  // 舒适档沿用底层 table.tsx 默认；紧凑档只收紧本表，不影响其它表格。
  const list = useMemo(() => rows ?? [], [rows])
  const [pageIndex, setPageIndex] = useState(0)
  const effectivePageSize = pageSize && pageSize > 0 ? pageSize : undefined
  const pageCount = effectivePageSize ? Math.max(1, Math.ceil(list.length / effectivePageSize)) : 1
  const currentPage = Math.min(pageIndex, pageCount - 1)
  const pageRows = useMemo(() => {
    if (!effectivePageSize) return list
    const start = currentPage * effectivePageSize
    return list.slice(start, start + effectivePageSize)
  }, [currentPage, effectivePageSize, list])

  useEffect(() => {
    setPageIndex(0)
  }, [effectivePageSize, list.length])

  return (
    <>
      <Table containerClassName={stickyHeader ? 'overflow-x-visible' : undefined}>
        <TableHeader>
          <TableRow>
            {columns.map((col, i) => (
              <TableHead
                key={i}
                className={cn(
                  density === 'compact' && 'h-8 px-2 text-xs',
                  stickyHeader && 'sticky top-0 z-10 bg-surface-2',
                  col.headClassName,
                )}
              >
                {col.header}
              </TableHead>
            ))}
            {/* 可点击行的行尾箭头占位：给出常驻的"可进入"暗示，
                不依赖只有鼠标才能感知的 cursor 变化 */}
            {onRowClick && (
              <TableHead
                className={cn('w-8', stickyHeader && 'sticky top-0 z-10 bg-surface-2')}
                aria-label="操作"
              />
            )}
          </TableRow>
        </TableHeader>
        <TableBody>
          {list.length > 0 ? (
            pageRows.map((row, index) => {
              const absoluteIndex = effectivePageSize
                ? currentPage * effectivePageSize + index
                : index
              return (
                <TableRow
                  key={rowKey(row, absoluteIndex)}
                  className={cn(
                    onRowClick &&
                      // 可点击行：悬停底色 + 指针 + 键盘聚焦环；窄屏/触摸场景靠行尾箭头列传达可点性
                      'group/row cursor-pointer outline-none hover:bg-surface-2 focus-visible:bg-brand-50/50 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring',
                    rowClassName?.(row),
                  )}
                  onClick={onRowClick ? () => { onRowClick(row); } : undefined}
                  // 可点击行的键盘可达性：可聚焦 + Enter / 空格触发（与鼠标点击等价）
                  tabIndex={onRowClick ? 0 : undefined}
                  onKeyDown={
                    onRowClick
                      ? (e) => {
                          if (e.key === 'Enter' || e.key === ' ') {
                            e.preventDefault()
                            onRowClick(row)
                          }
                        }
                      : undefined
                  }
                >
                  {columns.map((col, i) => (
                    <TableCell
                      key={i}
                      className={cn(
                        density === 'compact' && 'px-2 py-1.5 text-xs',
                        col.className,
                      )}
                    >
                      {col.cell(row)}
                    </TableCell>
                  ))}
                  {onRowClick && (
                    <TableCell className="w-8 text-right">
                      <ChevronRight
                        aria-hidden
                        className="size-4 text-ink-4 transition-transform group-hover/row:translate-x-0.5 group-hover/row:text-ink-2 motion-reduce:translate-x-0 motion-reduce:transition-none"
                      />
                    </TableCell>
                  )}
                </TableRow>
              )
            })
          ) : (
            <TableRow>
              <TableCell
                colSpan={columns.length + (onRowClick ? 1 : 0)}
                className="text-center text-muted-foreground"
              >
                {emptyText ?? '暂无数据'}
              </TableCell>
            </TableRow>
          )}
        </TableBody>
      </Table>
      {effectivePageSize && list.length > effectivePageSize && (
        <div className="flex flex-wrap items-center justify-end gap-2 py-2 text-sm text-muted-foreground">
          <span>
            第 {currentPage + 1} / {pageCount} 页 · 共 {list.length} 行
          </span>
          {/* 内部分页也走组件库按钮（此前是裸 <button>，违反"页面不自建通用控件"） */}
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={currentPage === 0}
            onClick={() => { setPageIndex((p) => Math.max(0, p - 1)); }}
          >
            上一页
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={currentPage >= pageCount - 1}
            onClick={() => { setPageIndex((p) => Math.min(pageCount - 1, p + 1)); }}
          >
            下一页
          </Button>
        </div>
      )}
    </>
  )
}
