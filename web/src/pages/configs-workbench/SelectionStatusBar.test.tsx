// SelectionStatusBar 单测（FR-115）：顶部常驻选中状态栏。
// 覆盖三态（未选/受管选中=发布/服务器选中=抓取）、对应主操作按钮回调、清除、撤回上一步禁用闸。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import SelectionStatusBar from './SelectionStatusBar'

// 公共默认 props 工厂：各用例按需覆盖
function setup(overrides: Partial<Parameters<typeof SelectionStatusBar>[0]> = {}) {
  const props = {
    selSide: null,
    count: 0,
    fetchScopeLabel: '组 main',
    onPublish: vi.fn(),
    onFetch: vi.fn(),
    onClear: vi.fn(),
    canUndoLast: false,
    onUndoLast: vi.fn(),
    ...overrides,
  } as Parameters<typeof SelectionStatusBar>[0]
  render(<SelectionStatusBar {...props} />)
  return props
}

describe('SelectionStatusBar（FR-115）', () => {
  it('未选中：显操作提示（legendHint），不出发布/抓取按钮', () => {
    setup({ selSide: null })
    expect(screen.getByText(/勾选\/ctrl\/shift 多选/)).toBeInTheDocument()
    expect(screen.queryByText(/发布选中/)).not.toBeInTheDocument()
    expect(screen.queryByText(/抓取选中/)).not.toBeInTheDocument()
  })

  it('受管选中：显发布提示与「发布选中 N 项」按钮，点击触发 onPublish', async () => {
    const props = setup({ selSide: 'managed', count: 3 })
    expect(screen.getByText('已选中 3 项（受管侧）')).toBeInTheDocument()
    const btn = screen.getByRole('button', { name: '发布选中 3 项' })
    await userEvent.click(btn)
    expect(props.onPublish).toHaveBeenCalledTimes(1)
  })

  it('服务器选中：显抓取目标覆盖层与「抓取选中 N 项」按钮，点击触发 onFetch', async () => {
    const props = setup({ selSide: 'server', count: 2, fetchScopeLabel: '组 pvp' })
    expect(screen.getByText('已选中 2 项（服务器侧）')).toBeInTheDocument()
    expect(screen.getByText('抓取后落到：组 pvp')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '抓取选中 2 项 ↤' }))
    expect(props.onFetch).toHaveBeenCalledTimes(1)
  })

  it('选中态有清除按钮，点击触发 onClear', async () => {
    const props = setup({ selSide: 'managed', count: 1 })
    await userEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(props.onClear).toHaveBeenCalledTimes(1)
  })

  it('撤回上一步：无可撤回时禁用、有则可点并触发 onUndoLast', async () => {
    const onUndoLast = vi.fn()
    const { rerender } = render(
      <SelectionStatusBar
        selSide={null}
        count={0}
        fetchScopeLabel="组 main"
        onPublish={vi.fn()}
        onFetch={vi.fn()}
        onClear={vi.fn()}
        canUndoLast={false}
        onUndoLast={onUndoLast}
      />,
    )
    expect(screen.getByRole('button', { name: /撤回上一步/ })).toBeDisabled()

    rerender(
      <SelectionStatusBar
        selSide={null}
        count={0}
        fetchScopeLabel="组 main"
        onPublish={vi.fn()}
        onFetch={vi.fn()}
        onClear={vi.fn()}
        canUndoLast={true}
        onUndoLast={onUndoLast}
      />,
    )
    const undoBtn = screen.getByRole('button', { name: /撤回上一步/ })
    expect(undoBtn).not.toBeDisabled()
    await userEvent.click(undoBtn)
    expect(onUndoLast).toHaveBeenCalledTimes(1)
  })
})
