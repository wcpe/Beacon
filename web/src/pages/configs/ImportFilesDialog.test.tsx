// ImportFilesDialog 关键路径测试（FR-38）：
// 打开对话框 → 选目标组 → 选文件 → 点导入 → 以正确入参调用 importFiles。
// api/client 被 mock，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由用例断言
vi.mock('../../api/client', () => ({
  importFiles: vi.fn(),
}))

// mock 全局消息提示，避免 toast 依赖
vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ImportFilesDialog from './ImportFilesDialog'
import { importFiles } from '../../api/client'

function renderDialog(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(importFiles).mockResolvedValue({ files: 2, created: 2, updated: 0 })
})

// 环境候选改为「值/显示分离」（FR-70）：value=code、label=「编码 · 名称」
const NS_OPTS = [{ value: 'prod', label: 'prod · 生产' }]

describe('ImportFilesDialog', () => {
  it('选组与文件后点导入，以正确入参调用 importFiles', async () => {
    renderDialog(<ImportFilesDialog namespaces={NS_OPTS} groups={['bw']} />)

    // 打开对话框
    await userEvent.click(screen.getByRole('button', { name: '导入到组' }))
    const dialog = await screen.findByRole('dialog')

    // 选目标组（FR-51：combobox，展开后点选候选；下拉渲染到 body）
    await userEvent.click(screen.getByLabelText('目标组'))
    await userEvent.click(within(await screen.findByRole('listbox')).getByText('bw'))

    // 选两个文件（多文件输入）
    const fileInput = dialog.querySelector('#imp-files') as HTMLInputElement
    const f1 = new File(['a: 1\n'], 'config.yml', { type: 'text/yaml' })
    const f2 = new File(['hello\n'], 'zh.yml', { type: 'text/yaml' })
    await userEvent.upload(fileInput, [f1, f2])
    expect(await screen.findByText('已选 2 个文件')).toBeInTheDocument()

    // 点导入
    await userEvent.click(screen.getByRole('button', { name: '导入' }))

    await waitFor(() => {
      expect(vi.mocked(importFiles)).toHaveBeenCalledWith(
        'prod',
        'bw',
        [
          expect.objectContaining({ path: 'config.yml' }),
          expect.objectContaining({ path: 'zh.yml' }),
        ],
      )
    })
  })

  it('未选目标组时不调用 importFiles', async () => {
    renderDialog(<ImportFilesDialog namespaces={NS_OPTS} groups={['bw']} />)
    await userEvent.click(screen.getByRole('button', { name: '导入到组' }))
    const dialog = await screen.findByRole('dialog')

    const fileInput = dialog.querySelector('#imp-files') as HTMLInputElement
    await userEvent.upload(fileInput, [new File(['x\n'], 'a.yml')])

    await userEvent.click(screen.getByRole('button', { name: '导入' }))
    // 组为空：校验拦截，不发请求
    expect(vi.mocked(importFiles)).not.toHaveBeenCalled()
  })
})
