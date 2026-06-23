// ReverseFetchReviewPanel 关键路径测试（FR-60）：
// 默认选定策略（文本+未超阈值+未命中规则才默认选中；超阈值/规则/二进制默认排除）；
// 选定集含超阈值未勾确认时拦提交、不发请求；勾确认后以正确入参（selectedPaths + confirmOverThreshold）调用 submit；
// 「忽略此项」临时取消选中；保存为持久规则（exact）以正确入参调用 createIgnoreRule。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { ReverseFetchScanFileView, ReverseFetchTaskView } from '../../api/types'

vi.mock('../../api/client', () => ({
  submitReverseFetchTask: vi.fn(),
  createIgnoreRule: vi.fn(),
}))

vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ReverseFetchReviewPanel from './ReverseFetchReviewPanel'
import { submitReverseFetchTask, createIgnoreRule } from '../../api/client'

function file(
  path: string,
  opts: Partial<ReverseFetchScanFileView> = {},
): ReverseFetchScanFileView {
  return {
    path,
    size: 1024,
    isText: true,
    overThreshold: false,
    ignoredByRule: false,
    ...opts,
  }
}

function task(files: ReverseFetchScanFileView[]): ReverseFetchTaskView {
  return {
    id: 42,
    namespace: 'prod',
    serverId: 'lobby-1',
    scope: 'group',
    group: 'area1',
    target: '',
    status: 'pending-review',
    scanCommandId: 1,
    submitCommandId: 0,
    totalFiles: files.length,
    selectedCount: 0,
    overThresholdCount: files.filter((f) => f.overThreshold).length,
    skippedCount: 0,
    files,
    selectedPaths: [],
    operator: 'admin',
    note: '',
    createdAt: '',
    updatedAt: '',
  }
}

function renderPanel(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(submitReverseFetchTask).mockResolvedValue(task([]))
  vi.mocked(createIgnoreRule).mockResolvedValue({
    id: 1,
    namespace: 'prod',
    scope: 'group',
    group: 'area1',
    target: '',
    ruleType: 'exact',
    pattern: 'AllinCore/config.yml',
    comment: '',
    operator: 'admin',
    createdAt: '',
  })
})

describe('ReverseFetchReviewPanel', () => {
  it('默认选定策略：文本未超阈值默认选中，超阈值/命中规则/二进制默认排除', () => {
    renderPanel(
      <ReverseFetchReviewPanel
        task={task([
          file('AllinCore/config.yml'),
          file('big.dat', { overThreshold: true }),
          file('logs/latest.log', { ignoredByRule: true }),
          file('icon.png', { isText: false }),
        ])}
        onSubmitted={() => {}}
      />,
    )
    const ok = screen.getByLabelText('AllinCore/config.yml') as HTMLElement
    const over = screen.getByLabelText('big.dat') as HTMLElement
    const ignored = screen.getByLabelText('logs/latest.log') as HTMLElement
    const bin = screen.getByLabelText('icon.png') as HTMLElement
    expect(ok).toHaveAttribute('data-state', 'checked')
    expect(over).toHaveAttribute('data-state', 'unchecked')
    expect(ignored).toHaveAttribute('data-state', 'unchecked')
    expect(bin).toHaveAttribute('data-state', 'unchecked')
  })

  it('提交：以默认选定集 + confirmOverThreshold=false 调用 submit', async () => {
    renderPanel(
      <ReverseFetchReviewPanel
        task={task([file('AllinCore/config.yml'), file('Residence/config.yml')])}
        onSubmitted={() => {}}
      />,
    )
    await userEvent.click(screen.getByRole('button', { name: '提交选定' }))
    await waitFor(() => {
      expect(vi.mocked(submitReverseFetchTask)).toHaveBeenCalledWith(42, {
        selectedPaths: ['AllinCore/config.yml', 'Residence/config.yml'],
        confirmOverThreshold: false,
      })
    })
  })

  it('选定超阈值文件但未勾确认时拦提交、不发请求', async () => {
    renderPanel(
      <ReverseFetchReviewPanel
        task={task([file('big.dat', { overThreshold: true })])}
        onSubmitted={() => {}}
      />,
    )
    // 手动勾选超阈值文件（默认排除）
    await userEvent.click(screen.getByLabelText('big.dat'))
    await userEvent.click(screen.getByRole('button', { name: '提交选定' }))
    expect(vi.mocked(submitReverseFetchTask)).not.toHaveBeenCalled()
  })

  it('勾确认后超阈值文件可提交，confirmOverThreshold=true', async () => {
    renderPanel(
      <ReverseFetchReviewPanel
        task={task([file('big.dat', { overThreshold: true })])}
        onSubmitted={() => {}}
      />,
    )
    await userEvent.click(screen.getByLabelText('big.dat'))
    // 勾「我已确认纳入超阈值文件」
    await userEvent.click(screen.getByRole('checkbox', { name: /我已确认纳入超阈值文件/ }))
    await userEvent.click(screen.getByRole('button', { name: '提交选定' }))
    await waitFor(() => {
      expect(vi.mocked(submitReverseFetchTask)).toHaveBeenCalledWith(42, {
        selectedPaths: ['big.dat'],
        confirmOverThreshold: true,
      })
    })
  })

  it('「忽略此项」临时取消选中（不进选定集）', async () => {
    renderPanel(
      <ReverseFetchReviewPanel
        task={task([file('AllinCore/config.yml'), file('Residence/config.yml')])}
        onSubmitted={() => {}}
      />,
    )
    const row = screen.getByLabelText('AllinCore/config.yml').closest('li') as HTMLElement
    await userEvent.click(
      within(row).getByRole('button', { name: /忽略此项 AllinCore\/config\.yml/ }),
    )
    await userEvent.click(screen.getByRole('button', { name: '提交选定' }))
    await waitFor(() => {
      expect(vi.mocked(submitReverseFetchTask)).toHaveBeenCalledWith(42, {
        selectedPaths: ['Residence/config.yml'],
        confirmOverThreshold: false,
      })
    })
  })

  it('保存为持久规则（精确）以正确入参调用 createIgnoreRule', async () => {
    renderPanel(
      <ReverseFetchReviewPanel
        task={task([file('AllinCore/config.yml')])}
        onSubmitted={() => {}}
      />,
    )
    const row = screen.getByLabelText('AllinCore/config.yml').closest('li') as HTMLElement
    await userEvent.click(within(row).getByRole('button', { name: '存为规则' }))
    await waitFor(() => {
      expect(vi.mocked(createIgnoreRule)).toHaveBeenCalledWith({
        namespace: 'prod',
        scope: 'group',
        group: 'area1',
        target: undefined,
        ruleType: 'exact',
        pattern: 'AllinCore/config.yml',
      })
    })
  })
})
