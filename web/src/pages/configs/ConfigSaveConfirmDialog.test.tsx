// ConfigSaveConfirmDialog 关键路径测试（FR-67）：
// 展示配置三元组 + diff（上一保存版本 ⟷ 当前编辑态）+ 备注；确认才回调 onConfirm（携回写备注），取消回调 onCancel。
// CodeEditor 被 mock 为 diff 替身，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

// mock Monaco DiffEditor，避免 jsdom 下加载真实编辑器
vi.mock('../../components/CodeEditor', () => ({
  default: (props: { original?: string; modified?: string }) => (
    <div data-testid="diff-editor" data-original={props.original} data-modified={props.modified} />
  ),
}))

// mock 全局消息提示，避免 toast 依赖
vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ConfigSaveConfirmDialog from './ConfigSaveConfirmDialog'

function setup(overrides: Partial<React.ComponentProps<typeof ConfigSaveConfirmDialog>> = {}) {
  const onConfirm = vi.fn()
  const onCancel = vi.fn()
  const onCommentChange = vi.fn()
  render(
    <ConfigSaveConfirmDialog
      open
      namespace="prod"
      group="gA"
      dataId="app.yml"
      format="yaml"
      originalContent={'k: 1\n'}
      currentContent={'k: 2\n'}
      comment="管理台保存"
      pending={false}
      onCommentChange={onCommentChange}
      onConfirm={onConfirm}
      onCancel={onCancel}
      {...overrides}
    />,
  )
  return { onConfirm, onCancel, onCommentChange }
}

describe('ConfigSaveConfirmDialog', () => {
  it('展示配置三元组与 diff（上一保存版本 ⟷ 当前编辑态）', () => {
    setup()
    expect(screen.getByText('保存确认')).toBeInTheDocument()
    expect(screen.getByText('配置：prod / gA / app.yml')).toBeInTheDocument()
    const diff = screen.getByTestId('diff-editor')
    expect(diff).toHaveAttribute('data-original', 'k: 1\n')
    expect(diff).toHaveAttribute('data-modified', 'k: 2\n')
  })

  it('点确认保存回调 onConfirm 并回写备注', async () => {
    const { onConfirm, onCommentChange } = setup()
    await userEvent.click(screen.getByRole('button', { name: '确认保存' }))
    expect(onCommentChange).toHaveBeenCalledWith('管理台保存')
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('备注可编辑，确认时回写编辑后的备注', async () => {
    const { onConfirm, onCommentChange } = setup({ comment: '' })
    const input = screen.getByLabelText('备注')
    await userEvent.type(input, '改端口')
    await userEvent.click(screen.getByRole('button', { name: '确认保存' }))
    expect(onCommentChange).toHaveBeenCalledWith('改端口')
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('点取消回调 onCancel、不回调 onConfirm', async () => {
    const { onConfirm, onCancel } = setup()
    await userEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(onCancel).toHaveBeenCalledTimes(1)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('内容无变化时给提示', () => {
    setup({ originalContent: 'same\n', currentContent: 'same\n' })
    expect(screen.getByText('内容无变化')).toBeInTheDocument()
  })
})
