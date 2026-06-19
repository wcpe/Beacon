// 配置资源管理器树：按 namespace > group > dataId 层级展示，点击文件叶子打开标签。

import { useState } from 'react'
import { cn } from '@/lib/utils'
import type { TreeNode } from './types'

export default function ConfigFileTree({
  nodes,
  selectedKey,
  onSelect,
}: {
  nodes: TreeNode[]
  selectedKey: string | null
  onSelect: (node: TreeNode) => void
}) {
  return (
    <>
      {nodes.map((node) => (
        <ConfigTreeNode
          key={node.key}
          node={node}
          depth={0}
          selectedKey={selectedKey}
          onSelect={onSelect}
        />
      ))}
    </>
  )
}

// 单个树节点：目录可展开/折叠（默认展开前两层），文件叶子可点击。
function ConfigTreeNode({
  node,
  depth,
  selectedKey,
  onSelect,
}: {
  node: TreeNode
  depth: number
  selectedKey?: string | null
  onSelect: (node: TreeNode) => void
}) {
  const [expanded, setExpanded] = useState(depth < 2)
  const isFolder = node.type === 'folder'
  const isSelected = node.key === selectedKey
  const hasChildren = isFolder && node.children && node.children.length > 0

  const handleClick = () => {
    if (isFolder) setExpanded(!expanded)
    onSelect(node)
  }

  return (
    <div>
      <button
        type="button"
        className={cn(
          'flex w-full items-center gap-1 px-2 py-1 text-sm transition-colors',
          isSelected
            ? 'bg-accent text-accent-foreground'
            : 'text-muted-foreground hover:bg-muted hover:text-foreground',
        )}
        style={{ paddingLeft: `${depth * 12 + 8}px` }}
        onClick={handleClick}
      >
        {hasChildren ? (
          expanded ? <span className="text-xs">▼</span> : <span className="text-xs">▶</span>
        ) : (
          <span className="w-3" />
        )}
        <span className="truncate">{node.label}</span>
      </button>
      {hasChildren && expanded && (
        <div>
          {node.children!.map((child) => (
            <ConfigTreeNode
              key={child.key}
              node={child}
              depth={depth + 1}
              selectedKey={selectedKey}
              onSelect={onSelect}
            />
          ))}
        </div>
      )}
    </div>
  )
}
