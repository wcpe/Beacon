// 全局环境选择器（FR-105）：两层页眉右侧「环境槽」的控件，仅环境范围页渲染。
// 值绑定全局环境 store（useEnvironment / setEnvironment），切换后跨页保持、刷新仍在（localStorage）。
// 复用既有 Combobox + listNamespaces + namespaceOptions；含「全部环境」选项（值为空串）。

import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { useQuery } from '@tanstack/react-query'
import { Globe } from 'lucide-react'
import { listNamespaces } from '@/api/client'
import { namespaceOptions } from '@/api/format'
import { Combobox } from '@/components/ui/combobox'
import { useEnvironment, setEnvironment } from '@/state/environment'

export default function EnvSelector() {
  const { t } = useTranslation()
  const environment = useEnvironment()

  // 环境候选来自 listNamespaces；候选显示「编码 · 名称」，真实值仍是 code（FR-70）。
  const namespacesQuery = useQuery({ queryKey: ['namespaces'], queryFn: () => listNamespaces() })

  // 候选首项固定为「全部环境」（值为空串），其后接各环境；严格下拉，只在已知候选中选。
  const options = useMemo(
    () => [{ value: '', label: t('pageHeader.allEnvironments') }, ...namespaceOptions(namespacesQuery.data)],
    [namespacesQuery.data, t],
  )

  return (
    <div className="flex items-center gap-1.5">
      {/* 环境图标前缀：一眼可辨此为环境选择 */}
      <Globe aria-hidden className="size-4 shrink-0 text-muted-foreground" />
      {/* 紧凑尺寸的严格下拉：值即全局环境，留空＝全部环境 */}
      <Combobox
        value={environment}
        onChange={setEnvironment}
        options={options}
        allowCustom={false}
        aria-label={t('pageHeader.envSelectorLabel')}
        className="w-44"
      />
    </div>
  )
}
