// 双面板树 v2（受管 / 服务器共用）：Xftp 风多列 —— 名称列(树形缩进+行首状态点+复选框+图标) + 若干元信息列。
// 文件行可多选（复选框 + ctrl/shift 点选）、可拖（@dnd-kit）、可双击进编辑器、可右键弹自定义菜单；
// 文件夹可展开折叠。受管 / 服务器两侧的行首点语义不同（同步状态 vs 纳管标记），经 getDot 注入。

import { type ReactNode } from 'react'
import { useDraggable, useDroppable } from '@dnd-kit/core'
import { ChevronDown, ChevronRight, File as FileIcon, Folder } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { DotMeta } from './diffMeta'

// 面板树通用节点（受管 / 服务器两侧裁剪到公共字段）
export interface PanelNode {
  key: string
  name: string
  type: 'folder' | 'file'
  children?: PanelNode[]
}

// 右键菜单触发载荷（节点 + 光标坐标 + 来源面板）
export interface ContextMenuPayload {
  node: PanelNode
  side: 'managed' | 'server'
  x: number
  y: number
}

interface PanelTreeProps {
  nodes: PanelNode[]
  // 拖拽来源面板标识，拼进 draggable id 供跨面板判定（managed / server）
  side: 'managed' | 'server'
  // 双击文件
  onOpenFile: (node: PanelNode) => void
  // 行首状态点元信息（受管=同步状态 / 服务器=纳管标记），由各面板按节点注入
  getDot: (node: PanelNode) => DotMeta
  // 元信息列（除名称外的若干等宽列）；返回的每个元素对应一列。
  renderCols?: (node: PanelNode) => ReactNode[]
  // 列宽模板（与 renderCols 返回列数对应），用于行列对齐
  colWidths?: string[]
  // 当前展开的文件夹 key 集合（受控，由父组件持有，便于「固定高度内部滚」一致）
  expanded: Set<string>
  onToggleExpand: (key: string) => void
  // 选中的文件 key 集合（受控多选）
  selected: Set<string>
  // 行点击选择：普通点=单选；ctrl/meta=切换；shift=范围选（父据可见文件序计算）
  onSelectFile: (key: string, modifiers: { ctrl: boolean; shift: boolean }) => void
  // 右键菜单
  onContextMenu: (payload: ContextMenuPayload) => void
}

export default function PanelTree({
  nodes,
  side,
  onOpenFile,
  getDot,
  renderCols,
  colWidths,
  expanded,
  onToggleExpand,
  selected,
  onSelectFile,
  onContextMenu,
}: PanelTreeProps) {
  return (
    <div className="py-1">
      {nodes.map((n) => (
        <TreeRow
          key={n.key}
          node={n}
          depth={0}
          side={side}
          onOpenFile={onOpenFile}
          getDot={getDot}
          renderCols={renderCols}
          colWidths={colWidths}
          expanded={expanded}
          onToggleExpand={onToggleExpand}
          selected={selected}
          onSelectFile={onSelectFile}
          onContextMenu={onContextMenu}
        />
      ))}
    </div>
  )
}

interface TreeRowProps {
  node: PanelNode
  depth: number
  side: 'managed' | 'server'
  onOpenFile: (node: PanelNode) => void
  getDot: (node: PanelNode) => DotMeta
  renderCols?: (node: PanelNode) => ReactNode[]
  colWidths?: string[]
  expanded: Set<string>
  onToggleExpand: (key: string) => void
  selected: Set<string>
  onSelectFile: (key: string, modifiers: { ctrl: boolean; shift: boolean }) => void
  onContextMenu: (payload: ContextMenuPayload) => void
}

