// ChipSelect 单测（FR-115）：面板头部 chip 下拉。
// 覆盖当前值回显、打开下拉选项、选择回调、leading 前缀渲染、未知值回退显原值。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import ChipSelect from './ChipSelect'

const OPTIONS = [
  { value: 'global', label: '全局' },
  { value: 'group:main', label: '组 main' },
  { value: 'server:lobby-1', label: '实例 lobby-1' },
]

describe('ChipSelect（FR-115）', () => {
  it('触发器回显当前选中项 label', () => {
    render(<ChipSelect value="group:main" options={OPTIONS} onChange={vi.fn()} />)
    expect(screen.getByText('组 main')).toBeInTheDocument()
  })

  it('值不在候选中：回退显原始 value', () => {
    render(<ChipSelect value="unknown-scope" options={OPTIONS} onChange={vi.fn()} />)
    expect(screen.getByText('unknown-scope')).toBeInTheDocument()
  })

  it('渲染 leading 前缀节点', () => {
    render(
      <ChipSelect
        value="global"
        options={OPTIONS}
        onChange={vi.fn()}
        leading={<span data-testid="chip-leading" />}
      />,
    )
    expect(screen.getByTestId('chip-leading')).toBeInTheDocument()
  })

  it('打开下拉点选项触发 onChange 传对应 value', async () => {
    const onChange = vi.fn()
    render(<ChipSelect value="global" options={OPTIONS} onChange={onChange} />)
    await userEvent.click(screen.getByRole('button'))
    // 下拉项出现后点击「实例 lobby-1」
    const item = await screen.findByText('实例 lobby-1')
    await userEvent.click(item)
    expect(onChange).toHaveBeenCalledWith('server:lobby-1')
  })
})
