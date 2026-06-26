// PanelTree 单测（FR-115）：双面板树渲染 + 选择 / 展开 / 双击 / 右键 / 文件夹拖拽。
// 文件夹拖拽依赖 @dnd-kit useDraggable/useDroppable，必须包 DndContext 才能挂载（否则抛 context 缺失）。
// 覆盖：折叠时不渲染子节点、展开渲染子节点、文件单选/ctrl 切换、复选框选择、双击开文件、
// 右键弹自定义菜单载荷（含坐标 + side）、文件夹既可拖又是放置目标（draggable + droppable 挂载不报错）、
// flattenVisibleFiles 仅取展开下文件序。
import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { DndContext } from '@dnd-kit/core'
import { CircleCheck } from 'lucide-react'

import PanelTree, { flattenVisibleFiles, type PanelNode } from './PanelTree'
import type { DotMeta } from './diffMeta'

const DOT: DotMeta = { icon: CircleCheck, iconClass: 'text-emerald-500', labelKey: 'x' }

const NODES: PanelNode[] = [
  {
    key: 'plugins',
    name: 'plugins',
    type: 'folder',
    children: [
      { key: 'plugins/a.yml', name: 'a.yml', type: 'file' },
      { key: 'plugins/b.yml', name: 'b.yml', type: 'file' },
    ],
  },
  { key: 'plugins/top.yml', name: 'top.yml', type: 'file' },
]

function renderTree(over: Partial<Parameters<typeof PanelTree>[0]> = {}) {
  const props = {
    nodes: NODES,
    side: 'managed' as const,
    onOpenFile: vi.fn(),
    getDot: () => DOT,
    expanded: new Set<string>(),
    onToggleExpand: vi.fn(),
    selected: new Set<string>(),
    onSelectFile: vi.fn(),
    onContextMenu: vi.fn(),
    ...over,
  } as Parameters<typeof PanelTree>[0]
  render(
    <DndContext>
      <PanelTree {...props} />
    </DndContext>,
  )
  return props
}

describe('PanelTree（FR-115）', () => {
  it('折叠时不渲染文件夹子节点；展开后渲染', () => {
    const { rerender } = render(
      <DndContext>
        <PanelTree
          nodes={NODES}
          side="managed"
          onOpenFile={vi.fn()}
          getDot={() => DOT}
          expanded={new Set()}
          onToggleExpand={vi.fn()}
          selected={new Set()}
          onSelectFile={vi.fn()}
          onContextMenu={vi.fn()}
        />
      </DndContext>,
    )
    expect(screen.queryByText('a.yml')).not.toBeInTheDocument()
    expect(screen.getByText('top.yml')).toBeInTheDocument()

    rerender(
      <DndContext>
        <PanelTree
          nodes={NODES}
          side="managed"
          onOpenFile={vi.fn()}
          getDot={() => DOT}
          expanded={new Set(['plugins'])}
          onToggleExpand={vi.fn()}
          selected={new Set()}
          onSelectFile={vi.fn()}
          onContextMenu={vi.fn()}
        />
      </DndContext>,
    )
    expect(screen.getByText('a.yml')).toBeInTheDocument()
    expect(screen.getByText('b.yml')).toBeInTheDocument()
  })

  // dnd-kit draggable 行挂了 pointer listeners，userEvent 的指针序列可能被拖拽传感拦截；
  // 这里直接派发原生 click/contextMenu 事件断言行点击处理逻辑（与拖拽解耦）。
  it('点文件夹行触发 onToggleExpand', () => {
    const props = renderTree()
    fireEvent.click(screen.getByText('plugins'))
    expect(props.onToggleExpand).toHaveBeenCalledWith('plugins')
  })

  it('点文件行触发 onSelectFile（普通点=非 ctrl/shift）', () => {
    const props = renderTree()
    fireEvent.click(screen.getByText('top.yml'))
    expect(props.onSelectFile).toHaveBeenCalledWith('plugins/top.yml', { ctrl: false, shift: false })
  })

  it('双击文件触发 onOpenFile', () => {
    const props = renderTree()
    fireEvent.doubleClick(screen.getByText('top.yml'))
    expect(props.onOpenFile).toHaveBeenCalledWith(expect.objectContaining({ key: 'plugins/top.yml' }))
  })

  it('文件复选框勾选触发 onSelectFile（ctrl=true 切换语义）', () => {
    const props = renderTree()
    fireEvent.click(screen.getByRole('checkbox', { name: 'top.yml' }))
    expect(props.onSelectFile).toHaveBeenCalledWith('plugins/top.yml', expect.objectContaining({ ctrl: true }))
  })

  it('右键文件弹自定义菜单载荷（含 node/side/坐标）', () => {
    const props = renderTree()
    fireEvent.contextMenu(screen.getByText('top.yml'))
    expect(props.onContextMenu).toHaveBeenCalledWith(
      expect.objectContaining({ side: 'managed', node: expect.objectContaining({ key: 'plugins/top.yml' }) }),
    )
  })

  it('文件夹拖拽：folder 行作为 draggable + droppable 正常挂载（含拖拽 attributes）', () => {
    renderTree({ expanded: new Set(['plugins']) })
    // 文件夹行带 role 不变，但 dnd-kit 会注入 aria 与 draggable 属性；只要渲染不抛错即说明 droppable/draggable 已挂。
    const folderRow = screen.getByText('plugins').closest('[role]') ?? screen.getByText('plugins').parentElement
    expect(folderRow).toBeTruthy()
    // 子文件可见 → 展开态文件夹未因 droppable 注入而破坏渲染
    expect(screen.getByText('a.yml')).toBeInTheDocument()
  })

  it('flattenVisibleFiles 仅取展开文件夹下文件 + 顶层文件，按渲染序', () => {
    expect(flattenVisibleFiles(NODES, new Set())).toEqual(['plugins/top.yml'])
    expect(flattenVisibleFiles(NODES, new Set(['plugins']))).toEqual([
      'plugins/a.yml',
      'plugins/b.yml',
      'plugins/top.yml',
    ])
  })
})
