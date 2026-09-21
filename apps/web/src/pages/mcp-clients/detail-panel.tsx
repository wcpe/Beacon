// MCP 客户端详情面板内容（非模态右侧列）：
// 身份字段（clientId / profile / secret 前缀与版本）+ 生命周期时间戳 + profile 能力说明，
// 底部按状态给出轮换 / 启用 / 吊销入口（点击后由父页开确认模态）。
import { useTranslation } from 'react-i18next'

import { Badge, Button } from '@beacon/ui'
import type { MCPClientItem } from '@beacon/contracts'

import { formatIso } from '../../features/system/format'

interface DetailPanelProps {
  item: MCPClientItem
  // 请求轮换（打开确认模态）
  onRotate: (row: MCPClientItem) => void
  // 请求启用（打开确认模态）
  onEnable: (row: MCPClientItem) => void
  // 请求吊销（打开二次确认模态）
  onRevoke: (row: MCPClientItem) => void
}

// 状态 → 语义药丸：生效绿 / 已吊销红。
function statusTone(status: MCPClientItem['status']): 'ok' | 'crit' {
  return status === 'active' ? 'ok' : 'crit'
}

export default function DetailPanel({ item, onRotate, onEnable, onRevoke }: DetailPanelProps) {
  const { t } = useTranslation()
  const active = item.status === 'active'

  return (
    <div className="grid gap-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[15px] font-semibold text-ink-1">{item.displayName}</span>
        <Badge variant={statusTone(item.status)} className="gap-1.5">
          <span className="size-1.5 rounded-full bg-current" />
          {t(`system.mcpClients.status.${item.status}`)}
        </Badge>
      </div>

      <Field label={t('system.mcpClients.clientId')} value={item.clientId} mono />
      <Field
        label={t('system.mcpClients.columns.profile')}
        value={`${t(`system.mcpClients.profile.${item.profile}`)} · ${item.profile}`}
      />
      <Field
        label={t('system.mcpClients.columns.secretPrefix')}
        value={`${item.secretPrefix}…（v${String(item.secretVersion)}）`}
        mono
      />
      <Field label={t('system.mcpClients.createdBy')} value={item.createdBy} />
      <Field label={t('system.mcpClients.columns.createdAt')} value={formatIso(item.createdAt)} />
      <Field label={t('system.mcpClients.updatedAt')} value={formatIso(item.updatedAt)} />
      {item.revokedAt != null && (
        <Field label={t('system.mcpClients.revokedAt')} value={formatIso(item.revokedAt)} />
      )}

      {/* profile 能力说明：让操作者不必回查文档就知道这个客户端能做什么 */}
      <div className="rounded-lg border border-border bg-muted/40 px-3 py-2 text-xs text-ink-3">
        {t(`system.mcpClients.profileHint.${item.profile}`)}
      </div>

      {/* 生命周期动作：按状态给出可用项 */}
      <div className="flex flex-wrap gap-2 border-t border-border pt-3">
        {active ? (
          <>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                onRotate(item)
              }}
            >
              {t('system.mcpClients.rotate')}
            </Button>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                onRevoke(item)
              }}
            >
              {t('system.mcpClients.revoke')}
            </Button>
          </>
        ) : (
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              onEnable(item)
            }}
          >
            {t('system.mcpClients.enable')}
          </Button>
        )}
      </div>
    </div>
  )
}

// 单个只读字段（标签 + 值）
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className={mono ? 'font-mono text-xs break-all text-ink-2' : 'text-sm text-ink-1'}>{value}</span>
    </div>
  )
}
