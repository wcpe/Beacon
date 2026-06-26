// BottomDock 单测（FR-115）：底部 dock tab 切换 + 上下文批量操作按钮显隐。
// 覆盖默认队列 tab、切到日志 tab、队列计数/日志计数、选中待审显批量审核钮（触发 onBatchReview）、
// 选中可撤回项显批量撤回钮（触发 onBatchUndo）。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import BottomDock from './BottomDock'
import type { OpLogEntry, SyncQueueRow } from '@/api/mock/workbench'

const QUEUE: SyncQueueRow[] = [
  { id: 'q1', name: 'config.yml', direction: 'fetch', status: 'done', scopeTarget: '组 main', sourcePath: 'a', targetPath: 'b', time: '14:32' },
  { id: 'q3', name: 'regions.yml', direction: 'fetch', status: 'pending-ingest', scopeTarget: '组 main', sourcePath: 'e', targetPath: 'f', time: '14:33' },
]
const LOG: OpLogEntry[] = [
  { id: 'e1', time: '14:33', action: 'push', operator: 'admin', files: ['spawn.yml'], target: '实例 lobby-1', detail: '下发 spawn.yml', undone: false },
]

function setup(over: Partial<Parameters<typeof BottomDock>[0]> = {}) {
  const props = {
    queueRows: QUEUE,
    onReview: vi.fn(),
    queueSel: new Set<string>(),
    onToggleQueueSel: vi.fn(),
    onBatchReview: vi.fn(),
    logEntries: LOG,
    logSel: new Set<string>(),
    onToggleLogSel: vi.fn(),
    onUndo: vi.fn(),
    onBatchUndo: vi.fn(),
    ...over,
  } as Parameters<typeof BottomDock>[0]
  render(<BottomDock {...props} />)
  return props
}

describe('BottomDock（FR-115）', () => {
  it('默认显队列 tab：队列内容 + 实时标记 + 队列计数', () => {
    setup()
    expect(screen.getByText('实时')).toBeInTheDocument()
    expect(screen.getByText('config.yml')).toBeInTheDocument()
    expect(screen.getByText('2 条')).toBeInTheDocument()
  })

  it('切到操作日志 tab：显日志内容 + 日志计数', async () => {
    setup()
    await userEvent.click(screen.getByRole('button', { name: /操作日志/ }))
    expect(screen.getByText('下发 spawn.yml')).toBeInTheDocument()
    expect(screen.getByText('1 条')).toBeInTheDocument()
  })

  it('队列 tab 选中待审项：显「批量审核」钮，点击触发 onBatchReview', async () => {
    const props = setup({ queueSel: new Set(['q3']) })
    const btn = screen.getByRole('button', { name: /批量审核选中 1 项/ })
    await userEvent.click(btn)
    expect(props.onBatchReview).toHaveBeenCalledTimes(1)
  })

  it('未选待审项：不显批量审核钮', () => {
    setup({ queueSel: new Set() })
    expect(screen.queryByText(/批量审核选中/)).not.toBeInTheDocument()
  })

  it('日志 tab 选中可撤回项：显「批量撤回」钮，点击触发 onBatchUndo', async () => {
    const props = setup({ logSel: new Set(['e1']) })
    await userEvent.click(screen.getByRole('button', { name: /操作日志/ }))
    const btn = screen.getByRole('button', { name: /批量撤回 1 项/ })
    await userEvent.click(btn)
    expect(props.onBatchUndo).toHaveBeenCalledTimes(1)
  })
})
