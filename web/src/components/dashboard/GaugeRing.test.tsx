// GaugeRing 单测：conic-gradient 进度环按健康等级变色（绿/琥珀/红）+ 中心图标 + 值文案 + null 占比退化。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import GaugeRing from './GaugeRing'

describe('GaugeRing', () => {
  it('渲染标签、值文案与中心图标', () => {
    render(
      <GaugeRing
        icon={<span data-testid="gauge-icon">i</span>}
        ratio={0.5}
        level="ok"
        label="连接池"
        valueText="10 / 20"
        hint="50% 可达"
      />,
    )
    expect(screen.getByText('连接池')).toBeInTheDocument()
    expect(screen.getByText('10 / 20')).toBeInTheDocument()
    expect(screen.getByText('50% 可达')).toBeInTheDocument()
    expect(screen.getByTestId('gauge-icon')).toBeInTheDocument()
  })

  it('正常等级（ok）环用绿色（#16a34a），占比对应角度', () => {
    const { container } = render(<GaugeRing icon={<span>i</span>} ratio={0.5} level="ok" label="L" valueText="v" />)
    const ring = container.querySelector('[role="img"]') as HTMLElement
    // conic-gradient 含绿色与 50% → 180deg 分界
    expect(ring.style.background).toContain('#16a34a')
    expect(ring.style.background).toContain('180deg')
  })

  it('危险等级（danger）环用红色（#dc2626）', () => {
    const { container } = render(<GaugeRing icon={<span>i</span>} ratio={0.95} level="danger" label="L" valueText="v" />)
    const ring = container.querySelector('[role="img"]') as HTMLElement
    expect(ring.style.background).toContain('#dc2626')
  })

  it('注意等级（warn）环用琥珀色（#f59e0b）', () => {
    const { container } = render(<GaugeRing icon={<span>i</span>} ratio={0.75} level="warn" label="L" valueText="v" />)
    const ring = container.querySelector('[role="img"]') as HTMLElement
    expect(ring.style.background).toContain('#f59e0b')
  })

  it('ratio=null（无上限）退化为 0 度（全轨道色），仅靠中心数值表达', () => {
    const { container } = render(<GaugeRing icon={<span>i</span>} ratio={null} level="ok" label="L" valueText="66" />)
    const ring = container.querySelector('[role="img"]') as HTMLElement
    // 0deg 分界：已占比段为空
    expect(ring.style.background).toContain('0deg')
    expect(screen.getByText('66')).toBeInTheDocument()
  })
})
