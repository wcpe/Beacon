// 维度输入统一组件（FR-51）：「下拉 + 可编辑」combobox。
// 把环境 / 大区 / 小区 / serverId 等维度输入统一为可键入过滤 + 候选下拉的控件。
// 两种模式：
//   - allowCustom=true（可编辑）：键入即上报，可提交候选列表外的新值（筛选框 / 可新建维度的表单）。
//   - allowCustom=false（严格选）：键入仅用于过滤候选，列表外值不上报（纯选择处，目标须为已存在项）。
// 候选两种形态（FR-70）：
//   - string[]：值即显示文本（zone / 大区 / serverId 等沿用，向后兼容）。
//   - {value,label}[]：值与显示分离——value 是真实值（如环境 code），label 是显示文本（如「编码 · 名称」）；
//     过滤匹配 label 或 value 均可命中，选中 / 上报回传 value，输入框回显 label。
// 基于既有 radix-ui umbrella 的 Popover + Input 自实现（项目无 command/popover 基元，避免新增依赖）。

import * as React from 'react'
import { useTranslation } from 'react-i18next'
import { Popover as PopoverPrimitive } from 'radix-ui'
import { ChevronDownIcon, XIcon } from 'lucide-react'

import { cn } from '@/lib/utils'
import { Input } from '@/components/ui/input'

// 值/显示分离的候选项：value 是真实值（上报 / 过滤的权威），label 是展示文本。
export interface ComboboxOption {
  value: string
  label: string
}

export interface ComboboxProps {
  // 当前值（受控）
  value: string
  // 值变更回调：严格模式仅在点选 / 输入命中候选时触发
  onChange: (value: string) => void
  // 候选项（已由上层按维度派生）：string[] 或 {value,label}[]
  options: Array<string | ComboboxOption>
  // 是否允许提交候选列表外的新值（true=可编辑，false=严格选）
  allowCustom: boolean
  placeholder?: string
  id?: string
  className?: string
  disabled?: boolean
  // 无障碍标签（透传到输入框，便于按 label 定位）
  'aria-label'?: string
  // 是否提供一键清空（值非空时展示 × 按钮，点击回传空值）；用于筛选框「清回全部」（FR-63）
  clearable?: boolean
  // 清空按钮的无障碍标签（clearable 时必传，上层经 i18n 注入中文）
  clearLabel?: string
}

// 候选项归一化为 {value,label}：string 形态下 value 与 label 同值。
function normalizeOption(opt: string | ComboboxOption): ComboboxOption {
  return typeof opt === 'string' ? { value: opt, label: opt } : opt
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
  clearable,
  clearLabel,
}: ComboboxProps) {
  const { t } = useTranslation()
  const [open, setOpen] = React.useState(false)
  // 键入草稿：严格模式下与 value 解耦（键入仅过滤，不立即上报）
  const [query, setQuery] = React.useState('')

  // 候选归一化（兼容 string[] 与 {value,label}[]）
  const normalized = React.useMemo(() => options.map(normalizeOption), [options])

  // 打开下拉时草稿归零，展示全部候选；关闭时严格模式回填已选值
  React.useEffect(() => {
    if (!open && !allowCustom) setQuery('')
  }, [open, allowCustom])

  // 当前值对应的显示文本：命中候选取其 label，否则回退为 value 本身（如可编辑模式的自定义值）
  const valueLabel = React.useMemo(() => {
    const hit = normalized.find((o) => o.value === value)
    return hit ? hit.label : value
  }, [normalized, value])

  // 输入框展示值：可编辑模式直接显示受控值；严格模式打开时显示草稿、关闭时回显已选 label
  const inputValue = allowCustom ? value : open ? query : valueLabel

  // 候选过滤：大小写无关子串匹配，label 或 value 任一命中即保留（草稿为空时列出全部）
  const filterText = (allowCustom ? value : query).trim().toLowerCase()
  const filtered = filterText
    ? normalized.filter(
        (o) =>
          o.label.toLowerCase().includes(filterText) || o.value.toLowerCase().includes(filterText),
      )
    : normalized

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

  function handlePick(opt: ComboboxOption) {
    onChange(opt.value)
    setQuery('')
    setOpen(false)
  }

  function handleClear(e: React.MouseEvent) {
    // 阻止冒泡触发输入框 onClick 展开下拉，仅清空
    e.stopPropagation()
    onChange('')
    setQuery('')
  }

  // 是否展示清空按钮：启用 clearable、值非空、未禁用
  const showClear = clearable && value !== '' && !disabled

  return (
    <PopoverPrimitive.Root open={open} onOpenChange={setOpen}>
      {/* Anchor 用 DOM 元素承接 ref（Input 非 forwardRef），并把宽度透传给下拉内容 */}
      <PopoverPrimitive.Anchor className={cn('relative block', className)}>
        {/* 右内侧补一个 chevron，让控件一眼可辨为下拉（与 select 触发器视觉一致）；
            clearable 时在 chevron 左侧再补一个 × 清空按钮。
            pointer-events-none 不挡输入；输入框右侧 padding 随有无清空按钮调整，避免文本压住图标 */}
        <Input
          id={id}
          aria-label={ariaLabel}
          disabled={disabled}
          placeholder={placeholder}
          value={inputValue}
          onChange={(e) => handleInput(e.target.value)}
          onClick={() => setOpen(true)}
          autoComplete="off"
          className={showClear ? 'pr-14' : 'pr-8'}
        />
        {showClear && (
          <button
            type="button"
            aria-label={clearLabel}
            onClick={handleClear}
            className="absolute top-1/2 right-7 flex size-4 -translate-y-1/2 items-center justify-center rounded text-muted-foreground opacity-60 hover:opacity-100"
          >
            <XIcon className="size-3.5" />
          </button>
        )}
        <ChevronDownIcon className="pointer-events-none absolute top-1/2 right-2.5 size-4 -translate-y-1/2 opacity-50" />
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
              <div className="px-2 py-1.5 text-sm text-muted-foreground">{t('common.noMatchOption')}</div>
            ) : (
              filtered.map((opt) => (
                <div
                  key={opt.value}
                  role="option"
                  aria-selected={opt.value === value}
                  className={cn(
                    'cursor-pointer rounded-md px-2 py-1.5 text-sm outline-none select-none hover:bg-accent hover:text-accent-foreground',
                    opt.value === value && 'bg-accent/60',
                  )}
                  onClick={() => handlePick(opt)}
                >
                  {opt.label}
                </div>
              ))
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
