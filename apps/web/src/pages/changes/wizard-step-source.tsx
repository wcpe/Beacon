// 向导第 2 步：选黄金模板源并扫描差异。候选以可筛选列表展示（搜索框按 serverId /
// 大区 / 小区名即输即滤，单选行），点「扫描差异」由父级懒建 draft 单并调 diff-scan，
// 差异清单带语义色计数徽标展示。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { ScanSearch } from 'lucide-react'

import { Badge, Button, Input, cn } from '@beacon/ui'

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
  const [keyword, setKeyword] = useState('')

  // 运行中且已分配小区的 backend 子服作为模板源候选
  const serversQuery = useQuery({
    queryKey: ['change-orders', 'wizard-servers', namespaceId],
    queryFn: () => fetchServers({ namespaceId, kind: 'backend', assigned: true, pageSize: 200 }),
  })
  const candidates = (serversQuery.data?.items ?? []).filter((s) => s.online)

  // 即输即滤：按 serverId / 小区名 / 大区名匹配（大小写不敏感）
  const needle = keyword.trim().toLowerCase()
  const filtered =
    needle === ''
      ? candidates
      : candidates.filter((s) =>
          [s.serverId, s.zoneName ?? '', s.regionName ?? ''].some((text) =>
            text.toLowerCase().includes(needle),
          ),
        )

  const counts = countActions(scan?.items ?? [])

  return (
    <div className="grid gap-3">
      <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.source.lead')}</p>

      {/* 搜索框 + 扫描按钮 */}
      <div className="flex flex-wrap items-center gap-2">
        <Input
          aria-label={t('delivery.changes.wizard.source.filter')}
          placeholder={t('delivery.changes.wizard.source.filterPlaceholder')}
          value={keyword}
          onChange={(e) => {
            setKeyword(e.target.value)
          }}
          className="h-9 w-64"
        />
        <Button size="sm" disabled={source === '' || scanning} onClick={onScan}>
          <ScanSearch className="size-4" />
          {scanning
            ? t('delivery.changes.wizard.source.scanning')
            : scan === null
              ? t('delivery.changes.wizard.source.scan')
              : t('delivery.changes.wizard.source.rescan')}
        </Button>
        {source !== '' && (
          <span className="text-xs text-ink-3">
            {t('delivery.changes.wizard.source.picked', { serverId: source })}
          </span>
        )}
      </div>

      {/* 候选列表（单选） */}
      {candidates.length === 0 && !serversQuery.isLoading ? (
        <p className="text-sm text-muted-foreground">{t('delivery.changes.wizard.source.noServer')}</p>
      ) : filtered.length === 0 ? (
        <p className="rounded-lg border border-dashed border-border px-3 py-4 text-center text-sm text-muted-foreground">
          {t('delivery.changes.wizard.source.noMatch')}
        </p>
      ) : (
        <ul
          role="radiogroup"
          aria-label={t('delivery.changes.wizard.source.pickLabel')}
          className="grid max-h-44 gap-0.5 overflow-y-auto rounded-lg border border-border p-1.5 sm:grid-cols-2"
        >
          {filtered.map((s) => {
            const selected = s.serverId === source
            return (
              <li key={s.serverId}>
                <button
                  type="button"
                  role="radio"
                  aria-checked={selected}
                  onClick={() => {
                    onSourceChange(s.serverId)
                  }}
                  className={cn(
                    'flex w-full cursor-pointer items-center gap-2 rounded-md px-2 py-1 text-left text-sm hover:bg-muted',
                    selected && 'bg-brand-50/60 ring-1 ring-brand',
                  )}
                >
                  <span
                    aria-hidden
                    className={cn(
                      'grid size-3.5 shrink-0 place-items-center rounded-full border',
                      selected ? 'border-brand' : 'border-ink-3/50',
                    )}
                  >
                    {selected && <span className="size-2 rounded-full bg-brand" />}
                  </span>
                  <span className="min-w-0 flex-1 truncate font-mono text-xs">{s.serverId}</span>
                  {s.zoneName !== null && (
                    <span className="shrink-0 text-xs text-ink-3">
                      {s.regionName === null ? s.zoneName : `${s.regionName} / ${s.zoneName}`}
                    </span>
                  )}
                </button>
              </li>
            )
          })}
        </ul>
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
