// SectionHeader 单测（FR-107 卡片降级）：锁定区段标题轻分隔组件契约——
// 标题渲染为 h2、可选图标 / 计数 / 右槽 actions 正确呈现、border-b 细线存在。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import SectionHeader from './SectionHeader'

describe('SectionHeader（FR-107 卡片降级）', () => {
  it('标题渲染为 h2 二级标题', () => {
    render(<SectionHeader title="筛选" />)
    expect(screen.getByRole('heading', { level: 2, name: '筛选' })).toBeInTheDocument()
  })

  it('渲染计数与右槽 actions', () => {
    render(
      <SectionHeader
        title="归派看板"
        count="3 台"
        actions={<button>解锁改派</button>}
      />,
    )
    expect(screen.getByText('3 台')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '解锁改派' })).toBeInTheDocument()
  })

  it('底部为 border-b 细线（轻分隔，非卡片边框）', () => {
    const { container } = render(<SectionHeader title="历史趋势" />)
    // 外层容器带 border-b，作为区段标题与内容之间的细线分隔
    expect(container.firstChild).toHaveClass('border-b')
  })
})
