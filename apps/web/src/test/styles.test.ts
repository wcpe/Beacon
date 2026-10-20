import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const styles = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8')

describe('全局样式', () => {
  it('直接使用完整背景 token 并引用带空格的字体名', () => {
    expect(styles).toMatch(/html\s*\{[\s\S]*?background:\s*var\(--background\);/)
    expect(styles).toMatch(/font-family:\s*["']Geist Variable["']/)
  })

  it('路由入场使用明显的上浮动画且不被减弱动态偏好关闭', () => {
    expect(styles).toMatch(
      /@keyframes shell-route-enter[\s\S]*?translateY\(14px\) scale\(0\.995\)[\s\S]*?translateY\(0\) scale\(1\)/,
    )
    expect(styles).toMatch(
      /\[data-slot='route-transition'\]\s*\{\s*animation:\s*shell-route-enter 300ms cubic-bezier\(0\.22, 1, 0\.36, 1\) both;/,
    )
    expect(styles).not.toMatch(
      /@media\s*\(prefers-reduced-motion:\s*reduce\)[\s\S]*?\[data-slot=['"]route-transition['"][\s\S]*?animation:\s*none;/,
    )
  })
})
