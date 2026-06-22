// StatCard 紧凑尺寸单测（回归「KPI 卡片过大」）：
// 锁定卡片以 size=sm 收紧内外边距、主数值字号为 text-xl（而非更大的 text-2xl），
// 避免后续改动让 KPI 卡片重新变大。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import StatCard from './StatCard'

describe('StatCard 紧凑尺寸', () => {
  it('卡片以 size=sm 收紧边距，主数值用 text-xl 而非 text-2xl', () => {
    render(<StatCard label="平均 TPS" value="19.9" icon={<span>i</span>} />)

    // 主数值容器：紧凑字号 text-xl，且不得回退到更大的 text-2xl
    const valueEl = screen.getByText('19.9')
    expect(valueEl.classList.contains('text-xl')).toBe(true)
    expect(valueEl.classList.contains('text-2xl')).toBe(false)

    // 外层卡片以 size=sm 渲染（--card-spacing 4→3，整体更紧凑）
    const cardEl = valueEl.closest('[data-slot="card"]')
    expect(cardEl).not.toBeNull()
    expect(cardEl?.getAttribute('data-size')).toBe('sm')
  })
})
