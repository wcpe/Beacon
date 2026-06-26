// 面板头部的 chip 下拉：受管侧 scope chip / 服务器侧 server chip 共用。
// 触发器是一枚紧凑 chip（图标 + 当前值 + ▾），点开下拉选 mock 候选。

import type { ReactNode } from 'react'
import { ChevronDown } from 'lucide-react'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { cn } from '@/lib/utils'

export interface ChipOption {
  value: string
  label: ReactNode
}

export default function ChipSelect({
  value,
  options,
  onChange,
  leading,
}: {
  value: string
  options: ChipOption[]
  onChange: (value: string) => void
  // chip 前缀图标 / 状态点
  leading?: ReactNode
}) {
  const current = options.find((o) => o.value === value)
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <button
          type="button"
          className={cn(
            'flex items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1 text-xs',
            'text-foreground hover:bg-muted/50 transition-colors',
          )}
        >
          {leading}
          <span className="max-w-[120px] truncate">{current?.label ?? value}</span>
          <ChevronDown className="h-3 w-3 text-muted-foreground" />
        </button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-40">
        {options.map((o) => (
          <DropdownMenuItem key={o.value} onClick={() => onChange(o.value)} className="text-xs">
            {o.label}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
