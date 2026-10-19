// F6 回归守护：骨架屏的脉冲动画必须尊重系统「减弱动态偏好」。
//
// 动画修在 Skeleton 基类上一处，因此 TableSkeleton / CardGridSkeleton / TileGridSkeleton /
// FormSkeleton 全部自动继承；这里同时校验基类与一个组合骨架，防止将来有人在局部
// 覆盖类名时把 motion-reduce:animate-none 丢掉（持续性脉冲对前庭敏感用户不友好）。
import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { FormSkeleton, Skeleton } from '@beacon/ui'

describe('骨架屏的减弱动态偏好降级', () => {
  it('Skeleton 基类同时带脉冲动画与 motion-reduce 关闭', () => {
    const { container } = render(<Skeleton />)
    const el = container.querySelector('[data-slot="skeleton"]')

    expect(el).not.toBeNull()
    expect(el).toHaveClass('animate-pulse')
    // 关键断言：prefers-reduced-motion 下动画必须可被关闭
    expect(el).toHaveClass('motion-reduce:animate-none')
  })

  it('FormSkeleton 等组合骨架继承同一降级', () => {
    const { container } = render(<FormSkeleton fields={1} withActions />)
    const bars = container.querySelectorAll('[data-slot="skeleton"]')

    expect(bars.length).toBeGreaterThan(0)
    for (const bar of bars) {
      expect(bar).toHaveClass('motion-reduce:animate-none')
    }
  })
})
