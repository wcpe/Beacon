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
import { Popover as PopoverPrimitive } from 'radix-ui'
import { CheckIcon, ChevronDownIcon, XIcon } from 'lucide-react'

import { cn } from '../../lib/utils'
import { Input } from './input'

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
  options: (string | ComboboxOption)[]
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
  // 无匹配候选时的空态文案
  emptyText?: string
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
  emptyText = '无匹配候选',
}: ComboboxProps) {
  const [open, setOpen] = React.useState(false)
  const anchorRef = React.useRef<HTMLDivElement>(null)
  // 键入草稿：严格模式下与 value 解耦（键入仅过滤，不立即上报）
  const [query, setQuery] = React.useState('')
  // 键盘高亮项索引：-1 表示未高亮。鼠标悬停与方向键共用同一份状态，
  // 否则键盘用户按方向键看不到任何视觉反馈，也就无法判断回车会选中哪一项。
  const [activeIndex, setActiveIndex] = React.useState(-1)
  const listboxId = React.useId()

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
    setActiveIndex(-1)
  }

  // 候选集合变化时把高亮复位，避免索引越界后回车选到错项。
  // 依赖必须含 filtered.length：候选由上层异步刷新时，输入文本可能没变而候选变短，
  // 只盯 filterText 会让旧索引越界，视觉高亮凭空消失。
  React.useEffect(() => {
    setActiveIndex(-1)
  }, [filterText, filtered.length])

  // 关闭下拉时复位高亮，下次打开从「无高亮」开始。
  // 单独成一个 effect（依赖只有 open）：若与上面的重置合并，打开瞬间的重置会
  // 覆盖方向键刚设好的首/末项高亮，导致首次按键看不到反馈。
  React.useEffect(() => {
    if (!open) setActiveIndex(-1)
  }, [open])

  // 高亮项滚动跟随：候选容器是 max-h-60 overflow-y-auto（约 7~8 行），
  // 上百项候选时高亮很快跑出可视区，屏幕毫无反馈，等于键盘操作失效。
  // block:'nearest' 只在高亮项越界时才滚动，不会把可视区每次顶回顶部。
  React.useEffect(() => {
    if (!open || activeIndex < 0) return
    const optionId = `${listboxId}-option-${String(activeIndex)}`
    const scrollToOption = () => {
      const optionEl = document.getElementById(optionId)
      // jsdom 无滚动布局，scrollIntoView 在测试环境里可能整体缺失：
      // 类型上它是必有的，这里用 typeof 守卫兜住运行时缺失，别让测试环境崩
      if (optionEl && typeof optionEl.scrollIntoView === 'function') {
        optionEl.scrollIntoView({ block: 'nearest' })
      }
    }
    scrollToOption()
    // Radix Portal 的首次挂载比本副作用晚一次提交：关闭态按方向键「打开 + 立即高亮」时
    // 节点此刻还不存在，不补一次延迟重试，这条路径就永远不滚动。
    if (document.getElementById(optionId)) return
    const timer = setTimeout(scrollToOption, 0)
    return () => { clearTimeout(timer) }
  }, [activeIndex, open, listboxId])

  // 键盘交互（WAI-ARIA APG combobox 模式）：方向键移动高亮、Enter 选中、Esc 关闭。
  // 没有这段时，纯选择场景对键盘用户完全不可用——候选是 div，Tab 也到不了。
  function handleKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const step = e.key === 'ArrowDown' ? 1 : -1
      if (filtered.length === 0) {
        // 无候选时仍打开列表以便看到空态，但没有可高亮项
        if (!open) setOpen(true)
        return
      }
      if (!open) {
        // 关闭态首次按下：同一次按键就落到首项 / 末项，
        // 否则要按第二次才见到高亮，用户会以为键盘失灵。
        setOpen(true)
        setActiveIndex(step === 1 ? 0 : filtered.length - 1)
        return
      }
      setActiveIndex((prev) => {
        // 从两端环绕，与原生 select 一致
        const next = prev + step
        if (next < 0) return filtered.length - 1
        if (next >= filtered.length) return 0
        return next
      })
      return
    }
    if (e.key === 'Enter') {
      // 可编辑模式下只有高亮了候选才拦截回车，否则应让上层表单正常提交自定义值。
      if (open && activeIndex >= 0 && activeIndex < filtered.length) {
        e.preventDefault()
        handlePick(filtered[activeIndex])
      }
      return
    }
    if (e.key === 'Escape') {
      if (open) {
        e.preventDefault()
        setOpen(false)
      }
      return
    }
    // Home / End 不做候选首尾跳转：严格选模式下输入框内容是可编辑的真实查询文本，
    // APG 规定 Home / End 应移动文本光标；拦截它会抢走本应属于光标的行为。
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
      <PopoverPrimitive.Anchor ref={anchorRef} className={cn('relative block', className)}>
        {/* 右内侧补一个 chevron，让控件一眼可辨为下拉（与 select 触发器视觉一致）；
            clearable 时在 chevron 左侧再补一个 × 清空按钮。
            pointer-events-none 不挡输入；输入框右侧 padding 随有无清空按钮调整，避免文本压住图标 */}
        <Input
          id={id}
          aria-label={ariaLabel}
          // 键盘与读屏语义：显式 role="combobox"（ARIA 1.2）。
          // 隐式 textbox 角色不支持 aria-expanded（aria-allowed-attr 判无效），
          // 且没有 combobox 角色时读屏不会播报「已展开 / 已折叠」。
          role="combobox"
          aria-expanded={open}
          // 列表关闭时 Popover 内容整棵卸载，此时若仍给出 listboxId 就是悬空 IDREF，
          // 因此只在展开时暴露 aria-controls。
          aria-controls={open ? listboxId : undefined}
          aria-autocomplete="list"
          aria-activedescendant={
            open && activeIndex >= 0 && activeIndex < filtered.length
              ? `${listboxId}-option-${String(activeIndex)}`
              : undefined
          }
          disabled={disabled}
          placeholder={placeholder}
          value={inputValue}
          onChange={(e) => { handleInput(e.target.value); }}
          onClick={() => { setOpen(true); }}
          onKeyDown={handleKeyDown}
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
          sideOffset={6}
          // 阻止聚焦回退到触发器，保持输入框焦点以便连续键入过滤
          onOpenAutoFocus={(e) => { e.preventDefault(); }}
          // 输入框属于锚点区域，点击回到输入位置时不关闭候选面板
          onInteractOutside={(e) => {
            if (e.target instanceof Node && anchorRef.current?.contains(e.target)) e.preventDefault()
          }}
          className={cn(
            'z-50 max-h-60 w-(--radix-popover-trigger-width) min-w-36 overflow-y-auto scrollbar-hide rounded-xl border border-border bg-popover p-1.5 text-popover-foreground shadow-[var(--sh-pop)] ring-1 ring-border/60',
          )}
        >
          <div role="listbox" id={listboxId}>
            {filtered.length === 0 ? (
              <div className="px-2 py-1.5 text-sm text-muted-foreground">{emptyText}</div>
            ) : (
              filtered.map((opt, index) => (
                <div
                  key={opt.value}
                  id={`${listboxId}-option-${String(index)}`}
                  role="option"
                  aria-selected={opt.value === value}
                  className={cn(
                    'flex cursor-pointer items-center gap-1.5 rounded-md px-2 py-1.5 text-sm outline-none transition-colors duration-100 select-none hover:bg-accent hover:text-accent-foreground',
                    // 焦点通道：键盘 / 鼠标高亮项用背景色，表示「回车会选中这一项」
                    index === activeIndex && 'bg-accent text-accent-foreground',
                    // 当前值通道：左侧勾标记 + 加粗，与高亮是两套视觉，
                    // 两者同时命中（高亮正好落在已选项）时仍能分辨焦点与当前值
                    opt.value === value && 'font-medium',
                  )}
                  onMouseEnter={() => { setActiveIndex(index); }}
                  onClick={() => { handlePick(opt); }}
                >
                  {/* 勾标记始终占位，未选中时以透明占位保持文本左对齐一致 */}
                  <CheckIcon
                    aria-hidden="true"
                    className={cn('size-3.5 shrink-0', opt.value === value ? 'opacity-100' : 'opacity-0')}
                  />
                  <span className="truncate">{opt.label}</span>
                </div>
              ))
            )}
          </div>
        </PopoverPrimitive.Content>
      </PopoverPrimitive.Portal>
    </PopoverPrimitive.Root>
  )
}
