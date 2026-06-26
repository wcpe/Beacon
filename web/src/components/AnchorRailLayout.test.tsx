// AnchorRailLayout 单测（FR-108）：
// 覆盖「rail 渲染各分区锚点 → 默认高亮首项 → 点击锚点切换高亮并滚动定位 → 各分区内容按 id 渲染 → 状态色点渲染」。
// jsdom 无真实布局，scroll-spy（IntersectionObserver）不触发，故聚焦锚点可点 / 高亮态 / 内容存在的可断言行为。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AnchorRailLayout, { AnchorSectionBlock, type AnchorSection } from './AnchorRailLayout'

const SECTIONS: AnchorSection[] = [
  { id: 'runtime', label: '进程运行时' },
  { id: 'db', label: '数据库', dot: <span data-testid="db-dot" /> },
  { id: 'longpoll', label: '长轮询' },
]

function renderLayout() {
  return render(
    <AnchorRailLayout sections={SECTIONS} ariaLabel="健康分区导航">
      <AnchorSectionBlock id="runtime" title="进程运行时">
        <div>运行时内容</div>
      </AnchorSectionBlock>
      <AnchorSectionBlock id="db" title="数据库">
        <div>数据库内容</div>
      </AnchorSectionBlock>
      <AnchorSectionBlock id="longpoll" title="长轮询">
        <div>长轮询内容</div>
      </AnchorSectionBlock>
    </AnchorRailLayout>,
  )
}

describe('AnchorRailLayout（FR-108）', () => {
  it('rail 渲染各分区锚点，且为带 aria-label 的导航', () => {
    renderLayout()
    const nav = screen.getByRole('navigation', { name: '健康分区导航' })
    expect(nav).toBeInTheDocument()
    // 三个锚点按钮（rail 项）
    expect(screen.getByRole('button', { name: '进程运行时' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '数据库' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '长轮询' })).toBeInTheDocument()
  })

  it('默认高亮首个分区（aria-current）', () => {
    renderLayout()
    expect(screen.getByRole('button', { name: '进程运行时' })).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('button', { name: '数据库' })).not.toHaveAttribute('aria-current')
  })

  it('点击锚点切换高亮并滚动定位到对应分区', async () => {
    const scrollSpy = vi.spyOn(Element.prototype, 'scrollIntoView')
    renderLayout()
    await userEvent.click(screen.getByRole('button', { name: '数据库' }))
    expect(screen.getByRole('button', { name: '数据库' })).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('button', { name: '进程运行时' })).not.toHaveAttribute('aria-current')
    expect(scrollSpy).toHaveBeenCalled()
    scrollSpy.mockRestore()
  })

  it('各分区内容按 id 锚定渲染（含分区标题与细线）', () => {
    renderLayout()
    // 分区标题（h2）与内容同时存在
    expect(screen.getByRole('heading', { name: '进程运行时' })).toBeInTheDocument()
    expect(screen.getByText('数据库内容')).toBeInTheDocument()
    // 分区根节点带 id
    expect(document.getElementById('longpoll')).not.toBeNull()
  })

  it('状态色点（dot）渲染在对应 rail 项', () => {
    renderLayout()
    expect(screen.getByTestId('db-dot')).toBeInTheDocument()
  })
})
