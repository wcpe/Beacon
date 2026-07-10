// 向导第 2 步：选黄金模板源并扫描差异。原生 select 列出运行中子服（便于测试），
// 点「扫描差异」由父级懒建 draft 单并调 diff-scan，差异清单带语义色计数徽标展示。
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { ScanSearch } from 'lucide-react'

import { Badge, Button, Label, cn } from '@beacon/ui'

import { fetchServers } from '../../api/cluster'
import type { ChangeOrderItem } from '../../api/delivery-changes'
import { formatBytes, formatTime } from './format'

/** 扫描结果（父级持有，步骤往返保留） */
export interface ScanResult {
  items: ChangeOrderItem[]
  snapshotAt: string | null
}

interface StepSourceProps {
  namespaceId: number
  source: string
  onSourceChange: (serverId: string) => void
  scan: ScanResult | null
  scanning: boolean
  onScan: () => void
  errorText: string | null
}

// 差异动作 → 语义色徽标变体（新增 ok / 修改 warn / 删除 crit）
const ACTION_VARIANT = { add: 'ok', update: 'warn', delete: 'crit' } as const

export default function WizardStepSource({
  namespaceId,
  source,
  onSourceChange,
  scan,
  scanning,
  onScan,
  errorText,
}: StepSourceProps) {
  const { t } = useTranslation()

  // 运行中且已分配小区的 backend 子服作为模板源候选
  const serversQuery = useQuery({
    queryKey: ['change-orders', 'wizard-servers', namespaceId],
    queryFn: () => fetchServers({ namespaceId, kind: 'backend', assigned: true, pageSize: 200 }),
  })
  const candidates = (serversQuery.data?.items ?? []).filter((s) => s.online)

  const counts = countActions(scan?.items ?? [])

  return (
    <div className="grid gap-3">
      <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.source.lead')}</p>

      <div className="flex flex-wrap items-end gap-2">
        <div className="grid gap-1.5">
          <Label htmlFor="wizard-source-server">{t('delivery.changes.wizard.source.pickLabel')}</Label>
          <select
            id="wizard-source-server"
            aria-label={t('delivery.changes.wizard.source.pickLabel')}
            value={source}
            onChange={(e) => {
              onSourceChange(e.target.value)
            }}
            className="h-9 w-64 rounded-md border bg-background px-2 text-sm"
          >
            <option value="">{t('delivery.changes.wizard.source.pickPlaceholder')}</option>
            {candidates.map((s) => (
              <option key={s.serverId} value={s.serverId}>
                {s.serverId}
                {s.zoneName === null ? '' : ` · ${s.zoneName}`}
              </option>
            ))}
          </select>
        </div>
        <Button size="sm" disabled={source === '' || scanning} onClick={onScan}>
          <ScanSearch className="size-4" />
          {scanning
            ? t('delivery.changes.wizard.source.scanning')
            : scan === null
              ? t('delivery.changes.wizard.source.scan')
              : t('delivery.changes.wizard.source.rescan')}
        </Button>
      </div>

      {candidates.length === 0 && !serversQuery.isLoading && (
        <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.source.noServer')}</p>
      )}
      {errorText !== null && <p className="text-sm text-destructive">{errorText}</p>}

      {scan === null ? (
        <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
          {t('delivery.changes.wizard.source.empty')}
        </p>
      ) : (
        <div className="grid gap-2">
          {/* 计数徽标 + 快照时间 */}
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <Badge variant="ok">{t('delivery.changes.wizard.source.addCount', { count: counts.add })}</Badge>
            <Badge variant="warn">
              {t('delivery.changes.wizard.source.updateCount', { count: counts.update })}
            </Badge>
            <Badge variant="crit">
              {t('delivery.changes.wizard.source.deleteCount', { count: counts.delete })}
            </Badge>
            <span className="text-ink-3">
              {t('delivery.changes.wizard.source.snapshotAt', { at: formatTime(scan.snapshotAt) })}
            </span>
          </div>
          {/* 差异文件清单 */}
          <ul className="max-h-56 divide-y divide-border overflow-y-auto rounded-lg border border-border">
            {scan.items.map((item) => (
              <li key={item.id} className="flex items-center gap-2 px-3 py-1.5 text-sm">
                <Badge variant={ACTION_VARIANT[item.action ?? 'update']} className="w-12 justify-center">
                  {t(`delivery.changes.wizard.source.action${actionKey(item.action)}`)}
                </Badge>
                <span className={cn('min-w-0 flex-1 truncate font-mono text-xs', item.action === 'delete' && 'line-through opacity-70')}>
                  {item.path}
                </span>
                <span className="tnum shrink-0 text-xs text-ink-3">
                  {item.sizeBytes === null ? '-' : formatBytes(item.sizeBytes)}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

// 差异清单按动作计数
function countActions(items: ChangeOrderItem[]): { add: number; update: number; delete: number } {
  const counts = { add: 0, update: 0, delete: 0 }
  for (const item of items) {
    if (item.action !== null) {
      counts[item.action] += 1
    }
  }
  return counts
}

// 动作 → i18n 键后缀
function actionKey(action: ChangeOrderItem['action']): 'Add' | 'Update' | 'Delete' {
  if (action === 'add') {
    return 'Add'
  }
  if (action === 'delete') {
    return 'Delete'
  }
  return 'Update'
}
