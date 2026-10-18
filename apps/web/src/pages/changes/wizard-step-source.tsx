// 向导第 2 步：选黄金模板源并扫描差异。候选以可筛选列表展示（搜索框按 serverId /
// 大区 / 小区名即输即滤，单选行），点「扫描差异」由父级懒建 draft 单并调 diff-scan，
// 差异清单带语义色计数徽标展示。
import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'

import { ChevronDown, ChevronRight, ScanSearch } from 'lucide-react'

import { Badge, Button, Input, cn } from '@beacon/ui'

import { fetchServers } from '../../api/cluster'
import type { ChangeOrderItem } from '../../api/delivery-changes'
import AssetPathPicker from '../../features/delivery/asset-path-picker'
import FileDiffPreview from '../../features/delivery/file-diff-preview'
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
  // 差异扫描的目录范围（服务器根内相对目录，如 plugins/）；改动即作废已扫差异
  scanDir: string
  onScanDirChange: (scanDir: string) => void
  scan: ScanResult | null
  scanning: boolean
  onScan: () => void
  errorText: string | null
  // 已建草稿单 id（扫描后就绪）：差异行「预览」文件内容懒加载所需
  orderId: number | null
}

// 差异动作 → 语义色徽标变体（新增 ok / 修改 warn / 删除 crit）
const ACTION_VARIANT = { add: 'ok', update: 'warn', delete: 'crit' } as const

export default function WizardStepSource({
  namespaceId,
  source,
  onSourceChange,
  scanDir,
  onScanDirChange,
  scan,
  scanning,
  onScan,
  errorText,
  orderId,
}: StepSourceProps) {
  const { t } = useTranslation()
  const [keyword, setKeyword] = useState('')
  // 展开预览的差异项 id 集合
  const [expandedIds, setExpandedIds] = useState<number[]>([])

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
      {/* 候选口径说明：与交付目标不同，模板源必须在线（要读它的实际文件） */}
      <p className="text-xs text-ink-3">{t('delivery.changes.wizard.source.candidateHint')}</p>

      {/* 搜索框 + 扫描目录范围 + 扫描按钮 */}
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
        <label className="flex items-center gap-1.5 text-xs text-ink-3">
          {t('delivery.changes.wizard.source.scanDirLabel')}
          {/* 扫描目录随已选模板源级联：列出该服真实目录，未选服时用常用目录兜底且仍可手输 */}
          <AssetPathPicker
            aria-label={t('delivery.changes.wizard.source.scanDirLabel')}
            namespaceId={namespaceId}
            serverId={source}
            value={scanDir}
            onChange={onScanDirChange}
            mode="directory"
            placeholder="plugins/"
            className="w-36"
          />
        </label>
        <Button size="sm" disabled={source === '' || scanning} onClick={onScan}>
          <ScanSearch className="size-3.5" />
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
                {/* 单选列表项：保留 role="radio" + aria-checked 暴露选中语义，
                    ghost 变体复用 Button 的焦点环 / 按压反馈，行高与排布在此覆写 */}
                <Button
                  type="button"
                  variant="ghost"
                  role="radio"
                  aria-checked={selected}
                  onClick={() => {
                    onSourceChange(s.serverId)
                  }}
                  className={cn(
                    'h-auto w-full justify-start gap-2 rounded-md px-2 py-1 text-left text-sm font-normal',
                    selected && 'bg-brand-50/60 ring-1 ring-brand hover:bg-brand-50/60',
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
                </Button>
              </li>
            )
          })}
        </ul>
      )}

      {errorText !== null && <p role="alert" className="text-sm text-destructive">{errorText}</p>}

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
          {/* 差异是「范围未设时按全量对照」算出的指示性结果，最终影响面要到提交前的影响预览才定 */}
          <p className="text-xs text-ink-3">{t('delivery.changes.wizard.source.diffIndicative')}</p>
          {/* 差异文件清单（可点开预览文件内容） */}
          <ul className="max-h-56 divide-y divide-border overflow-y-auto rounded-lg border border-border">
            {scan.items.map((item) => {
              const expanded = expandedIds.includes(item.id)
              return (
                <li key={item.id}>
                  <div className="flex items-center gap-2 px-3 py-1.5 text-sm">
                    <Badge variant={ACTION_VARIANT[item.action ?? 'update']} className="w-12 justify-center">
                      {t(`delivery.changes.wizard.source.action${actionKey(item.action)}`)}
                    </Badge>
                    <span className={cn('min-w-0 flex-1 truncate font-mono text-xs', item.action === 'delete' && 'line-through opacity-70')}>
                      {item.path}
                    </span>
                    <span className="tnum shrink-0 text-xs text-ink-3">
                      {item.sizeBytes === null ? '-' : formatBytes(item.sizeBytes)}
                    </span>
                    {orderId !== null && (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="shrink-0 px-2 text-xs"
                        aria-expanded={expanded}
                        onClick={() => {
                          setExpandedIds((prev) =>
                            prev.includes(item.id) ? prev.filter((v) => v !== item.id) : [...prev, item.id],
                          )
                        }}
                      >
                        {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                        {expanded
                          ? t('delivery.preview.change.collapseFile')
                          : t('delivery.preview.change.previewFile')}
                      </Button>
                    )}
                  </div>
                  {expanded && orderId !== null && (
                    <div className="border-t border-dashed border-border bg-surface-2/50 px-3 py-2.5">
                      <FileDiffPreview orderId={orderId} item={item} />
                    </div>
                  )}
                </li>
              )
            })}
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
