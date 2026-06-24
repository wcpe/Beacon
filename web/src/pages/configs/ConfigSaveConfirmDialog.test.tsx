// ConfigSaveConfirmDialog 关键路径测试（FR-67 + FR-79）：
// 展示配置三元组 + diff（上一保存版本 ⟷ 当前编辑态）+ 备注 + 发布影响面；确认才回调 onConfirm（携回写备注），取消回调 onCancel。
// CodeEditor 被 mock 为 diff 替身、impactPreview 被 mock 为可控数据，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

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

// mock 影响面预览端点（FR-79），由各用例注入数据
vi.mock('../../api/client', () => ({
  impactPreview: vi.fn(),
}))

import ConfigSaveConfirmDialog from './ConfigSaveConfirmDialog'
import { impactPreview } from '../../api/client'
import type { ImpactView } from '../../api/types'

beforeEach(() => {
  vi.mocked(impactPreview).mockResolvedValue({
    namespace: 'prod', scopeLevel: 'group', group: 'gA', scopeTarget: '',
    affected: ['s1', 's2'], total: 2,
  } satisfies ImpactView)
})

function renderWithClient(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

function setup(overrides: Partial<React.ComponentProps<typeof ConfigSaveConfirmDialog>> = {}) {
  const onConfirm = vi.fn()
  const onCancel = vi.fn()
  const onCommentChange = vi.fn()
  renderWithClient(
    <ConfigSaveConfirmDialog
      open
      namespace="prod"
      group="gA"
      dataId="app.yml"
      scopeLevel="group"
      scopeTarget=""
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

  it('展示发布影响面：将影响 N 台在线服 + serverId 列表（FR-79）', async () => {
    setup()
    expect(await screen.findByText('将影响 2 台在线服：s1、s2')).toBeInTheDocument()
  })

  it('影响面为 0 台时给「无在线子服受影响」提示（FR-79）', async () => {
    vi.mocked(impactPreview).mockResolvedValue({
      namespace: 'prod', scopeLevel: 'server', group: 'gA', scopeTarget: 's404',
      affected: [], total: 0,
    } satisfies ImpactView)
    setup({ scopeLevel: 'server', scopeTarget: 's404' })
    expect(await screen.findByText('当前无在线子服会受本次发布影响')).toBeInTheDocument()
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
