// 可观测列表页共用筛选下拉：原生 select（便于测试 selectOptions），首项为「全部」。
// 值 'all' 表示不筛选；派生与取数由页面持有，本组件只呈现。

import { useTranslation } from 'react-i18next'

// 单个选项：值 + 展示文案
export interface FilterOption {
  value: string
  label: string
}

interface FilterSelectProps {
  // 无障碍标签（同时用于 selectOptions 定位）
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
    <select
      aria-label={label}
      value={value}
      onChange={(e) => {
        onChange(e.target.value)
      }}
      className="h-9 rounded-md border bg-background px-2 text-sm"
    >
      <option value="all">
        {label} · {t('observability.common.all')}
      </option>
      {options.map((opt) => (
        <option key={opt.value} value={opt.value}>
          {opt.label}
        </option>
      ))}
    </select>
  )
}
