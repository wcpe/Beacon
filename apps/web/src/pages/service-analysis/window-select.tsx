// 时间窗预设下拉（调度决策 / 健康快照共用）：必选项无「全部」，key → 相对「现在」的毫秒跨度。
// 派生取数（from/to 计算）由各板块持有，本组件只呈现。

import { useTranslation } from 'react-i18next'

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

// 预设时间窗 key
export type WindowKey = '1h' | '6h' | '24h' | '7d'

// 时间窗 key → 毫秒跨度
export const WINDOW_MS: Record<WindowKey, number> = {
  '1h': 3_600_000,
  '6h': 21_600_000,
  '24h': 86_400_000,
  '7d': 604_800_000,
}

interface WindowSelectProps {
  // 当前时间窗
  value: WindowKey
  // 可选时间窗（各板块按需暴露子集）
  keys: readonly WindowKey[]
  // 变更回调
  onChange: (key: WindowKey) => void
}

export default function WindowSelect({ value, keys, onChange }: WindowSelectProps) {
  const { t } = useTranslation()
  return (
    <Select
      value={value}
      onValueChange={(next) => {
        onChange(next as WindowKey)
      }}
    >
      <SelectTrigger className="h-9 w-32" aria-label={t('observability.serviceAnalysis.window')}>
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {keys.map((key) => (
          <SelectItem key={key} value={key}>
            {t(`observability.serviceAnalysis.window${key}`)}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
