// 作用域选择条（/agent-settings 页顶）：namespace（必选）+ server（可选，留空 = namespace 默认层）。
//
// 两个控件的语义差别是本页的核心概念，必须在界面上说清楚：
// - 只选 namespace → 读写的都是该 namespace 的默认值，对该域内未单独覆盖的服务器生效；
// - 再选一台 server → 读写该服务器的覆盖层，优先级高于 namespace 默认。
// 候选都来自既有端点（fetchNamespaces / fetchServers），不新增契约面。
import { useTranslation } from 'react-i18next'

import { Badge, Button, Combobox, type ComboboxOption } from '@beacon/ui'
import type { NamespaceItem, ServerItem } from '@beacon/contracts'

import { formatIso } from '../../features/system/format'

export interface ScopeBarProps {
  namespaces: NamespaceItem[]
  namespaceId: number
  onNamespaceChange: (namespaceId: number) => void
  namespacePending: boolean
  namespaceError: string | null
  onRetryNamespace: () => void
  servers: ServerItem[]
  serverId: string
  onServerChange: (serverId: string) => void
  serverPending: boolean
  /** 该作用域最近一次下发时间；空串 = 从未下发 */
  updatedAt: string
}

export default function ScopeBar({
  namespaces,
  namespaceId,
  onNamespaceChange,
  namespacePending,
  namespaceError,
  onRetryNamespace,
  servers,
  serverId,
  onServerChange,
  serverPending,
  updatedAt,
}: ScopeBarProps) {
  const { t } = useTranslation()

  // namespace 候选：显示名优先，回退 code / name（与 /namespaces 页口径一致）
  const namespaceOptions: ComboboxOption[] = namespaces.map((item) => ({
    value: String(item.id),
    label: item.displayName ?? item.code ?? item.name,
  }))

  const serverOptions: ComboboxOption[] = servers.map((item) => ({ value: item.serverId, label: item.serverId }))

  return (
    <div className="grid gap-3 rounded-xl border border-border bg-card p-4 shadow-card">
      {/* 作用域说明放在选择器之上：先讲清「读的是哪一层」，再让用户选 */}
      <p className="text-xs leading-relaxed text-ink-3">{t('system.agentSettings.mission')}</p>
      <div className="grid gap-3 sm:grid-cols-2">
        <div className="grid min-w-0 gap-1.5">
          <span className="text-xs font-medium text-ink-2">{t('system.agentSettings.scope.namespace')}</span>
          <Combobox
            aria-label={t('system.agentSettings.scope.namespace')}
            value={namespaceId > 0 ? String(namespaceId) : ''}
            onChange={(next) => {
              onNamespaceChange(Number.parseInt(next, 10))
            }}
            options={namespaceOptions}
            allowCustom={false}
            placeholder={t('system.agentSettings.scope.namespacePlaceholder')}
            emptyText={namespacePending ? t('system.agentSettings.scope.namespaceLoading') : t('system.agentSettings.scope.namespaceEmpty')}
            disabled={namespacePending}
          />
          {namespaceError !== null && (
            <div role="alert" className="flex flex-wrap items-center gap-2 text-xs text-destructive">
              <span className="min-w-0 flex-1">{namespaceError}</span>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="h-auto px-1 py-0 text-xs text-destructive"
                onClick={onRetryNamespace}
              >
                {t('system.agentSettings.retry')}
              </Button>
            </div>
          )}
        </div>

        <div className="grid min-w-0 gap-1.5">
          <span className="text-xs font-medium text-ink-2">{t('system.agentSettings.scope.server')}</span>
          <Combobox
            aria-label={t('system.agentSettings.scope.server')}
            value={serverId}
            onChange={onServerChange}
            options={serverOptions}
            allowCustom={false}
            placeholder={t('system.agentSettings.scope.serverPlaceholder')}
            emptyText={serverPending ? t('system.agentSettings.scope.serverLoading') : t('system.agentSettings.scope.serverEmpty')}
            clearable
            clearLabel={t('system.agentSettings.scope.serverClear')}
            disabled={namespaceId <= 0 || serverPending}
          />
        </div>
      </div>

      {/* 当前作用域语义 + 最近下发时间：读的是哪一层、写会落到哪一层，别让运维靠猜 */}
      <div className="flex flex-wrap items-center gap-2 text-xs text-ink-3">
        <Badge variant={serverId === '' ? 'outline' : 'brand'}>
          {serverId === ''
            ? t('system.agentSettings.scope.hintNamespace')
            : t('system.agentSettings.scope.hintServer')}
        </Badge>
        <span>
          {updatedAt === ''
            ? t('system.agentSettings.scope.neverUpdated')
            : t('system.agentSettings.scope.updatedAt', { time: formatIso(updatedAt) })}
        </span>
      </div>
    </div>
  )
}
