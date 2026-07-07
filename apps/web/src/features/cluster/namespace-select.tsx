// namespace 作用域选择器（/zones /topology 共用）：拉 namespace 列表，原生 select 便于测试。
// 首个 namespace 作为默认作用域，选中值上报给页面驱动结构树 / 拓扑取数。

import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Label } from '@beacon/ui'

import { fetchNamespaces } from '../../api/cluster'

interface NamespaceSelectProps {
  value: number | null
  onChange: (namespaceId: number) => void
}

export default function NamespaceSelect({ value, onChange }: NamespaceSelectProps) {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['namespaces'], queryFn: fetchNamespaces })
  const items = query.data?.items ?? []

  // 数据到达后若尚未选择，自动选中首个 namespace
  useEffect(() => {
    if (value === null && items.length > 0) {
      onChange(items[0].id)
    }
  }, [value, items, onChange])

  return (
    <div className="flex items-center gap-2">
      <Label htmlFor="namespace-select" className="text-sm text-muted-foreground">
        {t('cluster.topology.filter.namespace')}
      </Label>
      <select
        id="namespace-select"
        aria-label={t('cluster.topology.filter.namespace')}
        value={value ?? ''}
        onChange={(e) => {
          onChange(Number.parseInt(e.target.value, 10))
        }}
        className="h-9 rounded-md border bg-background px-2 text-sm"
      >
        {items.map((ns) => (
          <option key={ns.id} value={ns.id}>
            {ns.name}
          </option>
        ))}
      </select>
    </div>
  )
}
