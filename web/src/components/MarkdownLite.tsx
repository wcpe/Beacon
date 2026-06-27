// 轻量 markdown 渲染（FR-100 安全渲染）：把 GitHub release 正文的基础 markdown 解析为 React 元素。
//
// 仅支持需要的子集：## / ### 标题、- 或 * 列表、**加粗**、空行分段。其余原样作为段落文本。
// 安全：所有文本均作为 React 文本子节点交由 React 自动转义，绝不使用 dangerouslySetInnerHTML，
// 从根本上杜绝 release 正文里的 HTML / 脚本被注入执行（防 XSS）。

import { Fragment, type ReactNode } from 'react'

// 行内加粗解析：把 **加粗** 片段渲染为 <strong>，其余为普通文本。
// 用全局正则切分，偶数段为普通文本、奇数段为加粗内容；React 自动转义每段文本。
function renderInline(text: string): ReactNode[] {
  const parts = text.split(/\*\*(.+?)\*\*/g)
  return parts.map((seg, i) =>
    i % 2 === 1 ? <strong key={i}>{seg}</strong> : <Fragment key={i}>{seg}</Fragment>,
  )
}

// 解析后的块级节点类型。
type Block =
  | { kind: 'heading'; level: 2 | 3; text: string }
  | { kind: 'list'; items: string[] }
  | { kind: 'paragraph'; lines: string[] }

// 把原始 markdown 文本按行解析为块级节点序列。
function parseBlocks(md: string): Block[] {
  const blocks: Block[] = []
  // 段落 / 列表的累积缓冲，遇到块边界（空行 / 标题 / 列表项切换）时收口。
  let para: string[] = []
  let list: string[] = []

  // 收口当前累积的段落缓冲。
  function flushPara() {
    if (para.length > 0) {
      blocks.push({ kind: 'paragraph', lines: para })
      para = []
    }
  }
  // 收口当前累积的列表缓冲。
  function flushList() {
    if (list.length > 0) {
      blocks.push({ kind: 'list', items: list })
      list = []
    }
  }

  for (const rawLine of md.split('\n')) {
    const line = rawLine.replace(/\r$/, '')
    const trimmed = line.trim()

    // 空行 = 段落 / 列表分隔
    if (trimmed === '') {
      flushPara()
      flushList()
      continue
    }

    // 标题：### 优先于 ##（更长前缀先判）
    const h3 = /^###\s+(.*)$/.exec(trimmed)
    if (h3) {
      flushPara()
      flushList()
      blocks.push({ kind: 'heading', level: 3, text: h3[1] })
      continue
    }
    const h2 = /^##\s+(.*)$/.exec(trimmed)
    if (h2) {
      flushPara()
      flushList()
      blocks.push({ kind: 'heading', level: 2, text: h2[1] })
      continue
    }

    // 列表项：- 或 * 起头
    const li = /^[-*]\s+(.*)$/.exec(trimmed)
    if (li) {
      flushPara()
      list.push(li[1])
      continue
    }

    // 普通文本行：归入当前段落（列表已开则先收口列表）
    flushList()
    para.push(trimmed)
  }

  flushPara()
  flushList()
  return blocks
}

// 把块级节点序列渲染为 React 元素。
function renderBlocks(blocks: Block[]): ReactNode {
  return blocks.map((block, i) => {
    switch (block.kind) {
      case 'heading':
        return block.level === 2 ? (
          <h3 key={i} className="mt-3 mb-1 text-sm font-semibold first:mt-0">
            {renderInline(block.text)}
          </h3>
        ) : (
          <h4 key={i} className="mt-2 mb-1 text-sm font-medium first:mt-0">
            {renderInline(block.text)}
          </h4>
        )
      case 'list':
        return (
          <ul key={i} className="my-1 list-disc space-y-0.5 pl-5">
            {block.items.map((item, j) => (
              <li key={j}>{renderInline(item)}</li>
            ))}
          </ul>
        )
      case 'paragraph':
        return (
          <p key={i} className="my-1 first:mt-0 last:mb-0">
            {block.lines.map((ln, j) => (
              <Fragment key={j}>
                {j > 0 && <br />}
                {renderInline(ln)}
              </Fragment>
            ))}
          </p>
        )
    }
  })
}

// 轻量 markdown 渲染组件：支持 ## / ### 标题、- / * 列表、**加粗**、空行分段。
export default function MarkdownLite({ source, className }: { source: string; className?: string }) {
  const blocks = parseBlocks(source)
  return <div className={className}>{renderBlocks(blocks)}</div>
}
