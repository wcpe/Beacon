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

/** FR-214 页眉观测范围选择器：当前仅 mock-first 交互，不改变任何写目标。 */
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
    <div data-slot="observation-scope-filter" className="flex items-center gap-1.5">
      <ScopeMenu
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
      <ScopeMenu
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
      {scope.kind === 'invalid' ? <TriangleAlert className="size-4 text-warning" aria-label={t('common.envFilter.invalidRangeAria')} /> : null}
    </div>
  )
}

function ScopeMenu({ label, icon, value, badge, invalid = false, disabled = false, children }: { label: string; icon: ReactNode; value: string; badge: string; invalid?: boolean; disabled?: boolean; children: ReactNode }) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="ghost" size="sm" aria-label={label} disabled={disabled} className="h-8 max-w-[12rem] gap-1.5 border-0 bg-transparent px-2 shadow-none hover:bg-muted">
          {icon}
          <span className="truncate text-[13px] text-ink-1">{value}</span>
          <Badge variant={invalid ? 'destructive' : 'secondary'} className="h-5 shrink-0 px-1.5 text-[10px] font-semibold">{badge}</Badge>
          <ChevronDown className="size-3.5 shrink-0 text-ink-4" aria-hidden />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="start" sideOffset={6} className="min-w-[238px] p-1.5" data-slot="observation-scope-menu">
        {children}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}

function ScopeItem({ active, label, meta, onSelect }: { active: boolean; label: string; meta?: string; onSelect: () => void }) {
  return <DropdownMenuItem className={active ? 'cursor-pointer gap-2 rounded-lg bg-brand-50 px-2 py-2 text-ink-1 focus:bg-brand-50' : 'cursor-pointer gap-2 rounded-lg px-2 py-2'} onSelect={onSelect}><span className="min-w-0 flex-1 truncate text-[13px] font-medium">{label}</span>{meta ? <span className="shrink-0 text-[11px] text-ink-4">{meta}</span> : null}{active ? <Check className="size-3.5 shrink-0 text-brand" aria-hidden /> : null}</DropdownMenuItem>
}
