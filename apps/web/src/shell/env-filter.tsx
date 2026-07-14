// 顶栏 env 过滤器（FR-178）：全局按 env 过滤各页视图的作用域选择器。
// env 是纯展示 / 过滤维度——切换只影响前端视图取数范围，绝不改任何权威数据。
// 切换后全量失效查询，让依赖作用域的页面（经 NamespaceSelect 收窄）重取数据，与场景切换器同一失效策略。
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { Layers } from 'lucide-react'

import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@beacon/ui'

import { useEnvOptions } from '../features/env/use-env-scope'
import { ALL_ENVS, setEnvFilter, useEnvFilter } from '../state/env-filter'

export default function EnvFilter() {
  const { t } = useTranslation()
  const queryClient = useQueryClient()
  const envId = useEnvFilter()
  const envs = useEnvOptions()

  const handleChange = (value: string) => {
    const next = Number.parseInt(value, 10)
    setEnvFilter(Number.isNaN(next) ? ALL_ENVS : next)
    // 切换 env 作用域后让全部查询失效重取（依赖 env→namespace 收窄的页面随之刷新）
    void queryClient.invalidateQueries()
  }

  return (
    <Select value={String(envId)} onValueChange={handleChange}>
      <SelectTrigger aria-label={t('common.envFilter.label')} size="sm" className="w-44 gap-1.5">
        <Layers className="size-3.5 shrink-0 text-ink-4" aria-hidden />
        <SelectValue placeholder={t('common.envFilter.all')} />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={String(ALL_ENVS)}>{t('common.envFilter.all')}</SelectItem>
        {envs.map((env) => (
          <SelectItem key={env.id} value={String(env.id)}>
            {env.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}
