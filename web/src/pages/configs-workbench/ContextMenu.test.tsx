// ContextMenu 单测（FR-115）：文件 / 文件夹右键自定义菜单。
// 覆盖文件菜单项全集、文件夹隐藏文件专属项、抓取/下发方向文案随 side、回滚仅受管文件、
// 各动作回调、点遮罩关闭、ESC 关闭。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import ContextMenu, { type ContextMenuState } from './ContextMenu'

function baseState(over: Partial<ContextMenuState> = {}): ContextMenuState {
  return { x: 10, y: 20, side: 'managed', name: 'config.yml', isFolder: false, ...over }
}

describe('ContextMenu（FR-115）', () => {
  it('受管文件：编辑/重命名/新建/下发/查看差异/回滚/删除全在', () => {
    render(<ContextMenu state={baseState()} onAction={vi.fn()} onClose={vi.fn()} />)
    expect(screen.getByText('编辑')).toBeInTheDocument()
    expect(screen.getByText('重命名')).toBeInTheDocument()
    expect(screen.getByText('新建')).toBeInTheDocument()
    expect(screen.getByText('下发到服务器')).toBeInTheDocument()
    expect(screen.getByText('查看差异')).toBeInTheDocument()
    expect(screen.getByText('回滚到历史版本…')).toBeInTheDocument()
    expect(screen.getByText('删除')).toBeInTheDocument()
  })

  it('服务器侧文件：传输文案为「抓取到受管」，且无回滚项（回滚仅受管）', () => {
    render(<ContextMenu state={baseState({ side: 'server' })} onAction={vi.fn()} onClose={vi.fn()} />)
    expect(screen.getByText('抓取到受管')).toBeInTheDocument()
    expect(screen.queryByText('回滚到历史版本…')).not.toBeInTheDocument()
  })

  it('文件夹：隐藏文件专属项（编辑/传输/差异/回滚），保留重命名/新建/删除', () => {
    render(<ContextMenu state={baseState({ isFolder: true })} onAction={vi.fn()} onClose={vi.fn()} />)
    expect(screen.queryByText('编辑')).not.toBeInTheDocument()
    expect(screen.queryByText('下发到服务器')).not.toBeInTheDocument()
    expect(screen.queryByText('查看差异')).not.toBeInTheDocument()
    expect(screen.queryByText('回滚到历史版本…')).not.toBeInTheDocument()
    expect(screen.getByText('重命名')).toBeInTheDocument()
    expect(screen.getByText('新建')).toBeInTheDocument()
    expect(screen.getByText('删除')).toBeInTheDocument()
  })

  it('点菜单项触发对应 action', async () => {
    const onAction = vi.fn()
    render(<ContextMenu state={baseState()} onAction={onAction} onClose={vi.fn()} />)
    await userEvent.click(screen.getByText('编辑'))
    expect(onAction).toHaveBeenCalledWith('edit')
    await userEvent.click(screen.getByText('删除'))
    expect(onAction).toHaveBeenCalledWith('delete')
    await userEvent.click(screen.getByText('回滚到历史版本…'))
    expect(onAction).toHaveBeenCalledWith('rollback')
  })

  it('ESC 键关闭菜单', async () => {
    const onClose = vi.fn()
    render(<ContextMenu state={baseState()} onAction={vi.fn()} onClose={onClose} />)
    await userEvent.keyboard('{Escape}')
    expect(onClose).toHaveBeenCalled()
  })

  it('点透明遮罩关闭菜单', async () => {
    const onClose = vi.fn()
    render(<ContextMenu state={baseState()} onAction={vi.fn()} onClose={onClose} />)
    // 遮罩是 aria-hidden 的覆盖层，取菜单标题（文件名）旁的遮罩用 DOM 查询
    const overlay = document.querySelector('.absolute.inset-0')
    await userEvent.click(overlay as Element)
    expect(onClose).toHaveBeenCalled()
  })
})
