// 顶栏 env 过滤器（FR-178 + FR-192）：截图式 Dropdown，无底触发器 + 淡入淡出菜单。
// env 是纯展示 / 过滤维度——切换只影响前端视图取数范围，绝不改任何权威数据。
// 切换后全量失效查询，与场景切换器同一失效策略。
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { Check, ChevronDown, Layers } from 'lucide-react'

import {
  Badge,
  Button,
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@beacon/ui'

import { useEnvOptions } from '../features/env/use-env-scope'
import { ALL_ENVS, setEnvFilter, useEnvFilter } from '../state/env-filter'

export default function EnvFilter() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const envId = useEnvFilter()
  const envs = useEnvOptions()

  const selected =
    envId === ALL_ENVS ? null : (envs.find((item) => item.id === envId) ?? null)
  const triggerLabel = selected === null ? t('common.envFilter.all') : selected.name
  const triggerBadge =
    selected === null ? t('common.envFilter.badgeAll') : t('common.envFilter.badgeEnv')

  const pick = (next: number) => {
    setEnvFilter(next)
    void queryClient.invalidateQueries()
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          aria-label={t('common.envFilter.label')}
          className="h-8 gap-1.5 border-0 bg-transparent px-2 shadow-none hover:bg-muted"
          data-slot="env-filter-trigger"
        >
          <Layers className="size-3.5 shrink-0 text-ink-4" aria-hidden />
          <span className="max-w-[9rem] truncate text-[13px] text-ink-1">{triggerLabel}</span>
          <Badge variant="secondary" className="h-5 px-1.5 text-[10px] font-semibold">
            {triggerBadge}
          </Badge>
          <ChevronDown className="size-3.5 shrink-0 text-ink-4" aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent
        align="start"
        sideOffset={6}
        className="min-w-[220px] p-1.5 data-open:animate-in data-open:fade-in-0 data-open:zoom-in-95 data-closed:animate-out data-closed:fade-out-0 data-closed:zoom-out-95"
        data-slot="env-filter-menu"
      >
        <EnvMenuRow
          active={envId === ALL_ENVS}
          label={t('common.envFilter.all')}
          badge={t('common.envFilter.badgeAll')}
          badgeVariant="secondary"
          onSelect={() => {
            pick(ALL_ENVS)
          }}
        />
        {envs.map((env) => (
          <EnvMenuRow
            key={env.id}
            active={envId === env.id}
            label={env.name}
            badge={t('common.envFilter.badgeEnv')}
            badgeVariant="brand"
            onSelect={() => {
              pick(env.id)
            }}
          />
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function EnvMenuRow({
  active,
  label,
  badge,
  badgeVariant,
  onSelect,
}: {
  active: boolean
  label: string
  badge: string
  badgeVariant: 'secondary' | 'brand'
  onSelect: () => void
}) {
  return (
    <DropdownMenuItem
      className={[
        'cursor-pointer gap-2 rounded-lg px-2 py-2',
        active ? 'bg-brand-50 text-ink-1 focus:bg-brand-50' : '',
      ].join(' ')}
      onSelect={onSelect}
    >
      <span
        className={[
          'grid size-7 shrink-0 place-items-center rounded-full',
          active ? 'bg-brand-100 text-brand' : 'bg-surface-2 text-ink-4',
        ].join(' ')}
        aria-hidden
      >
        <Layers className="size-3.5" />
      </span>
      <span className="min-w-0 flex-1 truncate text-[13px] font-medium">{label}</span>
      <Badge variant={badgeVariant} className="h-5 shrink-0 px-1.5 text-[10px] font-semibold">
        {badge}
      </Badge>
      {active ? <Check className="size-3.5 shrink-0 text-brand" aria-hidden /> : null}
    </DropdownMenuItem>
  )
}
