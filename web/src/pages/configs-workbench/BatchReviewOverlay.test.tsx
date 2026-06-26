// BatchReviewOverlay 单测（FR-115）：队列批量审核浮层（混合 ingest/imprint）。
// mock CodeEditor 规避 Monaco。覆盖：标题副标题计数（抓取/下发拆分）、左侧待审文件列、
// 切换行看不同详情（push→diff / fetch→纳管清单提示）、批量审阅闸禁用/放行、全部通过回调、取消回调。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

vi.mock('@/components/CodeEditor', () => ({
  default: () => <div data-testid="diff-editor" />,
}))

import BatchReviewOverlay from './BatchReviewOverlay'
import type { SyncQueueRow } from '@/api/mock/workbench'

const ROWS: SyncQueueRow[] = [
  { id: 'q4', name: 'motd.yml', direction: 'push', status: 'pending-imprint', scopeTarget: '实例 lobby-1', sourcePath: 'prod/motd.yml', targetPath: 'lobby-1:/motd.yml', time: '14:30' },
  { id: 'q3', name: 'WorldGuard/regions.yml', direction: 'fetch', status: 'pending-ingest', scopeTarget: '组 main', sourcePath: 'lobby-1:/regions.yml', targetPath: 'prod/regions.yml', time: '14:33' },
]

describe('BatchReviewOverlay（FR-115）', () => {
  it('标题 + 副标题计数（共 2 项·抓取 1·下发 1）', () => {
    render(<BatchReviewOverlay rows={ROWS} onConfirm={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('批量审核 · 待审项')).toBeInTheDocument()
    expect(screen.getByText('共 2 项（抓取 1 · 下发 1）—— 逐个审阅后一并通过')).toBeInTheDocument()
  })

  it('左列两个待审文件；默认首项（push）显 diff 编辑器', () => {
    render(<BatchReviewOverlay rows={ROWS} onConfirm={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('motd.yml')).toBeInTheDocument()
    expect(screen.getByText('WorldGuard/regions.yml')).toBeInTheDocument()
    // 首项是 push → 详情区是 diff 编辑器
    expect(screen.getByTestId('diff-editor')).toBeInTheDocument()
  })

  it('切到 fetch 项：详情区显纳管清单提示（非 diff）', async () => {
    render(<BatchReviewOverlay rows={ROWS} onConfirm={vi.fn()} onCancel={vi.fn()} />)
    await userEvent.click(screen.getByText('WorldGuard/regions.yml'))
    expect(screen.getByText('此项为反向抓取待纳管，可在队列中单独点开逐项勾选纳管清单。')).toBeInTheDocument()
    expect(screen.queryByTestId('diff-editor')).not.toBeInTheDocument()
  })

  it('批量审阅闸：未勾选时「全部通过」禁用，勾选后放行并触发 onConfirm', async () => {
    const onConfirm = vi.fn()
    render(<BatchReviewOverlay rows={ROWS} onConfirm={onConfirm} onCancel={vi.fn()} />)
    const approve = screen.getByRole('button', { name: '全部通过（2 项）' })
    expect(approve).toBeDisabled()
    await userEvent.click(screen.getByLabelText('我已审阅全部'))
    expect(approve).not.toBeDisabled()
    await userEvent.click(approve)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('取消触发 onCancel', async () => {
    const onCancel = vi.fn()
    render(<BatchReviewOverlay rows={ROWS} onConfirm={vi.fn()} onCancel={onCancel} />)
    const cancels = screen.getAllByRole('button', { name: '取消' })
    await userEvent.click(cancels[cancels.length - 1])
    expect(onCancel).toHaveBeenCalled()
  })
})
