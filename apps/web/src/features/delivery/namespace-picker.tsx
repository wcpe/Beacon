// 交付大域作用域选择器：拉 namespace 列表，走组件库 Select 统一设计语言。
// FR-178：顶栏 env 收窄可选集合；全部环境下默认首个 ns（交付写操作必须绑定具体 ns）。
import { useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Label, Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

import { fetchNamespaces } from '../../api/delivery'
import { useEnvNamespaceScope } from '../env/use-env-scope'

interface NamespacePickerProps {
  value: number | null
  onChange: (namespaceId: number) => void
}

export default function NamespacePicker({ value, onChange }: NamespacePickerProps) {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['namespaces'], queryFn: fetchNamespaces })
  const envScope = useEnvNamespaceScope()
  const allItems = query.data?.items ?? []
  // env 收窄：仅列出该 env 映射的 namespace；「全部环境」不收窄
  const items = useMemo(
    () => (envScope === null ? allItems : allItems.filter((ns) => envScope.includes(ns.id))),
    [allItems, envScope],
  )

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
    const withServers = items.find((ns) => (ns.serverCount ?? 0) > 0)
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
