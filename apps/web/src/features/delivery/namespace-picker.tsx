// 交付大域作用域选择器：拉 namespace 列表，原生 select 便于测试。首个 namespace 作默认作用域。
import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Label } from '@beacon/ui'

import { fetchNamespaces } from '../../api/delivery'

interface NamespacePickerProps {
  value: number | null
  onChange: (namespaceId: number) => void
}

export default function NamespacePicker({ value, onChange }: NamespacePickerProps) {
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
      <Label htmlFor="delivery-namespace" className="text-sm text-muted-foreground">
        {t('delivery.scope.namespace')}
      </Label>
      <select
        id="delivery-namespace"
        aria-label={t('delivery.scope.pickNamespace')}
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
