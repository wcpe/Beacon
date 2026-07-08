// 可观测列表页共用「主从（master-detail）」布局：左主列（列表）+ 右非模态详情列。
// 右列用 border-l 分隔、粘顶、自身滚动，绝不是模态遮罩层——不遮罩、不模糊背景、不撑动主区。
// 未选中行时右列收起（仅主列占满），选中后右列展开展示 detail 内容。

import type { ReactNode } from 'react'

import { Button, cn } from '@beacon/ui'
import { X } from 'lucide-react'

interface MasterDetailProps {
  // 左侧主列内容（列表、筛选、分页）
  master: ReactNode
  // 右侧详情内容；null 表示无选中，右列收起
  detail: ReactNode | null
  // 详情面板标题
  detailTitle?: ReactNode
  // 关闭详情（清空选中）
  onClose: () => void
  // 关闭按钮无障碍文案
  closeLabel: string
}

// 主从布局：右列非模态常驻列（非 overlay）。选中后并排显示，各自独立滚动。
export default function MasterDetail({
  master,
  detail,
  detailTitle,
  onClose,
  closeLabel,
}: MasterDetailProps) {
  const open = detail !== null
  return (
    <div
      className={cn(
        'grid gap-4',
        open ? 'lg:grid-cols-[minmax(0,1fr)_22rem] xl:grid-cols-[minmax(0,1fr)_26rem]' : 'grid-cols-1',
      )}
    >
      {/* 主列：始终占据剩余宽度，min-w-0 防止内容撑破网格 */}
      <div className="min-w-0">{master}</div>

      {/* 详情列：非模态布局列，border-l 分隔，粘顶自滚。仅在有选中时渲染。 */}
      {open && (
        <aside className="lg:border-l lg:border-border lg:pl-4">
          <div className="lg:sticky lg:top-0 grid max-h-[calc(100vh-9rem)] grid-rows-[auto_minmax(0,1fr)] overflow-hidden rounded-xl border border-border bg-card shadow-card lg:rounded-none lg:border-0 lg:shadow-none">
            <div className="flex items-center justify-between border-b border-border px-4 py-3">
              <span className="text-[13px] font-semibold text-ink-1">{detailTitle}</span>
              <Button size="sm" variant="ghost" className="size-7 shrink-0 p-0" onClick={onClose} aria-label={closeLabel}>
                <X className="size-4" />
              </Button>
            </div>
            <div className="overflow-y-auto px-4 py-3">{detail}</div>
          </div>
        </aside>
      )}
    </div>
  )
}
