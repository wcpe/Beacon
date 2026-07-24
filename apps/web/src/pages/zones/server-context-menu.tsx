// 服务器行右键操作菜单（自绘绝对定位，不引新依赖、不新增 packages/ui 导出）：
// 树里 / 未分配窄栏里的服务器行右键（onContextMenu）在光标处弹出，
// 点外部 / Esc 关闭。菜单项由调用方按可用操作传入（改派 / 解绑 / 查看详情等）。

import { useEffect, useRef } from 'react'
import { createPortal } from 'react-dom'

import { cn } from '@beacon/ui'

// 单个菜单项：文案 + 图标 + 点击回调 + 语义（普通 / 危险）
export interface ContextMenuItem {
  key: string
  label: string
  icon?: React.ReactNode
  tone?: 'default' | 'danger'
  onSelect: () => void
}

interface ServerContextMenuProps {
  // 光标位置（视口坐标）；null 表示不展示
  position: { x: number; y: number } | null
  items: ContextMenuItem[]
  onClose: () => void
}

export default function ServerContextMenu({ position, items, onClose }: ServerContextMenuProps) {
  const menuRef = useRef<HTMLDivElement | null>(null)

  // 点外部 / Esc / 滚动 关闭
  useEffect(() => {
    if (position === null) {
      return
    }
    const onPointerDown = (e: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(e.target as Node)) {
        onClose()
      }
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        onClose()
      }
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    window.addEventListener('scroll', onClose, true)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('scroll', onClose, true)
    }
  }, [position, onClose])

  if (position === null) {
    return null
  }

  // 复用 DropdownMenu content 的视觉风格（自绘定位），避免超出视口右/下边缘
  const style: React.CSSProperties = {
    left: Math.min(position.x, window.innerWidth - 200),
    top: Math.min(position.y, window.innerHeight - 40 - items.length * 34),
  }

  return createPortal(
    <div
      ref={menuRef}
      role="menu"
      className="fixed z-50 min-w-[168px] overflow-hidden rounded-lg border border-border bg-popover p-1 text-popover-foreground shadow-md"
      style={style}
    >
      {items.map((item) => (
        <button
          key={item.key}
          type="button"
          role="menuitem"
          className={cn(
            'flex w-full items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm transition-colors hover:bg-accent',
            item.tone === 'danger' ? 'text-crit hover:bg-crit-bg' : 'text-ink-1',
          )}
          onClick={() => {
            item.onSelect()
            onClose()
          }}
        >
          {item.icon}
          {item.label}
        </button>
      ))}
    </div>,
    document.body,
  )
}
