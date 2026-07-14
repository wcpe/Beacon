// namespace 作用域选择器（/zones /topology 共用）：拉 namespace 列表，走组件库 Select 统一设计语言。
// 首个 namespace 作为默认作用域，选中值上报给页面驱动结构树 / 拓扑取数。
// 顶栏 env 过滤器（FR-178）选中某 env 时，可选 namespace 收窄为该 env 映射的集合；「全部环境」则不收窄。

import { useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Label, Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

import { fetchNamespaces } from '../../api/cluster'
import { useEnvNamespaceScope } from '../env/use-env-scope'

interface NamespaceSelectProps {
  value: number | null
  onChange: (namespaceId: number) => void
}

export default function NamespaceSelect({ value, onChange }: NamespaceSelectProps) {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['namespaces'], queryFn: fetchNamespaces })
  const envScope = useEnvNamespaceScope()
  const allItems = query.data?.items ?? []
  // env 过滤器收窄：选中 env 时只保留其映射的 namespace；「全部环境」（null）不收窄
  const items = useMemo(
    () => (envScope === null ? allItems : allItems.filter((ns) => envScope.includes(ns.id))),
    [allItems, envScope],
  )

  // 数据到达 / env 收窄变化后，若尚未选择或当前选中已不在可选集内，自动选中首个可选 namespace
  useEffect(() => {
    if (items.length > 0 && (value === null || !items.some((ns) => ns.id === value))) {
      onChange(items[0].id)
    }
  }, [value, items, onChange])

  return (
    <div className="flex items-center gap-2">
      <Label htmlFor="namespace-select" className="text-sm text-muted-foreground">
        {t('cluster.topology.filter.namespace')}
      </Label>
      <Select
        value={value === null ? '' : String(value)}
        onValueChange={(next) => {
          onChange(Number.parseInt(next, 10))
        }}
      >
        <SelectTrigger
          id="namespace-select"
          className="h-9 w-40"
          aria-label={t('cluster.topology.filter.namespace')}
        >
          <SelectValue placeholder={t('cluster.topology.filter.namespace')} />
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
