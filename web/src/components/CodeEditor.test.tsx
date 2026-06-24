// CodeEditor 客户端格式校验单测（FR-75）：
// 编辑模式下对内容跑 lintContent，非法时编辑器旁出行内错误条（行号 + 信息）
// 并经 onValidate 上抛错误（合法上抛 null）；diff 模式不校验。
// @monaco-editor/react 被 mock 为可控替身（暴露一个文本域驱动 onChange），保证 jsdom 下稳定。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

// mock Monaco：Editor 渲染一个 textarea，change 时回调 onChange；DiffEditor 渲染占位。
vi.mock('@monaco-editor/react', () => ({
  __esModule: true,
  default: (props: { value?: string; onChange?: (v: string) => void }) => (
    <textarea
      data-testid="monaco-editor"
      value={props.value}
      onChange={(e) => props.onChange?.(e.target.value)}
    />
  ),
  DiffEditor: () => <div data-testid="monaco-diff" />,
}))

import CodeEditor from './CodeEditor'

describe('CodeEditor 格式校验（FR-75）', () => {
  it('合法 YAML：不出错误条，onValidate 上抛 null', () => {
    const onValidate = vi.fn()
    render(<CodeEditor value={'a: 1\n'} language="yaml" onValidate={onValidate} />)
    expect(screen.queryByText('格式错误，无法发布')).not.toBeInTheDocument()
    expect(onValidate).toHaveBeenLastCalledWith(null)
  })

  it('非法 YAML（Tab 缩进）：出行内错误条且 onValidate 上抛错误', () => {
    const onValidate = vi.fn()
    render(<CodeEditor value={'a:\n\tb: 1\n'} language="yaml" onValidate={onValidate} />)
    expect(screen.getByText('格式错误，无法发布')).toBeInTheDocument()
    // 行号 + 信息形如「第 2 行：…」
    expect(screen.getByText(/第 2 行/)).toBeInTheDocument()
    const lastArg = onValidate.mock.calls.at(-1)![0]
    expect(lastArg).not.toBeNull()
    expect(lastArg.line).toBe(2)
  })

  it('非法 JSON：出错误条', () => {
    render(<CodeEditor value={'{"a": 1,}'} language="json" />)
    expect(screen.getByText('格式错误，无法发布')).toBeInTheDocument()
  })

  it('diff 模式不做校验（不出错误条）', () => {
    render(<CodeEditor original={'a: 1\n'} modified={'a:\n\tb: 1\n'} language="yaml" />)
    expect(screen.queryByText('格式错误，无法发布')).not.toBeInTheDocument()
    expect(screen.getByTestId('monaco-diff')).toBeInTheDocument()
  })
})
