// ImprintReviewOverlay 单测（FR-115）：拓印审核 diff 浮层（单人自审门）。
// mock CodeEditor 为轻量替身规避 Monaco；用真实 imprintDiffs（motd.yml 有差异）。
// 覆盖：标题/队列名、有差异徽标、审阅闸——未勾选确认钮禁用、勾选后放行并触发 onConfirm、取消回调、
// 未知文件名（无 mock diff）→ 无差异徽标。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

vi.mock('@/components/CodeEditor', () => ({
  default: (props: { original?: string; modified?: string }) => (
    <div data-testid="diff-editor" data-original={props.original} data-modified={props.modified} />
  ),
}))

import ImprintReviewOverlay from './ImprintReviewOverlay'

describe('ImprintReviewOverlay（FR-115）', () => {
  it('标题/队列名 + 有差异徽标（motd.yml 在 mock 中 expected≠current）', () => {
    render(<ImprintReviewOverlay queueName="motd.yml" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('拓印审核 · 期望值 ⟷ 服务器现状')).toBeInTheDocument()
    expect(screen.getByText('motd.yml')).toBeInTheDocument()
    expect(screen.getByText('有差异')).toBeInTheDocument()
  })

  it('审阅闸：未勾选「我已审阅」时确认钮禁用', () => {
    render(<ImprintReviewOverlay queueName="motd.yml" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByRole('button', { name: '确认下发' })).toBeDisabled()
  })

  it('勾选审阅闸后确认钮放行，点击触发 onConfirm', async () => {
    const onConfirm = vi.fn()
    render(<ImprintReviewOverlay queueName="motd.yml" onConfirm={onConfirm} onCancel={vi.fn()} />)
    await userEvent.click(screen.getByLabelText('我已审阅此 diff'))
    const confirmBtn = screen.getByRole('button', { name: '确认下发' })
    expect(confirmBtn).not.toBeDisabled()
    await userEvent.click(confirmBtn)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('取消触发 onCancel', async () => {
    const onCancel = vi.fn()
    render(<ImprintReviewOverlay queueName="motd.yml" onConfirm={vi.fn()} onCancel={onCancel} />)
    const cancels = screen.getAllByRole('button', { name: '取消' })
    await userEvent.click(cancels[cancels.length - 1])
    expect(onCancel).toHaveBeenCalled()
  })

  it('未知文件（无 mock diff）：显「无差异」徽标', () => {
    render(<ImprintReviewOverlay queueName="unknown.yml" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('无差异')).toBeInTheDocument()
  })

  it('把 mock diff 喂入 CodeEditor（original=现状 / modified=期望）', () => {
    render(<ImprintReviewOverlay queueName="motd.yml" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    const ed = screen.getByTestId('diff-editor')
    expect(ed.getAttribute('data-modified')).toContain('受管·全局 期望值')
    expect(ed.getAttribute('data-original')).toContain('服务器现状')
  })
})
