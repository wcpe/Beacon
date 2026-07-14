// env 详情面板（FR-178）：展示该 env 概要、映射的 namespace，以及编辑 / 设置映射 / 删除入口。
import { useTranslation } from 'react-i18next'

import type { EnvItem } from '@beacon/contracts'
import { Badge, Button } from '@beacon/ui'

import { formatIso } from '../../features/system/format'

interface EnvDetailPanelProps {
  env: EnvItem
  onEdit: () => void
  onSetMapping: () => void
  onDelete: () => void
}

export default function EnvDetailPanel({ env, onEdit, onSetMapping, onDelete }: EnvDetailPanelProps) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-4">
      <div>
        <h3 className="text-base font-semibold text-ink-1">{env.name}</h3>
        {env.description && <p className="mt-1 text-sm text-ink-2">{env.description}</p>}
      </div>

      <div className="flex flex-wrap gap-2">
        <Button size="sm" variant="outline" onClick={onEdit}>
          {t('system.envs.edit')}
        </Button>
        <Button size="sm" variant="outline" onClick={onSetMapping}>
          {t('system.envs.mapping')}
        </Button>
        <Button size="sm" variant="destructive" onClick={onDelete}>
          {t('system.envs.delete')}
        </Button>
      </div>

      <div className="grid gap-2">
        <span className="text-[13px] font-semibold text-ink-1">{t('system.envs.mappedNamespaces')}</span>
        {env.namespaces.length === 0 ? (
          <p className="text-sm text-ink-3">{t('system.envs.noNamespaces')}</p>
        ) : (
          <div className="flex flex-wrap gap-1.5">
            {env.namespaces.map((ns) => (
              <Badge key={ns.id} variant="secondary">
                {ns.name}
              </Badge>
            ))}
          </div>
        )}
      </div>

      <div className="text-xs text-ink-4">
        {t('system.envs.columns.updatedAt')}: {formatIso(env.updatedAt)}
      </div>
    </div>
  )
}
