// 未分配右侧窄栏（非模态、布局内常驻/开合）：assigned=false 的 server 以紧凑可拖拽 chip 列出，
// 打开时主区（结构树）不被遮罩、仍可交互——因为要能从窄栏往树里拖。
// 两种分配路径：① 直接拖 chip 到树里的兼容目标；② 勾选多个 chip → 底部「分配到…」走目标选择器。
// 选择集只允许同 kind（分配要求同 namespace、同 kind）。

import { useMemo, useState } from 'react'
import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router-dom'
import { GripVertical, Inbox, Network, PanelRightClose, Server } from 'lucide-react'

import { AsyncSection, Badge, Button, Checkbox, cn } from '@beacon/ui'
import type { AssignmentResult, ServerItem } from '@beacon/contracts'

import { fetchServers, fetchZoneTree } from '../../api/cluster'
import { writeAssignDrag } from '../../features/cluster/assign-drag'
import { messageOf, useAssignServers } from '../../features/cluster/use-assign-servers'
import AssignDialog from './assign-dialog'
import ServerContextMenu, { type ContextMenuItem } from './server-context-menu'

interface UnassignedRailProps {
  namespaceId: number
  // 窄栏开合（由页面顶部入口控制）；关闭时整栏从布局移除
  open: boolean
  onClose: () => void
  // 上报正在拖拽的服务器 kind（拖起/结束），供树高亮兼容目标
  onDraggingKindChange: (kind: 'backend' | 'proxy' | null) => void
}

