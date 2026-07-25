// 可观测 / 列表详情共用「主从」布局。
//
// 治本策略：
// 1) 主列永远全宽，详情不参与 grid 列分配 → 表格零 reflow。
// 2) 详情用 document 外 fixed 层（createPortal），不用 Radix Dialog/Sheet：
//    - 避免 role=dialog 与业务确认框（alertdialog）嵌套抢焦点 / 误关；
//    - 列表可继续点选；Esc / 点外部 / 关闭钮收起（点外部与模态同语义，但不铺遮罩以免 reflow）。
//
// 调用方 API 不变：master + detail(null|node) + onClose + closeLabel + detailTitle。

import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

import { Button } from '@beacon/ui'
import { X } from 'lucide-react'

interface MasterDetailProps {
  master: ReactNode
  detail: ReactNode | null
  detailTitle?: ReactNode
  onClose: () => void
  closeLabel: string
}

export default function MasterDetail({
  master,
  detail,
  detailTitle,
  onClose,
  closeLabel,
}: MasterDetailProps) {
  const open = detail !== null
  const drawerRef = useRef<HTMLElement | null>(null)

  // Esc 关闭；若页面上还有 alertdialog / dialog 打开则不抢关（让内层先关）
  useEffect(() => {
    if (!open) {
      return
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') {
        return
      }
      const nested = document.querySelector(
        '[role="alertdialog"], [data-slot="dialog-content"], [data-slot="sheet-content"]',
      )
      if (nested) {
        return
      }
      onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => {
      window.removeEventListener('keydown', onKey)
    }
  }, [open, onClose])

  // 点抽屉外关闭（与模态「点遮罩关闭」同语义，但不铺遮罩，列表仍可点选换行）
  useEffect(() => {
    if (!open) {
      return
    }
    const onPointerDown = (e: PointerEvent) => {
      const target = e.target
      if (!(target instanceof Node)) {
        return
      }
      // 内层确认框 / Dialog / Sheet / 下拉 / 弹出层优先，不抢关
      if (target instanceof Element) {
        const nested = target.closest(
          '[role="alertdialog"], [data-slot="dialog-content"], [data-slot="sheet-content"], [data-slot="dropdown-menu-content"], [data-slot="select-content"], [data-radix-popper-content-wrapper]',
        )
        if (nested) {
          return
        }
      }
      const drawer = drawerRef.current
      if (drawer && drawer.contains(target)) {
        return
      }
      onClose()
    }
    // 捕获阶段：先于列表行 click 收起，再由行 click 打开新详情（换行不卡）
    document.addEventListener('pointerdown', onPointerDown, true)
    return () => {
      document.removeEventListener('pointerdown', onPointerDown, true)
    }
  }, [open, onClose])

  return (
    <>
      <div className="min-w-0">{master}</div>

      {open &&
        createPortal(
          <aside
            ref={drawerRef}
            // 非 ARIA dialog：不锁滚动、不焦点陷阱，便于列表继续操作与嵌套确认框
            data-slot="master-detail-drawer"
            className="fixed inset-y-0 right-0 z-40 flex w-full max-w-[min(32rem,90vw)] flex-col border-l border-border bg-card text-sm text-card-foreground shadow-[var(--sh-pop)] animate-in fade-in-0 slide-in-from-right-10 duration-200"
            aria-label={typeof detailTitle === 'string' ? detailTitle : closeLabel}
          >
            <div className="flex shrink-0 items-center justify-between border-b border-border px-4 py-3">
              <span className="text-[13px] font-semibold text-ink-1">{detailTitle ?? '\u00a0'}</span>
              <Button
                size="sm"
                variant="ghost"
                className="size-7 shrink-0 p-0"
                onClick={onClose}
                aria-label={closeLabel}
              >
                <X className="size-4" />
              </Button>
            </div>
            <div className="min-h-0 flex-1 overflow-y-auto px-4 py-3">{detail}</div>
          </aside>,
          document.body,
        )}
    </>
  )
}
