// NamespacesPage 环境管理页单测（FR-53）：锁定四项行为——
// ① 列表渲染环境编码 / 名称；② 改名提交携带 code + 新 name；
// ③ 删除二次确认后调 deleteNamespace；④ 守卫拒删（后端 409 中文错误）经 showError 提示。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 写操作反馈：spy showError / showSuccess
const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('../components/useMessage', () => ({
  useMessage: () => ({ showError, showSuccess }),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  listNamespaces: vi.fn(),
  createNamespace: vi.fn(),
  updateNamespace: vi.fn(),
  deleteNamespace: vi.fn(),
}))

import NamespacesPage from './NamespacesPage'
import {
  listNamespaces,
  updateNamespace,
  deleteNamespace,
} from '../api/client'

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  showError.mockClear()
  showSuccess.mockClear()
  vi.mocked(listNamespaces).mockResolvedValue([
    { code: 'prod', name: '生产' },
    { code: 'test', name: '测试' },
  ])
  vi.mocked(updateNamespace).mockResolvedValue({ code: 'prod', name: '生产环境' })
  vi.mocked(deleteNamespace).mockResolvedValue(undefined)
})

describe('NamespacesPage 环境管理（FR-53）', () => {
  it('列表渲染环境编码与名称', async () => {
    renderPage(<NamespacesPage />)
    expect(await screen.findByText('prod')).toBeInTheDocument()
    expect(screen.getByText('生产')).toBeInTheDocument()
    expect(screen.getByText('test')).toBeInTheDocument()
    expect(screen.getByText('测试')).toBeInTheDocument()
  })

  it('改名提交携带 code 与新 name', async () => {
    renderPage(<NamespacesPage />)
    await screen.findByText('prod')
    // 打开 prod 行的改名对话框
    const renameButtons = screen.getAllByRole('button', { name: '改名' })
    await userEvent.click(renameButtons[0])
    const dialog = await screen.findByRole('dialog')
    const input = within(dialog).getByLabelText('名称')
    await userEvent.clear(input)
    await userEvent.type(input, '生产环境')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(vi.mocked(updateNamespace)).toHaveBeenCalledWith('prod', '生产环境'))
  })

  it('删除二次确认后调 deleteNamespace', async () => {
    renderPage(<NamespacesPage />)
    await screen.findByText('prod')
    const deleteButtons = screen.getAllByRole('button', { name: '删除' })
    await userEvent.click(deleteButtons[0])
    const alert = await screen.findByRole('alertdialog')
    await userEvent.click(within(alert).getByRole('button', { name: '确认删除' }))
    await waitFor(() => expect(vi.mocked(deleteNamespace)).toHaveBeenCalledWith('prod'))
  })

  it('守卫拒删时后端中文错误经 showError 提示', async () => {
    vi.mocked(deleteNamespace).mockRejectedValueOnce(new Error('环境下仍有已注册实例，请先下线后再删除'))
    renderPage(<NamespacesPage />)
    await screen.findByText('prod')
    const deleteButtons = screen.getAllByRole('button', { name: '删除' })
    await userEvent.click(deleteButtons[0])
    const alert = await screen.findByRole('alertdialog')
    await userEvent.click(within(alert).getByRole('button', { name: '确认删除' }))
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith('环境下仍有已注册实例，请先下线后再删除'),
    )
  })
})
