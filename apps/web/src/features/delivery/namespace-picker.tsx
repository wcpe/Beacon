// 交付大域写目标选择器：拉 namespace 列表，走组件库 Select 统一设计语言。
// FR-178：顶栏 env 只影响观测视图，交付写目标必须由本控件显式选择。
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

  // 数据到达 / env 变化后校准选中值（交付写操作必须绑定具体 ns，无「全部命名空间」）
  useEffect(() => {
    if (query.isLoading) {
      return
    }
    if (items.length === 0) {
      return
    }
    if (value !== null && items.some((ns) => ns.id === value)) {
      return
    }
    // 优先有服的 ns，否则第一个
    const withServers = items.find((ns) => ns.serverCount > 0)
    onChange(withServers?.id ?? items[0].id)
  }, [value, items, onChange, query.isLoading])

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
