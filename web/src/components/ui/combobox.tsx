// 维度输入统一组件（FR-51）：「下拉 + 可编辑」combobox。
// 把环境 / 大区 / 小区 / serverId 等维度输入统一为可键入过滤 + 候选下拉的控件。
// 两种模式：
//   - allowCustom=true（可编辑）：键入即上报，可提交候选列表外的新值（筛选框 / 可新建维度的表单）。
//   - allowCustom=false（严格选）：键入仅用于过滤候选，列表外值不上报（纯选择处，目标须为已存在项）。
// 基于既有 radix-ui umbrella 的 Popover + Input 自实现（项目无 command/popover 基元，避免新增依赖）。

import * as React from 'react'
import { Popover as PopoverPrimitive } from 'radix-ui'

import { cn } from '@/lib/utils'
import { Input } from '@/components/ui/input'

export interface ComboboxProps {
  // 当前值（受控）
  value: string
  // 值变更回调：严格模式仅在点选 / 输入命中候选时触发
  onChange: (value: string) => void
  // 候选项（已由上层按维度派生）
  options: string[]
  // 是否允许提交候选列表外的新值（true=可编辑，false=严格选）
  allowCustom: boolean
  placeholder?: string
  id?: string
  className?: string
  disabled?: boolean
  // 无障碍标签（透传到输入框，便于按 label 定位）
  'aria-label'?: string
}

export function Combobox({
  value,
  onChange,
  options,
  allowCustom,
  placeholder,
  id,
  className,
  disabled,
  'aria-label': ariaLabel,
}: ComboboxProps) {
  const [open, setOpen] = React.useState(false)
  // 键入草稿：严格模式下与 value 解耦（键入仅过滤，不立即上报）
  const [query, setQuery] = React.useState('')

  // 打开下拉时草稿归零，展示全部候选；关闭时严格模式回填已选值
  React.useEffect(() => {
    if (!open && !allowCustom) setQuery('')
  }, [open, allowCustom])

  // 输入框展示值：可编辑模式直接显示受控值；严格模式打开时显示草稿、关闭时显示已选值
  const inputValue = allowCustom ? value : open ? query : value

  // 候选过滤：大小写无关子串匹配（草稿为空时列出全部）
  const filter = (allowCustom ? value : query).trim().toLowerCase()
  const filtered = filter
    ? options.filter((o) => o.toLowerCase().includes(filter))
    : options

  function handleInput(next: string) {
    if (!open) setOpen(true)
    if (allowCustom) {
      // 可编辑：键入即上报（放行列表外新值）
      onChange(next)
    } else {
      // 严格选：键入仅更新过滤草稿，不上报
      setQuery(next)
    }
  }

  function handlePick(opt: string) {
    onChange(opt)
    setQuery('')
    setOpen(false)
  }

  return (
    <PopoverPrimitive.Root open={open} onOpenChange={setOpen}>
      {/* Anchor 用 DOM 元素承接 ref（Input 非 forwardRef），并把宽度透传给下拉内容 */}
      <PopoverPrimitive.Anchor className={cn('block', className)}>
        <Input
          id={id}
          aria-label={ariaLabel}
          disabled={disabled}
          placeholder={placeholder}
          value={inputValue}
          onChange={(e) => handleInput(e.target.value)}
          onClick={() => setOpen(true)}
          autoComplete="off"
        />
      </PopoverPrimitive.Anchor>
      <PopoverPrimitive.Portal>
        <PopoverPrimitive.Content
          data-slot="combobox-content"
          align="start"
          sideOffset={4}
          // 阻止聚焦回退到触发器，保持输入框焦点以便连续键入过滤
          onOpenAutoFocus={(e) => e.preventDefault()}
          className={cn(
            'z-50 max-h-60 w-(--radix-popover-trigger-width) min-w-36 overflow-y-auto rounded-lg bg-popover p-1 text-popover-foreground shadow-md ring-1 ring-foreground/10',
          )}
        >
          <div role="listbox">
            {filtered.length === 0 ? (
              <div className="px-2 py-1.5 text-sm text-muted-foreground">无匹配候选</div>
            ) : (
              filtered.map((opt) => (
                <div
                  key={opt}
                  role="option"
                  aria-selected={opt === value}
                  className={cn(
                    'cursor-pointer rounded-md px-2 py-1.5 text-sm outline-none select-none hover:bg-accent hover:text-accent-foreground',
                    opt === value && 'bg-accent/60',
                  )}
                  onClick={() => handlePick(opt)}
                >
                  {opt}
                </div>
              ))
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
