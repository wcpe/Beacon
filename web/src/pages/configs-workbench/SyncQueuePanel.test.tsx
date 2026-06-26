// SyncQueuePanel(QueueList) 单测（FR-115）：同步队列列表 + 待审核批量选择。
// 覆盖空态、四种状态渲染（done/running/pending-ingest/pending-imprint）、进度条/百分比、
// 待审行可点开审核（onReview）与多选（仅待审行有复选框）、isPendingReview / countPendingSelected 辅助。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import { QueueList, isPendingReview, countPendingSelected } from './SyncQueuePanel'
import type { SyncQueueRow } from '@/api/mock/workbench'

const ROWS: SyncQueueRow[] = [
  { id: 'q1', name: 'config.yml', direction: 'fetch', status: 'done', scopeTarget: '组 main', sourcePath: 'a', targetPath: 'b', time: '14:32:10' },
  { id: 'q2', name: 'spawn.yml', direction: 'push', status: 'running', progress: 62, scopeTarget: '实例 lobby-1', sourcePath: 'c', targetPath: 'd', time: '14:33:01' },
  { id: 'q3', name: 'regions.yml', direction: 'fetch', status: 'pending-ingest', scopeTarget: '组 main', sourcePath: 'e', targetPath: 'f', time: '14:33:20' },
  { id: 'q4', name: 'motd.yml', direction: 'push', status: 'pending-imprint', scopeTarget: '实例 lobby-1', sourcePath: 'g', targetPath: 'h', time: '14:30:55' },
]

describe('QueueList（FR-115）', () => {
  it('空态：显「暂无同步任务」', () => {
    render(<QueueList rows={[]} onReview={vi.fn()} selected={new Set()} onToggleSelect={vi.fn()} />)
    expect(screen.getByText('暂无同步任务')).toBeInTheDocument()
  })

  it('四种状态各自渲染对应文案', () => {
    render(<QueueList rows={ROWS} onReview={vi.fn()} selected={new Set()} onToggleSelect={vi.fn()} />)
    expect(screen.getByText('已完成')).toBeInTheDocument()
    expect(screen.getByText('进行中')).toBeInTheDocument()
    expect(screen.getByText('待审核·ingest')).toBeInTheDocument()
    expect(screen.getByText('待审核·拓印')).toBeInTheDocument()
    // running 行显示进度百分比
    expect(screen.getByText('62%')).toBeInTheDocument()
  })

  it('待审核行可点开触发 onReview', async () => {
    const onReview = vi.fn()
    render(<QueueList rows={ROWS} onReview={onReview} selected={new Set()} onToggleSelect={vi.fn()} />)
    await userEvent.click(screen.getByText('regions.yml'))
    expect(onReview).toHaveBeenCalledTimes(1)
    expect(onReview.mock.calls[0][0].id).toBe('q3')
  })

  it('仅待审核行有复选框，勾选触发 onToggleSelect', async () => {
    const onToggleSelect = vi.fn()
    render(<QueueList rows={ROWS} onReview={vi.fn()} selected={new Set()} onToggleSelect={onToggleSelect} />)
    // 两条待审（q3/q4）→ 两个复选框
    const boxes = screen.getAllByRole('checkbox')
    expect(boxes).toHaveLength(2)
    await userEvent.click(boxes[0])
    expect(onToggleSelect).toHaveBeenCalledWith('q3')
  })

  it('isPendingReview / countPendingSelected 辅助', () => {
    expect(isPendingReview('pending-ingest')).toBe(true)
    expect(isPendingReview('pending-imprint')).toBe(true)
    expect(isPendingReview('done')).toBe(false)
    expect(isPendingReview('running')).toBe(false)
    // 选中 q1(done) + q3(待审) → 仅 q3 计入
    expect(countPendingSelected(ROWS, new Set(['q1', 'q3']))).toBe(1)
    expect(countPendingSelected(ROWS, new Set(['q3', 'q4']))).toBe(2)
  })
})
