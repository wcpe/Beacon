// OperationLogList 单测（FR-115）：操作日志列表 + 逐条/批量撤回选择。
// 覆盖空态、行渲染（操作 badge/文件/详情）、未撤回项可勾选撤回、已撤回项置灰且无复选框/撤回钮、
// 逐条撤回回调、countUndoableSelected 仅计未撤回选中项。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { OperationLogList, countUndoableSelected } from './OperationLogPanel'
import type { OpLogEntry } from './types'

const ENTRIES: OpLogEntry[] = [
  { id: 'e1', time: '14:33:01', action: 'push', operator: 'admin', files: ['spawn.yml'], target: '实例 lobby-1', detail: '下发 spawn.yml', undone: false },
  { id: 'e2', time: '14:32:10', action: 'fetch', operator: 'admin', files: ['config.yml'], target: '组 main', detail: '抓取 config.yml', undone: false },
  { id: 'e3', time: '14:20:08', action: 'rename', operator: 'ops', files: ['economy.yml'], target: '受管配置', detail: '重命名 econ', undone: true },
]

describe('OperationLogList（FR-115）', () => {
  it('空态：显「暂无操作记录」', () => {
    render(<OperationLogList entries={[]} selected={new Set()} onToggleSelect={vi.fn()} onUndo={vi.fn()} />)
    expect(screen.getByText('暂无操作记录')).toBeInTheDocument()
  })

  it('渲染每条记录的文件名与详情', () => {
    render(<OperationLogList entries={ENTRIES} selected={new Set()} onToggleSelect={vi.fn()} onUndo={vi.fn()} />)
    expect(screen.getByText('spawn.yml')).toBeInTheDocument()
    expect(screen.getByText('下发 spawn.yml')).toBeInTheDocument()
    // 操作 badge 文案
    expect(screen.getByText('下发')).toBeInTheDocument()
    expect(screen.getByText('抓取')).toBeInTheDocument()
  })

  it('未撤回项可逐条撤回，点击触发 onUndo 传 id', async () => {
    const onUndo = vi.fn()
    render(<OperationLogList entries={ENTRIES} selected={new Set()} onToggleSelect={vi.fn()} onUndo={onUndo} />)
    const undoBtns = screen.getAllByRole('button', { name: /撤回/ })
    // 两条未撤回 → 两个撤回按钮
    expect(undoBtns).toHaveLength(2)
    await userEvent.click(undoBtns[0])
    expect(onUndo).toHaveBeenCalledWith('e1')
  })

  it('已撤回项：显「已撤回」，无复选框、无撤回按钮', () => {
    render(<OperationLogList entries={ENTRIES} selected={new Set()} onToggleSelect={vi.fn()} onUndo={vi.fn()} />)
    expect(screen.getByText('已撤回')).toBeInTheDocument()
    // 复选框只对未撤回的两条出现
    expect(screen.getAllByRole('checkbox')).toHaveLength(2)
  })

  it('勾选未撤回项触发 onToggleSelect 传 id', async () => {
    const onToggleSelect = vi.fn()
    render(<OperationLogList entries={ENTRIES} selected={new Set()} onToggleSelect={onToggleSelect} onUndo={vi.fn()} />)
    await userEvent.click(screen.getAllByRole('checkbox')[0])
    expect(onToggleSelect).toHaveBeenCalledWith('e1')
  })

  it('countUndoableSelected 只计选中且未撤回的项', () => {
    // 选中 e1（未撤回）+ e3（已撤回）→ 仅 e1 计入
    expect(countUndoableSelected(ENTRIES, new Set(['e1', 'e3']))).toBe(1)
    expect(countUndoableSelected(ENTRIES, new Set(['e1', 'e2']))).toBe(2)
    expect(countUndoableSelected(ENTRIES, new Set())).toBe(0)
  })
})
