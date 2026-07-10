// 交付大域作用域选择器：拉 namespace 列表，走组件库 Select 统一设计语言。首个 namespace 作默认作用域。
import { useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Label, Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

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
      <Select
        value={value === null ? '' : String(value)}
        onValueChange={(next) => {
          onChange(Number.parseInt(next, 10))
        }}
      >
        <SelectTrigger id="delivery-namespace" className="h-9 w-40" aria-label={t('delivery.scope.pickNamespace')}>
          <SelectValue placeholder={t('delivery.scope.pickNamespace')} />
        </SelectTrigger>
        <SelectContent>
          {items.map((ns) => (
            <SelectItem key={ns.id} value={String(ns.id)}>
              {ns.name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
