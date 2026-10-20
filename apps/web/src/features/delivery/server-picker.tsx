// 交付域共用「服务器选择」控件：把原先手填 serverId 的输入框换成可搜索下拉。
// 候选来自集群域 fetchServers（只拉当前 namespace 的一页已分配后端服，量级受 pageSize 约束），
// 键入过滤交给 @beacon/ui 的 Combobox 做子串匹配；strict 模式保证上报值一定是系统内真实存在的服，
// 避免手填拼错导致审批 / 比对 / 模板源整条链路查不到对象。
// 大集群保护：只取一页候选并缓存（staleTime），不随键入重新拉全量；
// 当前值若不在候选内（历史值、跨 namespace、超出一页）会被补进候选，保证回显与可清空。
import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { Button, Combobox, type ComboboxOption } from '@beacon/ui'
import type { ServerItem, ServerKind } from '@beacon/contracts'

import { fetchServers } from '../../api/cluster'

// 单次拉取的候选上限：一页够用，避免把上千子服全量塞进 DOM
const CANDIDATE_PAGE_SIZE = 200

export interface ServerPickerProps {
  // 当前 namespace 作用域；<=0 表示页面还没选定（此时不按 namespace 过滤，交后端全量）
  namespaceId: number
  value: string
  onChange: (serverId: string) => void
  // 候选口径：默认只列后端服
  kind?: ServerKind
  // 是否只列已分配小区的服；不传表示不按分配状态过滤。
  // 文件清单 / 差异比对的候选不传（未分配的后端服同样有真实清单），模板源传 true（业务上必须是已分配目标）
  assigned?: boolean
  // 只在在线服里选（模板源必须能读到真实文件）
  onlineOnly?: boolean
  id?: string
  'aria-label'?: string
  placeholder?: string
  className?: string
  disabled?: boolean
  // 筛选类场景传 true，支持一键清空回「全部」
  clearable?: boolean
}

export default function ServerPicker({
  namespaceId,
  value,
  onChange,
  kind = 'backend',
  assigned,
  onlineOnly = false,
  id,
  'aria-label': ariaLabel,
  placeholder,
  className,
  disabled,
  clearable,
}: ServerPickerProps) {
  const { t } = useTranslation()

  const query = useQuery({
    queryKey: ['delivery', 'server-picker', namespaceId, kind, assigned, onlineOnly],
    queryFn: () =>
      fetchServers({
        // namespaceId=0 在后端语义里等于「全部命名空间」，显式传会更明确
        namespaceId: namespaceId > 0 ? namespaceId : undefined,
        kind,
        assigned,
        pageSize: CANDIDATE_PAGE_SIZE,
      }),
    // 服列表变化慢，缓存一分钟即可，避免四处选择器各自重复拉取
    staleTime: 60_000,
  })

  const options = useMemo<ComboboxOption[]>(() => {
    const items = (query.data?.items ?? []).filter((server) => !onlineOnly || server.online)
    const list = items.map((server) => ({ value: server.serverId, label: serverLabel(server, t) }))
    // 当前值不在候选内时补一项，保证既有值（历史 / 跨 ns / 超出一页）也能正确回显
    if (value !== '' && !list.some((option) => option.value === value)) {
      list.unshift({ value, label: value })
    }
    return list
  }, [query.data, onlineOnly, value, t])

  const loadError = query.error instanceof Error ? query.error.message : String(query.error)

  return (
    <div className="grid min-w-0 gap-1.5">
      <Combobox
        id={id}
        aria-label={ariaLabel ?? t('delivery.pickers.server.search')}
        value={value}
        onChange={onChange}
        options={options}
        // 这些位置必须选到系统内真实存在的服，不允许提交候选外的新值
        allowCustom={false}
        placeholder={placeholder ?? t('delivery.pickers.server.search')}
        emptyText={query.isLoading ? t('delivery.pickers.server.loading') : query.isError ? t('delivery.pickers.server.emptyError') : t('delivery.pickers.server.empty')}
        clearable={clearable}
        clearLabel={t('delivery.pickers.server.clear')}
        disabled={disabled}
        className={className}
      />
      {query.isError ? (
        <div role="alert" className="flex flex-wrap items-center gap-2 text-xs text-destructive">
          <span className="min-w-0 flex-1">{t('delivery.pickers.server.loadError', { message: loadError })}</span>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-auto px-1 py-0 text-xs text-destructive"
            onClick={() => { void query.refetch() }}
          >
            {t('delivery.pickers.server.retry')}
          </Button>
        </div>
      ) : null}
    </div>
  )
}

// 候选项显示文本：serverId 为主，大区 / 小区 / BC 集群作为辅助信息便于区分同名前缀服
function serverLabel(server: ServerItem, t: ReturnType<typeof useTranslation>['t']): string {
  const place = [server.regionName, server.zoneName ?? server.bcClusterName]
    .filter((part): part is string => part !== null && part !== '')
    .join(' / ')
  return place === ''
    ? server.serverId
    : t('delivery.pickers.server.optionLabel', { serverId: server.serverId, scope: place })
}
