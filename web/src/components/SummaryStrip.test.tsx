// SummaryStrip 单测（FR-106）：锁定——渲染标签+数值、语义色映射、空 items 不渲染。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import SummaryStrip from './SummaryStrip'

describe('SummaryStrip（FR-106 汇总条）', () => {
  it('渲染每项的标签与数值', () => {
    render(<SummaryStrip items={[{ label: '总实例', value: 5 }, { label: '在线', value: 3 }]} />)
    expect(screen.getByText('总实例')).toBeInTheDocument()
    expect(screen.getByText('5')).toBeInTheDocument()
    expect(screen.getByText('在线')).toBeInTheDocument()
    expect(screen.getByText('3')).toBeInTheDocument()
  })

  it('warning 语义色用琥珀文字', () => {
    render(<SummaryStrip items={[{ label: '未分配', value: 2, tone: 'warning' }]} />)
    expect(screen.getByText('2')).toHaveClass('text-amber-600')
  })

  it('danger 语义色用危险色文字', () => {
    render(<SummaryStrip items={[{ label: '失联', value: 1, tone: 'danger' }]} />)
    expect(screen.getByText('1')).toHaveClass('text-destructive')
  })

  it('items 为空时不渲染任何小卡', () => {
    const { container } = render(<SummaryStrip items={[]} />)
    expect(container).toBeEmptyDOMElement()
  })
})
