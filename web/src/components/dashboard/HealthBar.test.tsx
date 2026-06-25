// HealthBar 单测：分段健康条按各段计数占比排宽 + 按等级上色 + 总数为 0 时空态轨道。
import { describe, it, expect } from 'vitest'
import { render } from '@testing-library/react'
import HealthBar, { type HealthSegment } from './HealthBar'

const SEGMENTS: HealthSegment[] = [
  { label: '在线', count: 3, level: 'ok' },
  { label: '亚健康', count: 1, level: 'warn' },
  { label: '失联', count: 0, level: 'danger' },
]

describe('HealthBar', () => {
  it('各段按计数占比排宽（在线 3/4=75%、亚健康 1/4=25%）', () => {
    const { container } = render(<HealthBar segments={SEGMENTS} />)
    const greenSeg = container.querySelector('.bg-green-600') as HTMLElement
    const amberSeg = container.querySelector('.bg-amber-500') as HTMLElement
    expect(greenSeg).not.toBeNull()
    expect(amberSeg).not.toBeNull()
    // 宽度按占比（总数 4）
    expect(greenSeg.style.width).toBe('75%')
    expect(amberSeg.style.width).toBe('25%')
  })

  it('计数为 0 的分段不渲染色块（失联 0 → 无红段）', () => {
    const { container } = render(<HealthBar segments={SEGMENTS} />)
    expect(container.querySelector('.bg-red-600')).toBeNull()
  })

  it('总数为 0 时仅渲染空态轨道（无任何色块）', () => {
    const { container } = render(
      <HealthBar
        segments={[
          { label: '在线', count: 0, level: 'ok' },
          { label: '失联', count: 0, level: 'danger' },
        ]}
      />,
    )
    expect(container.querySelector('.bg-green-600')).toBeNull()
    expect(container.querySelector('.bg-red-600')).toBeNull()
  })

  it('无障碍标签汇总各段计数', () => {
    const { container } = render(<HealthBar segments={SEGMENTS} />)
    const bar = container.querySelector('[role="img"]') as HTMLElement
    expect(bar.getAttribute('aria-label')).toContain('在线 3')
    expect(bar.getAttribute('aria-label')).toContain('失联 0')
  })
})
