import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'

import { describe, expect, it } from 'vitest'

const styles = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8')

describe('全局样式', () => {
  it('直接使用完整背景 token 并引用带空格的字体名', () => {
    expect(styles).toMatch(/html\s*\{[\s\S]*?background:\s*var\(--background\);/)
    expect(styles).toMatch(/font-family:\s*["']Geist Variable["']/)
  })
})
