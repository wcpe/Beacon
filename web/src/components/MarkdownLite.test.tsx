// 轻量 markdown 渲染单测（FR-100 安全渲染）：锁定基础语法解析 + XSS 防护。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import MarkdownLite from './MarkdownLite'

describe('MarkdownLite', () => {
  it('## / ### 渲染为标题', () => {
    render(<MarkdownLite source={'## 二级标题\n### 三级标题'} />)
    expect(screen.getByRole('heading', { name: '二级标题' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '三级标题' })).toBeInTheDocument()
  })

  it('- / * 渲染为列表项', () => {
    const { container } = render(<MarkdownLite source={'- 项一\n* 项二'} />)
    const items = container.querySelectorAll('li')
    expect(items).toHaveLength(2)
    expect(items[0].textContent).toBe('项一')
    expect(items[1].textContent).toBe('项二')
  })

  it('**加粗** 渲染为 strong', () => {
    render(<MarkdownLite source={'这是 **重点** 内容'} />)
    const strong = screen.getByText('重点')
    expect(strong.tagName).toBe('STRONG')
  })

  it('空行分段为多个段落', () => {
    const { container } = render(<MarkdownLite source={'第一段\n\n第二段'} />)
    expect(container.querySelectorAll('p')).toHaveLength(2)
  })

  it('原始 HTML 作为纯文本转义，不注入元素（防 XSS）', () => {
    const { container } = render(
      <MarkdownLite source={'<img src=x onerror="alert(1)"> <script>bad()</script>'} />,
    )
    // 不产生真实 img / script 元素，原文以文本形式呈现
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('script')).toBeNull()
    expect(container.textContent).toContain('<img src=x onerror="alert(1)">')
    expect(container.textContent).toContain('<script>bad()</script>')
  })
})
