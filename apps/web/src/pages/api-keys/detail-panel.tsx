// 密钥详情面板内容（非模态右侧列）：单把密钥全字段（用途 / 角色 / 状态 / 前缀 / 过期 / 最近使用）
// + 吊销 / 重置入口（仅生效态可操作，点击后由父页开二次确认模态）。
import { useTranslation } from 'react-i18next'

import { Badge, Button } from '@beacon/ui'
import type { ApiKeyItem } from '@beacon/contracts'

import { formatIso } from '../../features/system/format'

interface DetailPanelProps {
  // 展示的密钥行
  item: ApiKeyItem
  // 请求吊销（打开二次确认模态）
  onRevoke: (row: ApiKeyItem) => void
  // 请求重置（打开二次确认模态）
  onReset: (row: ApiKeyItem) => void
}

// 密钥状态 → 语义药丸变体：生效绿 / 已过期灰 / 已吊销红。
function statusTone(status: ApiKeyItem['status']): 'ok' | 'off' | 'crit' {
  return status === 'active' ? 'ok' : status === 'expired' ? 'off' : 'crit'
}

export default function DetailPanel({ item, onRevoke, onReset }: DetailPanelProps) {
  const { t } = useTranslation()

  return (
    <div className="grid gap-3 text-sm">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-[15px] font-semibold text-ink-1">{item.name}</span>
        <Badge variant={statusTone(item.status)} className="gap-1.5">
          <span className="size-1.5 rounded-full bg-current" />
          {t(`system.apiKeys.status.${item.status}`)}
        </Badge>
      </div>

      <Field label={t('system.apiKeys.columns.role')} value={t(`system.apiKeys.role.${item.role}`)} />
      <Field label={t('system.apiKeys.columns.keyPrefix')} value={`${item.keyPrefix}…`} mono />
      <Field label={t('system.apiKeys.columns.createdAt')} value={formatIso(item.createdAt)} />
      <Field
        label={t('system.apiKeys.columns.expiresAt')}
        value={item.expiresAt === null ? t('system.apiKeys.never') : formatIso(item.expiresAt)}
      />
      <Field
        label={t('system.apiKeys.columns.lastUsedAt')}
        value={item.lastUsedAt === null ? t('system.apiKeys.neverUsed') : formatIso(item.lastUsedAt)}
      />

      {/* 吊销 / 重置：仅生效态可操作，点击后开二次确认模态 */}
      {item.status === 'active' && (
        <div className="flex flex-wrap gap-2 border-t border-border pt-3">
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              onReset(item)
            }}
          >
            {t('system.apiKeys.reset')}
          </Button>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              onRevoke(item)
            }}
          >
            {t('system.apiKeys.revoke')}
          </Button>
        </div>
      )}
    </div>
  )
}

// 单个只读字段（标签 + 值）
function Field({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className={mono ? 'font-mono text-xs text-ink-2' : 'text-sm text-ink-1'}>{value}</span>
    </div>
  )
}