export default function UnassignedBasket({ namespaceId, open, onClose, onDraggingKindChange }: UnassignedRailProps) {
  const { t } = useTranslation()
  const navigate = useNavigate()
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set())
  const [assignOpen, setAssignOpen] = useState(false)
  const [errorText, setErrorText] = useState<string | null>(null)
  const [results, setResults] = useState<AssignmentResult[] | null>(null)
  // 未分配 chip 右键菜单：光标位置 + 目标服务器
  const [menu, setMenu] = useState<{ x: number; y: number; server: ServerItem } | null>(null)

  const query = useQuery({
    queryKey: ['servers', 'unassigned', namespaceId],
    queryFn: () => fetchServers({ namespaceId, assigned: false, pageSize: 200 }),
    placeholderData: keepPreviousData,
  })
  const treeQuery = useQuery({
    queryKey: ['zone-tree', namespaceId],
    queryFn: () => fetchZoneTree(namespaceId),
    placeholderData: keepPreviousData,
  })

  const rows = query.data?.items ?? []
  // 已选中的 server 行（决定 kind 与分配目标）
  const selectedRows = useMemo(() => rows.filter((r) => selectedIds.has(r.id)), [rows, selectedIds])
  // 选择集的 kind：以首个选中项为准（无选中为 null），其余 kind 的行禁选
  const selectionKind: 'backend' | 'proxy' | null = selectedRows.length > 0 ? selectedRows[0].kind : null

  const assignMutation = useAssignServers()

  const toggle = (row: ServerItem) => {
    setSelectedIds((prev) => {
      const next = new Set(prev)
      if (next.has(row.id)) {
        next.delete(row.id)
      } else {
        next.add(row.id)
      }
      return next
    })
  }

  const submitAssign = (targetId: string, isDefaultEntry: boolean) => {
    setErrorText(null)
    setResults(null)
    const kind = selectionKind === 'proxy' ? 'bc_cluster' : 'zone'
    assignMutation.mutate(
      {
        serverIds: selectedRows.map((r) => r.id),
        target: { kind, id: Number.parseInt(targetId, 10) },
        isDefaultEntry,
      },
      {
        onSuccess: (response) => {
          setResults(response.results)
          setSelectedIds(new Set())
        },
        onError: (error) => {
          setErrorText(messageOf(error))
        },
      },
    )
  }

  if (!open) {
    return null
  }

  return (
    // 布局内的右侧窄栏（非模态、无 overlay），主区树仍完全可见可交互
    <aside
      data-slot="unassigned-basket"
      className="flex w-[280px] shrink-0 flex-col self-start rounded-xl border border-border bg-card shadow-card"
    >
      <div className="flex items-center gap-2 border-b border-border px-3 py-2.5">
        <Inbox className="size-4 text-brand" />
        <h2 className="text-[12.5px] font-semibold text-ink-1">{t('cluster.zones.basket.title')}</h2>
        {rows.length > 0 && (
          <Badge variant="warn" className="tnum">
            {rows.length}
          </Badge>
        )}
        <Button variant="ghost" size="icon" className="ml-auto size-7" onClick={onClose} aria-label={t('cluster.zones.basket.close')}>
          <PanelRightClose className="size-4" />
        </Button>
      </div>

      <p className="px-3 pt-2 text-[11px] leading-relaxed text-ink-4">{t('cluster.zones.basket.railHint')}</p>

      <div className="min-h-0 flex-1 overflow-y-auto px-2.5 py-2">
        <AsyncSection isLoading={query.isLoading} isError={query.isError} error={query.error}>
          {rows.length === 0 ? (
            <p className="rounded-lg border border-dashed border-border-strong px-3 py-6 text-center text-xs text-ink-3">
              {t('cluster.zones.basket.empty')}
            </p>
          ) : (
            <ul className="grid gap-1.5">
              {rows.map((row) => {
                const checked = selectedIds.has(row.id)
                // 已选其它 kind 时禁选/禁拖跨 kind 行
                const disabled = selectionKind !== null && row.kind !== selectionKind && !checked
                const isProxy = row.kind === 'proxy'
                return (
                  <li key={row.id}>
                    <div
                      // 原生 HTML5 可拖拽 chip：拖起写入载荷，供树里目标接收
                      draggable={!disabled}
                      onDragStart={(e) => {
                        writeAssignDrag(e.dataTransfer, { id: row.id, serverId: row.serverId, kind: row.kind })
                        onDraggingKindChange(row.kind)
                      }}
                      onDragEnd={() => {
                        onDraggingKindChange(null)
                      }}
                      onContextMenu={(e) => {
                        e.preventDefault()
                        setMenu({ x: e.clientX, y: e.clientY, server: row })
                      }}
                      className={cn(
                        'group flex items-center gap-1.5 rounded-lg border px-2 py-1.5 transition-colors',
                        disabled
                          ? 'cursor-not-allowed border-border bg-surface-2 opacity-50'
                          : cn(
                              'cursor-grab border-border bg-surface-2 hover:border-brand-100 hover:bg-brand-50 active:cursor-grabbing active:opacity-60',
                              checked && 'border-brand-100 bg-brand-50',
                            ),
                      )}
                    >
                      <Checkbox
                        checked={checked}
                        disabled={disabled}
                        onCheckedChange={() => {
                          toggle(row)
                        }}
                        aria-label={t('common.selectRow', { id: row.serverId })}
                      />
                      <GripVertical className="size-3.5 shrink-0 text-ink-4" aria-hidden />
                      <span
                        className={cn(
                          'grid size-5 shrink-0 place-items-center rounded-md',
                          isProxy ? 'bg-brand-100 text-brand-600' : 'bg-brand-50 text-brand',
                        )}
                        aria-hidden
                      >
                        {isProxy ? <Network className="size-3" /> : <Server className="size-3" />}
                      </span>
                      <span className="min-w-0 flex-1 truncate font-mono text-[12px] font-semibold text-ink-1">
                        {row.serverId}
                      </span>
                      <Badge variant={isProxy ? 'brand' : 'secondary'} className="shrink-0">
                        {t(`cluster.servers.kind.${row.kind}`)}
                      </Badge>
                    </div>
                  </li>
                )
              })}
            </ul>
          )}
        </AsyncSection>
      </div>

      {/* 底部操作条：选择集提示 + 「分配到…」 */}
      <div className="flex items-center gap-2 border-t border-border px-3 py-2.5">
        {selectedIds.size > 0 ? (
          <span className="text-[11.5px] font-medium text-brand-600">
            {t('cluster.zones.basket.selected', { count: selectedIds.size })}
          </span>
        ) : (
          <span className="text-[11.5px] text-ink-4">{t('cluster.zones.basket.selectHint')}</span>
        )}
        <Button
          size="sm"
          className="ml-auto"
          disabled={selectedIds.size === 0}
          onClick={() => {
            setErrorText(null)
            setResults(null)
            setAssignOpen(true)
          }}
        >
          {t('cluster.zones.basket.assignTo')}
        </Button>
      </div>

      <AssignDialog
        open={assignOpen}
        onOpenChange={(isOpen) => {
          setAssignOpen(isOpen)
          if (!isOpen) {
            setResults(null)
            setErrorText(null)
          }
        }}
        servers={selectedRows}
        kind={selectionKind === 'proxy' ? 'proxy' : 'backend'}
        tree={treeQuery.data}
        pending={assignMutation.isPending}
        errorText={errorText}
        results={results}
        onConfirm={submitAssign}
      />

      {/* 未分配 chip 右键菜单：未分配服务器仅提供查看详情（改派 / 解绑在 /servers 处理） */}
      <ServerContextMenu
        position={menu ? { x: menu.x, y: menu.y } : null}
        items={
          menu
            ? ([
                {
                  key: 'detail',
                  label: t('cluster.zones.menu.viewDetail'),
                  icon: <Server className="size-3.5" />,
                  onSelect: () => {
                    navigate(`/servers?keyword=${encodeURIComponent(menu.server.serverId)}`)
                  },
                },
              ] satisfies ContextMenuItem[])
            : []
        }
        onClose={() => {
          setMenu(null)
        }}
      />
    </aside>
  )
}
