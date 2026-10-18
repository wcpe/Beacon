import { useMemo, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Check, ChevronDown, Layers, Network, TriangleAlert } from 'lucide-react'

import { Badge, Button, DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from '@beacon/ui'

import { useEnvOptions } from '../features/env/use-env-scope'
import {
  ALL_OBSERVATION_ENV,
  ALL_OBSERVATION_NAMESPACE,
  deriveObservationScope,
  selectObservationEnv,
  selectObservationNamespace,
  setObservationScope,
  useObservationScopeSelection,
} from '../features/env/observation-scope'

/** FR-214 页眉观测范围选择器：驱动各观测 / 集群页按范围收窄取数（FR-178，真源见 features/env/observation-scope）。 */
export default function EnvFilter() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const selection = useObservationScopeSelection()
  const envs = useEnvOptions()
  const scope = deriveObservationScope(selection, envs)
  const namespaces = useMemo(() => {
    if (scope.kind === 'env') return envs.find((env) => env.id === scope.envId)?.namespaces ?? []
    return envs.flatMap((env) => env.namespaces)
  }, [envs, scope.envId, scope.kind])
  const selectedEnv = envs.find((env) => env.id === scope.envId)
  const selectedNamespace = namespaces.find((namespace) => namespace.id === scope.namespaceId)

  const update = (next: Parameters<typeof setObservationScope>[0]) => {
    setObservationScope(next)
    void queryClient.invalidateQueries()
  }

  return (
    <div data-slot="observation-scope-filter" className="flex min-w-0 items-center gap-1.5">
      {/* 宽度随断点递增：小屏 7rem 够放「全部环境」，sm 起 12rem。
          lg 档资源组出现会挤占空间，但实测仍余 48px，故不再放窄
          （此前 lg:max-w-[6rem] 反而比 sm 窄，导致「全部环境」被截断）。 */}
      <ScopeMenu
        triggerClassName="max-w-[7rem] sm:max-w-[12rem]"
        label={t('common.envFilter.envLabel')}
        icon={<Layers className="size-3.5 text-ink-4" aria-hidden />}
        value={scope.kind === 'invalid' ? t('common.envFilter.invalid') : selectedEnv?.name ?? t('common.envFilter.all')}
        badge={scope.kind === 'invalid' ? t('common.envFilter.needReselect') : scope.kind === 'env' ? t('common.envFilter.badgeEnv') : t('common.envFilter.badgeAll')}
        invalid={scope.kind === 'invalid'}
      >
        <ScopeItem active={scope.kind === 'all'} label={t('common.envFilter.all')} onSelect={() => { update(selectObservationEnv(selection, ALL_OBSERVATION_ENV)) }} />
        {envs.map((env) => (
          <ScopeItem key={env.id} active={scope.kind === 'env' && scope.envId === env.id} label={env.name} meta={t('common.envFilter.nsCount', { count: env.namespaces.length })} onSelect={() => { update(selectObservationEnv(selection, env.id)) }} />
        ))}
      </ScopeMenu>
      <div data-slot="observation-scope-namespace" className="hidden sm:flex">
        <ScopeMenu
          triggerClassName="max-w-[12rem]"
          label={t('common.envFilter.nsLabel')}
          icon={<Network className="size-3.5 text-ink-4" aria-hidden />}
          value={scope.kind === 'invalid' ? t('common.envFilter.invalidPick') : selectedNamespace?.name ?? (scope.kind === 'env' ? t('common.envFilter.envAllNamespaces') : t('common.envFilter.allNamespaces'))}
          badge={scope.empty ? t('common.envFilter.emptyMapping') : scope.kind === 'invalid' ? t('common.envFilter.stopped') : t('common.envFilter.scopeBadge')}
          invalid={scope.kind === 'invalid'}
          disabled={scope.kind === 'invalid'}
        >
          <ScopeItem active={scope.namespaceId === ALL_OBSERVATION_NAMESPACE} label={scope.kind === 'env' ? t('common.envFilter.envAllNamespaces') : t('common.envFilter.allNamespaces')} onSelect={() => { update(selectObservationNamespace(selection, ALL_OBSERVATION_NAMESPACE)) }} />
          {namespaces.map((namespace) => (
            <ScopeItem key={namespace.id} active={scope.namespaceId === namespace.id} label={namespace.name} meta={`#${String(namespace.id)}`} onSelect={() => { update(selectObservationNamespace(selection, namespace.id)) }} />
          ))}
          {scope.empty ? <p className="px-2 py-2 text-xs text-ink-4">{t('common.envFilter.emptyNote')}</p> : null}
        </ScopeMenu>
      </div>
      {scope.kind === 'invalid' ? <TriangleAlert className="size-4 text-warning" aria-label={t('common.envFilter.invalidRangeAria')} /> : null}
    </div>
  )
}

function ScopeMenu({ label, icon, value, badge, invalid = false, disabled = false, triggerClassName = 'max-w-[12rem]', children }: { label: string; icon: ReactNode; value: string; badge: string; invalid?: boolean; disabled?: boolean; triggerClassName?: string; children: ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="ghost" size="sm" aria-label={label} disabled={disabled} className={['h-8 min-w-0 gap-1.5 border-0 bg-transparent px-2 shadow-none hover:bg-muted', triggerClassName].join(' ')}>
          {icon}
          <span className="min-w-0 flex-1 truncate text-[13px] text-ink-1">{value}</span>
          <Badge variant={invalid ? 'destructive' : 'secondary'} className="hidden h-5 shrink-0 px-1.5 text-[10px] font-semibold sm:inline-flex lg:hidden xl:inline-flex">{badge}</Badge>
          <ChevronDown className="size-3.5 shrink-0 text-ink-4" aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" sideOffset={6} collisionPadding={12} className="min-w-[238px] max-w-[calc(100vw-1.5rem)] p-1.5" data-slot="observation-scope-menu">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ScopeItem({ active, label, meta, onSelect }: { active: boolean; label: string; meta?: string; onSelect: () => void }) {
  return <DropdownMenuItem className={active ? 'cursor-pointer gap-2 rounded-lg bg-brand-50 px-2 py-2 text-ink-1 focus:bg-brand-50' : 'cursor-pointer gap-2 rounded-lg px-2 py-2'} onSelect={onSelect}><span className="min-w-0 flex-1 truncate text-[13px] font-medium">{label}</span>{meta ? <span className="shrink-0 text-[11px] text-ink-4">{meta}</span> : null}{active ? <Check className="size-3.5 shrink-0 text-brand" aria-hidden /> : null}</DropdownMenuItem>
}