function TreeRow({
  node,
  depth,
  side,
  onOpenFile,
  getDot,
  renderCols,
  colWidths,
  expanded,
  onToggleExpand,
  selected,
  onSelectFile,
  onContextMenu,
}: TreeRowProps) {
  const isFolder = node.type === 'folder'
  const hasChildren = isFolder && !!node.children && node.children.length > 0
  const isExpanded = expanded.has(node.key)
  const isSelected = selected.has(node.key)
  const dot = getDot(node)

  // 文件 / 文件夹均可拖：id 形如 managed::<key> / server::<key>，落点据前缀判定方向。
  // data 带 type / key，供上层区分「文件」与「整个目录」的同步语义。
  const { attributes, listeners, setNodeRef: setDragRef, isDragging } = useDraggable({
    id: `${side}::${node.key}`,
    data: { side, name: node.name, type: node.type, key: node.key },
  })

  // 文件夹行同时可作为本面板内放置目标（拖文件 / 目录落文件夹 = 改目录）。
  // id 形如 folder::managed::<key>，data 带 side / folderKey / folderName，供上层判定「同面板移动」。
  const { setNodeRef: setDropRef, isOver: isFolderOver } = useDroppable({
    id: `folder::${side}::${node.key}`,
    data: { side, folderKey: node.key, folderName: node.name },
    disabled: !isFolder,
  })

  // 合并 ref：文件夹既可拖又可作放置目标，文件仅可拖
  const setRowRef = (el: HTMLDivElement | null) => {
    setDragRef(el)
    if (isFolder) setDropRef(el)
  }

  const cols = renderCols?.(node) ?? []

  // 行点击：文件夹切换展开；文件按修饰键选择
  function onRowClick(e: React.MouseEvent) {
    if (isFolder) {
      onToggleExpand(node.key)
      return
    }
    onSelectFile(node.key, { ctrl: e.ctrlKey || e.metaKey, shift: e.shiftKey })
  }

  return (
    <div>
      <div
        ref={setRowRef}
        {...attributes}
        {...listeners}
        className={cn(
          'group flex items-center gap-2 rounded px-2 py-1 text-xs select-none',
          'cursor-grab active:cursor-grabbing',
          !isFolder && (isSelected ? 'bg-primary/10 ring-1 ring-inset ring-primary/30' : 'hover:bg-muted/50'),
          isFolder && 'hover:bg-muted/50',
          // 拖文件悬停到本文件夹上：高亮提示「将移入此目录」（改进 3）
          isFolder && isFolderOver && 'bg-primary/10 ring-1 ring-inset ring-primary/40',
          isDragging && 'opacity-40',
        )}
        onClick={onRowClick}
        onDoubleClick={() => !isFolder && onOpenFile(node)}
        onContextMenu={(e) => {
          e.preventDefault()
          onContextMenu({ node, side, x: e.clientX, y: e.clientY })
        }}
        title={isFolder ? undefined : node.name}
      >
        {/* 名称列（占满剩余宽度）：缩进 + 状态点 + 复选框 + 展开箭头 + 图标 + 名称 */}
        <span className="flex min-w-0 flex-1 items-center gap-1.5" style={{ paddingLeft: `${depth * 14}px` }}>
          {/* 行首状态图标（受管=同步状态 / 服务器=纳管标记）；改进 2：色球→lucide 图标 */}
          <dot.icon className={cn('h-3.5 w-3.5 shrink-0', dot.iconClass, dot.spin && 'animate-spin')} />
          {/* 复选框（仅文件，多选用） */}
          {isFolder ? (
            <span className="w-3.5 shrink-0" />
          ) : (
            <input
              type="checkbox"
              checked={isSelected}
              onClick={(e) => e.stopPropagation()}
              onChange={(e) => onSelectFile(node.key, { ctrl: true, shift: e.nativeEvent instanceof MouseEvent && (e.nativeEvent as MouseEvent).shiftKey })}
              className="h-3 w-3 shrink-0 cursor-pointer accent-primary"
              aria-label={node.name}
            />
          )}
          {/* 展开箭头（仅有子节点的文件夹） */}
          {hasChildren ? (
            isExpanded ? (
              <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
            )
          ) : (
            <span className="w-3.5 shrink-0" />
          )}
          {/* 文件夹 / 文件图标 */}
          {isFolder ? (
            <Folder className="h-3.5 w-3.5 shrink-0 fill-amber-400/30 text-amber-500" />
          ) : (
            <FileIcon className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          )}
          {/* 名称 */}
          <span className="truncate text-foreground">{node.name}</span>
        </span>
        {/* 元信息列（等宽、与列头对齐） */}
        {cols.map((c, i) => (
          <span key={i} className={cn('shrink-0 text-right tabular-nums text-muted-foreground', colWidths?.[i])}>
            {c}
          </span>
        ))}
      </div>
      {/* 子节点 */}
      {hasChildren && isExpanded && (
        <div>
          {node.children!.map((c) => (
            <TreeRow
              key={c.key}
              node={c}
              depth={depth + 1}
              side={side}
              onOpenFile={onOpenFile}
              getDot={getDot}
              renderCols={renderCols}
              colWidths={colWidths}
              expanded={expanded}
              onToggleExpand={onToggleExpand}
              selected={selected}
              onSelectFile={onSelectFile}
              onContextMenu={onContextMenu}
            />
          ))}
        </div>
      )}
    </div>
  )
}

// 扁平化「可见文件序」：用于 shift 范围选（仅展开下的文件，按渲染顺序）。
export function flattenVisibleFiles(nodes: PanelNode[], expanded: Set<string>): string[] {
  const out: string[] = []
  const walk = (list: PanelNode[]) => {
    for (const n of list) {
      if (n.type === 'file') {
        out.push(n.key)
      } else if (n.children && expanded.has(n.key)) {
        walk(n.children)
      }
    }
  }
  walk(nodes)
  return out
}
