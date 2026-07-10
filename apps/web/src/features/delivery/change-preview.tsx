// 变更内容预览（共享控件）：展示一张变更单的全部更新内容——文件差异清单
// （新增 / 修改 / 删除分组、语义色、大小）+ 配置变更清单（文件名、from→to 版本、
// 可展开行级 diff）。纯展示：items 与配置展示信息 / diff 渲染均由调用方传入，
// 不自己取数，便于向导第五步与后续详情 / 历史页复用。
import { useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { ChevronDown, ChevronRight } from 'lucide-react'

import { Badge, Button, cn } from '@beacon/ui'

import type { ChangeOrderItem } from '../../api/delivery-changes'
import { formatBytes } from './format'

/** 配置变更行的展示信息（由调用方解析：向导用已选 picks，详情页可查配置中心） */
export interface ConfigChangeLabel {
  fileName: string
  fromVersionNo: number | null
  toVersionNo: number
}

interface ChangePreviewProps {
  /** 本单全部变更项（契约 ChangeOrderItem，混含 file_diff 与 config_change） */
  items: ChangeOrderItem[]
  /** 解析配置变更项的文件名与版本号；缺省 / 返回 null 时回退为作用域 + 版本 id 原始展示 */
  configLabelOf?: (item: ChangeOrderItem) => ConfigChangeLabel | null
  /** 配置行展开时渲染行级 diff（懒加载由调用方注入）；缺省不提供展开 */
  renderConfigDiff?: (item: ChangeOrderItem) => ReactNode
}

// 文件动作 → 语义色徽标变体与分组顺序
const FILE_GROUPS = [
  { action: 'add', variant: 'ok' },
  { action: 'update', variant: 'warn' },
  { action: 'delete', variant: 'crit' },
] as const

export default function ChangePreview({ items, configLabelOf, renderConfigDiff }: ChangePreviewProps) {
  const { t } = useTranslation()
  // 展开 diff 的配置项 id 集合
  const [expandedIds, setExpandedIds] = useState<number[]>([])

  const fileItems = items.filter((item) => item.kind === 'file_diff')
  const configItems = items.filter((item) => item.kind === 'config_change')

  if (fileItems.length === 0 && configItems.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-border px-3 py-6 text-center text-sm text-muted-foreground">
        {t('delivery.preview.change.empty')}
      </p>
    )
  }

  const toggleExpanded = (id: number): void => {
    setExpandedIds((prev) => (prev.includes(id) ? prev.filter((v) => v !== id) : [...prev, id]))
  }

  return (
    <div className="grid gap-4">
      {/* 文件差异清单：按动作分组 */}
      {fileItems.length > 0 && (
        <section className="grid gap-1.5">
          <h4 className="text-[13px] font-semibold text-ink-2">
            {t('delivery.preview.change.filesTitle', { count: fileItems.length })}
          </h4>
          <div className="divide-y divide-border rounded-lg border border-border">
            {FILE_GROUPS.map(({ action, variant }) => {
              const group = fileItems.filter((item) => item.action === action)
              if (group.length === 0) {
                return null
              }
              const groupBytes = group.reduce((sum, item) => sum + (item.sizeBytes ?? 0), 0)
              return (
                <div key={action}>
                  <div className="flex items-center gap-2 bg-surface-2 px-3 py-1.5 text-xs">
                    <Badge variant={variant}>
                      {t(`delivery.preview.change.group.${action}`, { count: group.length })}
                    </Badge>
                    {groupBytes > 0 && (
                      <span className="text-ink-3">
                        {t('delivery.preview.change.groupSize', { size: formatBytes(groupBytes) })}
                      </span>
                    )}
                  </div>
                  <ul className="divide-y divide-border/60">
                    {group.map((item) => (
                      <li key={item.id} className="flex items-center gap-2 px-3 py-1 text-sm">
                        <span
                          className={cn(
                            'min-w-0 flex-1 truncate font-mono text-xs',
                            action === 'delete' && 'line-through opacity-70',
                          )}
                        >
                          {item.path}
                        </span>
                        <span className="tnum shrink-0 text-xs text-ink-3">
                          {item.sizeBytes === null ? '-' : formatBytes(item.sizeBytes)}
                        </span>
                      </li>
                    ))}
                  </ul>
                </div>
              )
            })}
          </div>
        </section>
      )}

      {/* 配置变更清单：文件名 + from→to 版本，可展开行级 diff */}
      {configItems.length > 0 && (
        <section className="grid gap-1.5">
          <h4 className="text-[13px] font-semibold text-ink-2">
            {t('delivery.preview.change.configsTitle', { count: configItems.length })}
          </h4>
          <ul className="divide-y divide-border rounded-lg border border-border">
            {configItems.map((item) => {
              const label = configLabelOf?.(item) ?? null
              const expanded = expandedIds.includes(item.id)
              const expandable = renderConfigDiff !== undefined
              return (
                <li key={item.id}>
                  <div className="flex items-center gap-2 px-3 py-1.5 text-sm">
                    <span className="min-w-0 flex-1 truncate font-mono text-xs">
                      {label?.fileName ??
                        t('delivery.preview.change.configFallback', {
                          kind: item.configScopeKind ?? '-',
                          id: item.configScopeId ?? '-',
                        })}
                    </span>
                    <span className="tnum shrink-0 text-xs text-ink-2">
                      {label !== null
                        ? label.fromVersionNo === null
                          ? t('delivery.preview.change.versionNew', { to: label.toVersionNo })
                          : t('delivery.preview.change.versionRange', {
                              from: label.fromVersionNo,
                              to: label.toVersionNo,
                            })
                        : item.configFromVersionId === null
                          ? t('delivery.preview.change.versionIdNew', { to: item.configToVersionId ?? '-' })
                          : t('delivery.preview.change.versionIdRange', {
                              from: item.configFromVersionId,
                              to: item.configToVersionId ?? '-',
                            })}
                    </span>
                    {expandable && (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="h-7 shrink-0 px-2 text-xs"
                        aria-expanded={expanded}
                        onClick={() => {
                          toggleExpanded(item.id)
                        }}
                      >
                        {expanded ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
                        {expanded
                          ? t('delivery.preview.change.collapseDiff')
                          : t('delivery.preview.change.expandDiff')}
                      </Button>
                    )}
                  </div>
                  {expanded && renderConfigDiff !== undefined && (
                    <div className="border-t border-dashed border-border bg-surface-2/50 px-3 py-2.5">
                      {renderConfigDiff(item)}
                    </div>
                  )}
                </li>
              )
            })}
          </ul>
        </section>
      )}
    </div>
  )
}
