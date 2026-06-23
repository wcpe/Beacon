// ImportFilesDialog 关键路径测试（FR-38 + FR-66 预览审批）：
// 打开对话框 → 选目标组 → 选文件 → 点预览 → 预览模态出现 → 勾审阅 → 确认导入 → 以正确入参调用 importFiles。
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
  it('选组与文件后点预览，审阅确认才以正确入参调用 importFiles', async () => {
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

    // 点预览：先弹审批模态、列出待传清单，尚未调 importFiles（FR-66）
    await userEvent.click(screen.getByRole('button', { name: '预览' }))
    expect(await screen.findByText('上传预览审批')).toBeInTheDocument()
    expect(screen.getByText('config.yml')).toBeInTheDocument()
    expect(screen.getByText('zh.yml')).toBeInTheDocument()
    expect(vi.mocked(importFiles)).not.toHaveBeenCalled()

    // 勾「我已审阅」→ 确认导入 → 才以正确入参调用 importFiles
    await userEvent.click(screen.getByLabelText('我已审阅全部待传内容'))
    await userEvent.click(screen.getByRole('button', { name: '确认导入' }))

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

  it('未勾审阅时点确认导入不调用 importFiles（审阅闸）', async () => {
    renderDialog(<ImportFilesDialog namespaces={NS_OPTS} groups={['bw']} />)
    await userEvent.click(screen.getByRole('button', { name: '导入到组' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(screen.getByLabelText('目标组'))
    await userEvent.click(within(await screen.findByRole('listbox')).getByText('bw'))
    const fileInput = dialog.querySelector('#imp-files') as HTMLInputElement
    await userEvent.upload(fileInput, [new File(['x\n'], 'a.yml', { type: 'text/yaml' })])

    await userEvent.click(screen.getByRole('button', { name: '预览' }))
    await screen.findByText('上传预览审批')
    // 未勾审阅 → 确认按钮禁用，不发请求
    expect(screen.getByRole('button', { name: '确认导入' })).toBeDisabled()
    expect(vi.mocked(importFiles)).not.toHaveBeenCalled()
  })

  it('预览取消不入库（不调用 importFiles）', async () => {
    renderDialog(<ImportFilesDialog namespaces={NS_OPTS} groups={['bw']} />)
    await userEvent.click(screen.getByRole('button', { name: '导入到组' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(screen.getByLabelText('目标组'))
    await userEvent.click(within(await screen.findByRole('listbox')).getByText('bw'))
    const fileInput = dialog.querySelector('#imp-files') as HTMLInputElement
    await userEvent.upload(fileInput, [new File(['x\n'], 'a.yml', { type: 'text/yaml' })])

    await userEvent.click(screen.getByRole('button', { name: '预览' }))
    await screen.findByText('上传预览审批')
    await userEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(vi.mocked(importFiles)).not.toHaveBeenCalled()
  })

  it('文本文件预览读出并展示内容（FileReader）', async () => {
    renderDialog(<ImportFilesDialog namespaces={NS_OPTS} groups={['bw']} />)
    await userEvent.click(screen.getByRole('button', { name: '导入到组' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(screen.getByLabelText('目标组'))
    await userEvent.click(within(await screen.findByRole('listbox')).getByText('bw'))
    const fileInput = dialog.querySelector('#imp-files') as HTMLInputElement
    await userEvent.upload(fileInput, [new File(['port: 25565\n'], 'cfg.yml', { type: 'text/yaml' })])

    await userEvent.click(screen.getByRole('button', { name: '预览' }))
    await screen.findByText('上传预览审批')
    // 首个文件默认选中 → 异步读出文本内容并渲染
    expect(await screen.findByText('port: 25565')).toBeInTheDocument()
  })

  it('二进制文件标记为二进制 Badge（不读内容）', async () => {
    renderDialog(<ImportFilesDialog namespaces={NS_OPTS} groups={['bw']} />)
    await userEvent.click(screen.getByRole('button', { name: '导入到组' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(screen.getByLabelText('目标组'))
    await userEvent.click(within(await screen.findByRole('listbox')).getByText('bw'))
    const fileInput = dialog.querySelector('#imp-files') as HTMLInputElement
    // .bin 无文本后缀、application/octet-stream → 判二进制
    const bin = new File([new Uint8Array([0, 1, 2, 3])], 'data.bin', { type: 'application/octet-stream' })
    await userEvent.upload(fileInput, [bin])

    await userEvent.click(screen.getByRole('button', { name: '预览' }))
    await screen.findByText('上传预览审批')
    // 选中二进制文件 → 列出二进制 Badge 且内容区提示不预览
    await userEvent.click(screen.getByRole('button', { name: 'data.bin' }))
    expect(screen.getByText('二进制')).toBeInTheDocument()
    expect(screen.getByText('二进制文件不预览内容')).toBeInTheDocument()
  })

  it('未选目标组时不打开预览、不调用 importFiles', async () => {
    renderDialog(<ImportFilesDialog namespaces={NS_OPTS} groups={['bw']} />)
    await userEvent.click(screen.getByRole('button', { name: '导入到组' }))
    const dialog = await screen.findByRole('dialog')

    const fileInput = dialog.querySelector('#imp-files') as HTMLInputElement
    await userEvent.upload(fileInput, [new File(['x\n'], 'a.yml')])

    await userEvent.click(screen.getByRole('button', { name: '预览' }))
    // 组为空：校验拦截，不开预览、不发请求
    expect(screen.queryByText('上传预览审批')).not.toBeInTheDocument()
    expect(vi.mocked(importFiles)).not.toHaveBeenCalled()
  })
})
