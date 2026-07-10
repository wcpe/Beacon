// namespace 详情面板内容（非模态右侧列）：该 namespace 概要 + 其互通信任关系（出向 / 入向）+ 授予 / 收回入口。
// 原「信任面板」并列铺开的布局收进本面板；授予 / 收回的原因必填表单仍由父页走模态。
import { useTranslation } from 'react-i18next'
import { ArrowRight, Handshake } from 'lucide-react'

import { Badge, Button } from '@beacon/ui'
import type { NamespaceItem, NamespaceTrustItem, TrustCapability } from '@beacon/devmock'

import { formatIso } from '../../features/system/format'

interface NamespaceDetailPanelProps {
  // 选中的 namespace
  item: NamespaceItem
  // 全量信任行（父页一次取得），面板内按方向过滤
  trusts: NamespaceTrustItem[]
  // 请求授予信任（打开授予模态）
  onGrant: () => void
  // 请求收回某条信任（打开原因必填模态）
  onRevoke: (trust: NamespaceTrustItem) => void
}

export default function NamespaceDetailPanel({ item, trusts, onGrant, onRevoke }: NamespaceDetailPanelProps) {
  const { t } = useTranslation()

  const capabilityLabel = (cap: TrustCapability): string => {
    if (cap === 'schedule') {
      return t('system.namespaces.trusts.capabilitySchedule')
    }
    if (cap === 'message') {
      return t('system.namespaces.trusts.capabilityMessage')
    }
    return t('system.namespaces.trusts.capabilityAgentOps')
  }

  // 出向：本域作为来源，可操作目标域；入向：本域作为目标，被来源域操作。
  const outbound = trusts.filter((tr) => tr.fromNamespaceId === item.id)
  const inbound = trusts.filter((tr) => tr.toNamespaceId === item.id)

  const relationRow = (tr: NamespaceTrustItem, direction: 'out' | 'in') => {
    const other = direction === 'out' ? tr.toNamespaceName : tr.fromNamespaceName
    return (
      <div key={`${direction}-${String(tr.id)}`} className="grid gap-1.5 rounded-lg bg-surface-2 px-3 py-2.5">
        <div className="flex items-center gap-1.5 text-sm font-medium text-ink-1">
          {direction === 'out' ? (
            <>
              <span>{item.name}</span>
              <ArrowRight className="size-3.5 text-ink-4" aria-hidden />
              <span>{other}</span>
            </>
          ) : (
            <>
              <span>{other}</span>
              <ArrowRight className="size-3.5 text-ink-4" aria-hidden />
              <span>{item.name}</span>
            </>
          )}
        </div>
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <span className="text-ink-3">{capabilityLabel(tr.capability)}</span>
          <Badge variant={tr.status === 'active' ? 'ok' : 'off'} className="gap-1.5">
            <span className="size-1.5 rounded-full bg-current" />
            {tr.status === 'active'
              ? t('system.namespaces.trusts.statusActive')
              : t('system.namespaces.trusts.statusRevoked')}
          </Badge>
          {tr.status === 'active' && (
            <Button
              size="sm"
              variant="ghost"
              className="ml-auto h-7"
              onClick={() => {
                onRevoke(tr)
              }}
            >
              {t('system.namespaces.trusts.revoke')}
            </Button>
          )}
        </div>
      </div>
    )
  }

  return (
    <div className="grid gap-3 text-sm">
      <div className="text-[15px] font-semibold text-ink-1">{item.name}</div>

      <Field label={t('system.namespaces.columns.description')} value={item.description || '-'} />
      <div className="grid grid-cols-3 gap-2">
        <Metric label={t('system.namespaces.columns.serverCount')} value={item.serverCount} />
        <Metric label={t('system.namespaces.columns.bcClusterCount')} value={item.bcClusterCount} />
        <Metric label={t('system.namespaces.columns.trustCount')} value={item.activeTrustCount} />
      </div>
      <Field label={t('system.namespaces.columns.createdAt')} value={formatIso(item.createdAt)} />

      {/* 互通信任关系（出向 / 入向） */}
      <div className="grid gap-2 border-t border-border pt-3">
        <div className="flex items-center gap-2">
          <Handshake className="size-4 text-ink-4" aria-hidden />
          <span className="text-[13px] font-semibold text-ink-1">{t('system.namespaces.relationsTitle')}</span>
          <Button size="sm" variant="outline" className="ml-auto h-7" onClick={onGrant}>
            {t('system.namespaces.trusts.grant')}
          </Button>
        </div>

        {outbound.length === 0 && inbound.length === 0 && (
          <p className="rounded-lg bg-surface-2 px-3 py-2.5 text-xs text-ink-3">{t('system.namespaces.noRelations')}</p>
        )}

        {outbound.length > 0 && (
          <div className="grid gap-1.5">
            <span className="text-[11px] text-ink-4">{t('system.namespaces.relationOutbound')}</span>
            {outbound.map((tr) => relationRow(tr, 'out'))}
          </div>
        )}

        {inbound.length > 0 && (
          <div className="grid gap-1.5">
            <span className="text-[11px] text-ink-4">{t('system.namespaces.relationInbound')}</span>
            {inbound.map((tr) => relationRow(tr, 'in'))}
          </div>
        )}
      </div>
    </div>
  )
}

// 单个只读字段（标签 + 值）
function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1">
      <span className="text-xs text-ink-4">{label}</span>
      <span className="text-sm text-ink-1">{value}</span>
    </div>
  )
}

// 概要指标格（弱标签 + 大数值）
function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="grid gap-0.5 rounded-lg bg-surface-2 px-3 py-2">
      <span className="text-[11px] leading-none text-ink-4">{label}</span>
      <span className="text-[15px] font-semibold leading-none tnum text-ink-1">{value}</span>
    </div>
  )
}
