// 带行号槽的纯文本编辑器：复用 textarea + 等宽字体 + CSS 行号列。
// 不引 CodeMirror/Monaco（依赖管理需确认，本批不引），行号随内容行数生成、与文本框同步滚动。

import { useRef } from 'react'

export default function CodeEditor({
  value,
  onChange,
  rows = 16,
  placeholder,
}: {
  value: string
  onChange: (v: string) => void
  rows?: number
  placeholder?: string
}) {
  const gutterRef = useRef<HTMLDivElement>(null)

  // 行号文本：按内容行数生成 1..n（空内容也至少一行）
  const lineCount = value.length === 0 ? 1 : value.split('\n').length
  const gutter = Array.from({ length: lineCount }, (_, i) => i + 1).join('\n')

  // 文本框滚动时同步行号槽的纵向偏移
  function onScroll(e: React.UIEvent<HTMLTextAreaElement>) {
    if (gutterRef.current) gutterRef.current.scrollTop = e.currentTarget.scrollTop
  }

  return (
    <div className="code-editor">
      <div className="code-editor-gutter" ref={gutterRef} aria-hidden="true">
        {gutter}
      </div>
      <textarea
        className="code-editor-textarea"
        value={value}
        rows={rows}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onScroll={onScroll}
        spellCheck={false}
      />
    </div>
  )
}
