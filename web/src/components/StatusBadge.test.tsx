// StatusBadge 冒烟测试：验证测试基建可跑，并锁定三态配色映射。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import StatusBadge from './StatusBadge'

describe('StatusBadge', () => {
  it('渲染状态文本', () => {
    render(<StatusBadge status="online" />)
    expect(screen.getByText('online')).toBeInTheDocument()
  })

  it('online 用绿色底', () => {
    render(<StatusBadge status="online" />)
    expect(screen.getByText('online')).toHaveClass('bg-green-600')
  })

  it('lost 用琥珀底', () => {
    render(<StatusBadge status="lost" />)
    expect(screen.getByText('lost')).toHaveClass('bg-amber-500')
  })

  it('offline 用灰底', () => {
    render(<StatusBadge status="offline" />)
    expect(screen.getByText('offline')).toHaveClass('bg-muted')
  })
})
