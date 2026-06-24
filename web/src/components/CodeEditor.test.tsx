// CodeEditor 客户端格式校验单测（FR-75）：
// 编辑模式下对内容跑 lintContent，非法时编辑器旁出行内错误条（行号 + 信息）
// 并经 onValidate 上抛错误（合法上抛 null）；diff 模式不校验。
// @monaco-editor/react 被 mock 为可控替身（暴露一个文本域驱动 onChange），保证 jsdom 下稳定。
import { useState } from 'react'
import { describe, it, expect, vi, afterEach } from 'vitest'
import { render, screen, fireEvent, act } from '@testing-library/react'

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

describe('CodeEditor 去抖校验（FR-75）', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  // 受控壳：把 onChange 写回 value，模拟父层持有编辑内容
  function Controlled({ onValidate }: { onValidate: (e: unknown) => void }) {
    const [value, setValue] = useState('a: 1\n')
    return (
      <CodeEditor value={value} language="yaml" onChange={setValue} onValidate={onValidate} />
    )
  }

  it('编辑后去抖落定才出错误条；落定前保持「校验中」禁用保存', () => {
    vi.useFakeTimers()
    const onValidate = vi.fn()
    render(<Controlled onValidate={onValidate} />)
    // 初始合法：上抛 null
    expect(onValidate).toHaveBeenLastCalledWith(null)

    // 改成非法（Tab 缩进），去抖未落定：错误条尚未出现
    act(() => {
      fireEvent.change(screen.getByTestId('monaco-editor'), { target: { value: 'a:\n\tb: 1\n' } })
    })
    expect(screen.queryByText('格式错误，无法发布')).not.toBeInTheDocument()
    // 校验中占位：onValidate 收到非 null（line=0），保存保持禁用，不让非法漏过
    const pendingArg = onValidate.mock.calls.at(-1)![0] as { line: number } | null
    expect(pendingArg).not.toBeNull()
    expect(pendingArg!.line).toBe(0)

    // 推进去抖（250ms）后真实错误落定
    act(() => {
      vi.advanceTimersByTime(250)
    })
    expect(screen.getByText('格式错误，无法发布')).toBeInTheDocument()
    const settledArg = onValidate.mock.calls.at(-1)![0] as { line: number } | null
    expect(settledArg).not.toBeNull()
    expect(settledArg!.line).toBe(2)
  })
})
