// ZoneSummaryTree 渲染单测（FR-55）：锁定树形展示——大区 / 小区标题与计数、子服叶子、空态。
import { describe, it, expect } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import ZoneSummaryTree from './ZoneSummaryTree'
import type { SummaryTree } from './summaryTree'

const TREE: SummaryTree = {
  groups: [
    {
      group: 'gA',
      serverCount: 2,
      onlineCount: 1,
      zones: [
        {
          zone: 'z1',
          serverCount: 2,
          onlineCount: 1,
          servers: [
            { serverId: 'a-1', status: 'online' },
            { serverId: 'a-2', status: 'offline' },
          ],
        },
        { zone: 'z2', serverCount: 0, onlineCount: 0, servers: [] },
      ],
    },
  ],
}

describe('ZoneSummaryTree', () => {
  it('渲染大区标题与合计计数', () => {
    render(<ZoneSummaryTree tree={TREE} />)
    expect(screen.getByText('大区 gA')).toBeInTheDocument()
    // 大区计数徽标：2 服 · 1 在线（与小区合计一致）
    expect(screen.getAllByText(/2 服 · 1 在线/).length).toBeGreaterThan(0)
  })

  it('渲染小区与其子服叶子', () => {
    render(<ZoneSummaryTree tree={TREE} />)
    expect(screen.getByText('z1')).toBeInTheDocument()
    expect(screen.getByText('a-1')).toBeInTheDocument()
    expect(screen.getByText('a-2')).toBeInTheDocument()
    // 子服在线状态点以 aria-label 暴露
    expect(screen.getByLabelText('状态 online')).toBeInTheDocument()
    expect(screen.getByLabelText('状态 offline')).toBeInTheDocument()
  })

  it('无子服的空 zone 显示占位文案', () => {
    render(<ZoneSummaryTree tree={TREE} />)
    expect(screen.getByText('无在册子服')).toBeInTheDocument()
  })

  it('子服叶子嵌套在其小区节点之下（层级正确）', () => {
    render(<ZoneSummaryTree tree={TREE} />)
    // z1 的小区节点（li）内应能查到其子服 a-1
    const z1Label = screen.getByText('z1')
    const zoneItem = z1Label.closest('li')!
    expect(within(zoneItem).getByText('a-1')).toBeInTheDocument()
  })

  it('空树显示无数据文案', () => {
    render(<ZoneSummaryTree tree={{ groups: [] }} />)
    expect(screen.getByText('无汇总数据')).toBeInTheDocument()
  })
})
