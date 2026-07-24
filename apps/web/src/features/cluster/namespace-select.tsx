// namespace 作用域选择器（/zones /topology /servers 等共用）：
// - 顶栏 env =「全部环境」时：首项为「全部命名空间」(id=0)，API 不传 / 传 0 取全量
// - 顶栏 env = 具体环境时：仅列出该 env 映射的 namespace，自动落到有服优先的首个
// FR-178：env 收窄只影响可选集合与默认值，绝不改权威数据。

import { useEffect, useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Label, Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

import { fetchNamespaces } from '../../api/cluster'
import { useEnvNamespaceScope } from '../env/use-env-scope'

/** 哨兵：全部命名空间（与后端 zone-tree?namespaceId=0 全量语义对齐） */
export const ALL_NAMESPACES = 0

interface NamespaceSelectProps {
  value: number | null
  /** 含 ALL_NAMESPACES(0) 表示全量 */
  onChange: (namespaceId: number) => void
  /** 是否在「全部环境」下提供「全部命名空间」选项；默认 true */
  allowAll?: boolean
}

export default function NamespaceSelect({ value, onChange, allowAll = true }: NamespaceSelectProps) {
  const { t } = useTranslation()
  const query = useQuery({ queryKey: ['namespaces'], queryFn: fetchNamespaces })
  const envScope = useEnvNamespaceScope()
  const allItems = query.data?.items ?? []
  // env 过滤器收窄：选中 env 时只保留其映射的 namespace；「全部环境」（null）不收窄
  const items = useMemo(
    () => (envScope === null ? allItems : allItems.filter((ns) => envScope.includes(ns.id))),
    [allItems, envScope],
  )
  // 仅「全部环境」且 allowAll 时展示「全部命名空间」
  const showAllOption = allowAll && envScope === null

  // 数据到达 / env 变化后校准选中值
  useEffect(() => {
    if (query.isLoading) {
      return
    }
    // env 收窄且无任何 ns：无法选择
    if (envScope !== null && items.length === 0) {
      return
    }
    // 当前值合法则保留
    if (value === ALL_NAMESPACES && showAllOption) {
      return
    }
    if (value !== null && value !== ALL_NAMESPACES && items.some((ns) => ns.id === value)) {
      return
    }
    // 全部环境：默认「全部命名空间」，确保选「全部环境」后可见全量
    if (showAllOption) {
      onChange(ALL_NAMESPACES)
      return
    }
    // 具体 env：优先有服的 ns，否则第一个
    const withServers = items.find((ns) => ns.serverCount > 0)
    if (withServers) {
      onChange(withServers.id)
      return
    }
    if (items.length > 0) {
      onChange(items[0].id)
    }
  }, [value, items, onChange, showAllOption, envScope, query.isLoading])

  const selectValue =
    value === null
      ? ''
      : value === ALL_NAMESPACES && showAllOption
        ? String(ALL_NAMESPACES)
        : String(value)

  return (
    <div className="flex items-center gap-2">
      <Label htmlFor="namespace-select" className="text-sm text-muted-foreground">
        {t('cluster.topology.filter.namespace')}
      </Label>
      <Select
        value={selectValue}
        onValueChange={(next) => {
          onChange(Number.parseInt(next, 10))
        }}
      >
        <SelectTrigger
          id="namespace-select"
          className="h-9 w-44"
          aria-label={t('cluster.topology.filter.namespace')}
          data-slot="namespace-select"
        >
          <SelectValue placeholder={t('cluster.topology.filter.namespace')} />
        </SelectTrigger>
        <SelectContent>
          {showAllOption ? (
            <SelectItem value={String(ALL_NAMESPACES)}>{t('cluster.topology.filter.allNamespaces')}</SelectItem>
          ) : null}
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
