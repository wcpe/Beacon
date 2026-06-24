// DestructiveConfirmDialog 通用破坏性二次确认单测（FR-76）：锁定五项行为——
// ① 渲染标题 / 动作描述；② 点确认调 onConfirm，点取消不调；
// ③ impacts 非空时渲染影响摘要（脱链哪层 / 影响哪些服）；
// ④ 传 confirmPhrase 时手输不符确认禁用、相符启用；⑤ pending 时确认禁用。
import { describe, it, expect, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import DestructiveConfirmDialog from './DestructiveConfirmDialog'

describe('DestructiveConfirmDialog 破坏性二次确认（FR-76）', () => {
  it('渲染标题与破坏性动作描述', async () => {
    render(
      <DestructiveConfirmDialog
        open
        onOpenChange={() => {}}
        title="删除环境「生产」？"
        description="此操作不可恢复。"
        confirmLabel="确认删除"
        onConfirm={vi.fn()}
      />,
    )
    const alert = await screen.findByRole('alertdialog')
    expect(within(alert).getByText('删除环境「生产」？')).toBeInTheDocument()
    expect(within(alert).getByText('此操作不可恢复。')).toBeInTheDocument()
  })

  it('点确认调 onConfirm，点取消不调', async () => {
    const onConfirm = vi.fn()
    render(
      <DestructiveConfirmDialog
        open
        onOpenChange={() => {}}
        title="t"
        description="d"
        confirmLabel="确认删除"
        onConfirm={onConfirm}
      />,
    )
    const alert = await screen.findByRole('alertdialog')
    await userEvent.click(within(alert).getByRole('button', { name: '确认删除' }))
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('点取消不调 onConfirm', async () => {
    const onConfirm = vi.fn()
    render(
      <DestructiveConfirmDialog
        open
        onOpenChange={() => {}}
        title="t"
        description="d"
        confirmLabel="确认删除"
        onConfirm={onConfirm}
      />,
    )
    const alert = await screen.findByRole('alertdialog')
    await userEvent.click(within(alert).getByRole('button', { name: '取消' }))
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('impacts 非空时渲染影响摘要', async () => {
    render(
      <DestructiveConfirmDialog
        open
        onOpenChange={() => {}}
        title="t"
        description="d"
        confirmLabel="确认删除"
        impacts={['脱链该环境下全部配置覆盖层', '影响所有已注册子服']}
        onConfirm={vi.fn()}
      />,
    )
    const alert = await screen.findByRole('alertdialog')
    expect(within(alert).getByText('脱链该环境下全部配置覆盖层')).toBeInTheDocument()
    expect(within(alert).getByText('影响所有已注册子服')).toBeInTheDocument()
  })

  it('传 confirmPhrase 时手输不符确认禁用、相符启用', async () => {
    const onConfirm = vi.fn()
    render(
      <DestructiveConfirmDialog
        open
        onOpenChange={() => {}}
        title="t"
        description="d"
        confirmLabel="确认吊销"
        confirmPhrase="ci-key"
        onConfirm={onConfirm}
      />,
    )
    const alert = await screen.findByRole('alertdialog')
    const confirmBtn = within(alert).getByRole('button', { name: '确认吊销' })
    // 未输入：禁用
    expect(confirmBtn).toBeDisabled()
    const input = within(alert).getByLabelText('输入确认')
    // 输错：仍禁用
    await userEvent.type(input, 'wrong')
    expect(confirmBtn).toBeDisabled()
    // 输对：启用
    await userEvent.clear(input)
    await userEvent.type(input, 'ci-key')
    expect(confirmBtn).toBeEnabled()
    await userEvent.click(confirmBtn)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('复述比对忽略首尾空白：phrase 带尾空格时照抄正文也能确认（不卡死）', async () => {
    render(
      <DestructiveConfirmDialog
        open
        onOpenChange={() => {}}
        title="t"
        description="d"
        confirmLabel="确认吊销"
        confirmPhrase="Prod Key "
        onConfirm={vi.fn()}
      />,
    )
    const alert = await screen.findByRole('alertdialog')
    const confirmBtn = within(alert).getByRole('button', { name: '确认吊销' })
    const input = within(alert).getByLabelText('输入确认')
    // 用户照抄可见名称（无尾空格）：trim 后相符 → 启用
    await userEvent.type(input, 'Prod Key')
    expect(confirmBtn).toBeEnabled()
    // 仍区分大小写：大小写不符 → 禁用
    await userEvent.clear(input)
    await userEvent.type(input, 'prod key')
    expect(confirmBtn).toBeDisabled()
  })

  it('pending 时确认按钮禁用', async () => {
    render(
      <DestructiveConfirmDialog
        open
        onOpenChange={() => {}}
        title="t"
        description="d"
        confirmLabel="确认删除"
        pending
        onConfirm={vi.fn()}
      />,
    )
    const alert = await screen.findByRole('alertdialog')
    expect(within(alert).getByRole('button', { name: '确认删除' })).toBeDisabled()
  })
})
