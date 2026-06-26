// StatusBadge 冒烟测试：验证测试基建可跑，并锁定三态配色映射 + 状态文案中文化（i18n）。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import StatusBadge from './StatusBadge'

describe('StatusBadge', () => {
  it('渲染中文状态文本（online→在线）', () => {
    render(<StatusBadge status="online" />)
    expect(screen.getByText('在线')).toBeInTheDocument()
  })

  it('online 用绿色底', () => {
    render(<StatusBadge status="online" />)
    expect(screen.getByText('在线')).toHaveClass('bg-green-600')
  })

  it('lost 用琥珀底', () => {
    render(<StatusBadge status="lost" />)
    expect(screen.getByText('失联')).toHaveClass('bg-amber-500')
  })

  it('offline 用灰底', () => {
    render(<StatusBadge status="offline" />)
    expect(screen.getByText('离线')).toHaveClass('bg-muted')
  })

  // FR-81：传入 reason 时以原生 title 悬浮提示展示原因
  it('reason 非空时设 title 悬浮提示', () => {
    render(<StatusBadge status="lost" reason="35s 未心跳 > ttl 30s" />)
    expect(screen.getByText('失联')).toHaveAttribute('title', '35s 未心跳 > ttl 30s')
  })

  it('reason 为空时不设 title', () => {
    render(<StatusBadge status="online" reason="" />)
    expect(screen.getByText('在线')).not.toHaveAttribute('title')
  })
})
