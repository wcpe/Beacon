/**
 * 配置中心文件树组件
 *
 * 按 namespace > group > dataId 层级组织，使用 shadcn 语义 token。
 * 节点展开/折叠由外部通过 `expandedKeys` 控制。
 */

import { useState } from 'react'
import { ChevronRight, ChevronDown, Folder, FolderOpen, FileText } from 'lucide-react'
import { ScrollArea } from '@/components/ui/scroll-area'
import { cn } from '@/lib/utils'

// ---- 类型 ----

export interface FileTreeNode {
  /** 节点唯一标识 */
  key: string
  /** 显示名称 */
  label: string
  /** 节点类型 */
  type: 'folder' | 'file'
  /** 子节点 */
  children?: FileTreeNode[]
  /** 附加数据（用于传递给点击回调） */
  data?: Record<string, unknown>
}

interface FileTreeProps {
  /** 树形数据 */
  data: FileTreeNode[]
  /** 当前选中的节点 key */
  selectedKey?: string
  /** 点击节点回调 */
  onSelect?: (node: FileTreeNode) => void
  /** 默认展开的 key 集合（可选，未提供时全部折叠） */
  defaultExpanded?: string[]
}

// ---- 主组件 ----

export default function FileTree({ data, selectedKey, onSelect, defaultExpanded }: FileTreeProps) {
  const [expanded, setExpanded] = useState<Set<string>>(new Set(defaultExpanded ?? []))

  const toggle = (key: string) => {
    setExpanded((prev) => {
      const next = new Set(prev)
      if (next.has(key)) {
        next.delete(key)
      } else {
        next.add(key)
      }
      return next
    })
  }

  return (
    <ScrollArea className="h-full">
      <div className="py-1">
        {data.map((node) => (
          <TreeNode
            key={node.key}
            node={node}
            depth={0}
            expanded={expanded}
            selectedKey={selectedKey}
            onToggle={toggle}
            onSelect={onSelect}
          />
        ))}
      </div>
    </ScrollArea>
  )
}

// ---- 树节点 ----

interface TreeNodeProps {
  node: FileTreeNode
  depth: number
  expanded: Set<string>
  selectedKey?: string
  onToggle: (key: string) => void
  onSelect?: (node: FileTreeNode) => void
}

function TreeNode({ node, depth, expanded, selectedKey, onToggle, onSelect }: TreeNodeProps) {
  const isFolder = node.type === 'folder'
  const isExpanded = expanded.has(node.key)
  const isSelected = node.key === selectedKey
  const hasChildren = isFolder && node.children && node.children.length > 0

  const handleClick = () => {
    if (isFolder) {
      onToggle(node.key)
    }
    onSelect?.(node)
  }

  const Icon = isFolder
    ? isExpanded
      ? FolderOpen
      : Folder
    : FileText

  return (
    <div>
      <button
        type="button"
        className={cn(
          'flex w-full items-center gap-1 rounded-md px-2 py-1.5 text-sm transition-colors',
          isSelected
            ? 'bg-accent font-medium text-accent-foreground'
            : 'text-muted-foreground hover:bg-muted hover:text-foreground',
        )}
        style={{ paddingLeft: `${depth * 16 + 8}px` }}
        onClick={handleClick}
      >
        {hasChildren ? (
          isExpanded ? (
            <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
          )
        ) : (
          <span className="w-3.5 shrink-0" />
        )}
        <Icon className={cn('h-3.5 w-3.5 shrink-0', isFolder ? 'text-blue-400' : 'text-muted-foreground')} />
        <span className="truncate">{node.label}</span>
      </button>
      {hasChildren && isExpanded && (
        <div>
          {node.children!.map((child) => (
            <TreeNode
              key={child.key}
              node={child}
              depth={depth + 1}
              expanded={expanded}
              selectedKey={selectedKey}
              onToggle={onToggle}
              onSelect={onSelect}
            />
          ))}
        </div>
      )}
    </div>
  )
}
