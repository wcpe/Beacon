// 可观测列表页共用筛选下拉：走组件库 Select 统一设计语言，首项为「全部」。
// 值 'all' 表示不筛选；派生与取数由页面持有，本组件只呈现。

import { useTranslation } from 'react-i18next'

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

// 单个选项：值 + 展示文案
export interface FilterOption {
  value: string
  label: string
}

interface FilterSelectProps {
  // 无障碍标签（同时作为触发器可访问名，便于测试定位）
  label: string
  // 当前值（'all' 表示全部）
  value: string
  // 选项列表（不含「全部」，组件自动前置）
  options: readonly FilterOption[]
  // 变更回调
  onChange: (value: string) => void
}

export default function FilterSelect({ label, value, options, onChange }: FilterSelectProps) {
  const { t } = useTranslation()
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger className="h-9 w-40" aria-label={label}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {/* 首项「全部」：值 'all' 表示不筛选 */}
        <SelectItem value="all">
          {label} · {t('observability.common.all')}
        </SelectItem>
        {options.map((opt) => (
          <SelectItem key={opt.value} value={opt.value}>
            {opt.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
