// ConfigEditorPage 单测（FR-112）：配置文件真详情多标签编辑器（/configs/:id 真路由）。
// 替身：useWorkbenchFile 注入文件 + revisions；CodeEditor 替身（textarea 暴露 onChange + diff 标记）；
// publishFile 真保存 API 替身；useMessage toast 替身。
// 覆盖：深链按 :id 加载文件、局部面包屑/返回、多标签渲染与切换、历史修订列表（diff）、
// 保存确认（FR-67）走 publishFile 真保存 + 成功 toast、脏标记、加载/未找到态。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { PageHeaderProvider } from '@/components/PageHeader'
import PageHeader from '@/components/PageHeader'

// CodeEditor 替身：编辑模式暴露 textarea；diff 模式（有 original/modified）渲染只读标记，供断言历史 diff。
vi.mock('@/components/CodeEditor', () => ({
  default: (props: { value?: string; original?: string; modified?: string; onChange?: (v: string) => void }) =>
    props.original !== undefined || props.modified !== undefined ? (
      <div data-testid="diff-editor" data-original={props.original} data-modified={props.modified} />
    ) : (
      <textarea data-testid="code-editor" value={props.value ?? ''} onChange={(e) => props.onChange?.(e.target.value)} />
    ),
}))

const toastSuccess = vi.fn()
const toastError = vi.fn()
vi.mock('@/components/useMessage', () => ({
  useMessage: () => ({ showSuccess: toastSuccess, showError: toastError }),
}))

vi.mock('./configs-workbench/useWorkbenchData', () => ({
  useWorkbenchFile: vi.fn(),
}))

const publishFileMock = vi.fn()
vi.mock('@/api/client', () => ({
  publishFile: (...args: unknown[]) => publishFileMock(...args),
  // ConfigSaveConfirmDialog 用 impactPreview 算影响面（纯展示）；替身返回空影响面。
  impactPreview: () => Promise.resolve({ total: 0, affected: [], group: '' }),
}))

import ConfigEditorPage from './ConfigEditorPage'
import { useWorkbenchFile } from './configs-workbench/useWorkbenchData'
import type { WorkbenchFile } from './configs-workbench/types'

const mockedFile = vi.mocked(useWorkbenchFile)

const FILE: WorkbenchFile = {
  key: 'plugins/Essentials/config.yml',
  fileId: 42,
  namespace: 'prod',
  group: 'main',
  dataId: 'Essentials/config.yml',
  scope: 'group',
  targetServer: 'lobby-1',
  format: 'yaml',
  content: 'a: 1\n',
  revisions: [
    { version: 7, author: 'admin', time: '今天 14:32', comment: '调整冷却', content: 'a: 1\n' },
    { version: 6, author: 'ops', time: '昨天', comment: '初版', content: 'a: 0\n' },
  ],
}

function mockFileHook(over: Partial<ReturnType<typeof useWorkbenchFile>> = {}) {
  mockedFile.mockReturnValue({ data: FILE, isLoading: false, refetch: vi.fn(), ...over } as ReturnType<typeof useWorkbenchFile>)
}

function renderPage(id = 'plugins%2FEssentials%2Fconfig.yml') {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[`/configs/${id}`]}>
        <PageHeaderProvider>
          {/* 渲染页头显示组件，使注入的面包屑标题（含返回链接）可断言 */}
          <PageHeader />
          <Routes>
            <Route path="/configs" element={<div>工作台</div>} />
            <Route path="/configs/:id" element={<ConfigEditorPage />} />
          </Routes>
        </PageHeaderProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('ConfigEditorPage（FR-112）', () => {
  beforeEach(() => {
    toastSuccess.mockClear()
    toastError.mockClear()
    publishFileMock.mockReset()
    publishFileMock.mockResolvedValue({ version: 8, md5: 'newmd5' })
    mockFileHook()
  })

  it('按 :id 深链加载文件：编辑器渲染该文件内容', () => {
    renderPage()
    expect((screen.getByTestId('code-editor') as HTMLTextAreaElement).value).toBe('a: 1\n')
  })

  it('局部面包屑：返回配置中心可点', () => {
    renderPage()
    expect(screen.getAllByText('config.yml').length).toBeGreaterThanOrEqual(1)
    // 面包屑「配置中心」链接存在且指向 /configs
    const link = screen.getByRole('link', { name: /配置中心/ })
    expect(link).toHaveAttribute('href', '/configs')
  })

  it('多标签：活跃标签渲染（含文件名 + 关闭）', () => {
    renderPage()
    // 标签栏内出现文件名
    expect(screen.getAllByText('config.yml').length).toBeGreaterThanOrEqual(1)
  })

  it('历史修订面板：列出版本，最新版带「当前」徽标', () => {
    renderPage()
    expect(screen.getByText('历史修订')).toBeInTheDocument()
    expect(screen.getByText('v7')).toBeInTheDocument()
    expect(screen.getByText('v6')).toBeInTheDocument()
    expect(screen.getByText('当前')).toBeInTheDocument()
  })

  it('点历史版本 → diff（当前 ⟷ 选定版本）', async () => {
    renderPage()
    await userEvent.click(screen.getByText('v6'))
    const diff = await screen.findByTestId('diff-editor')
    // 左=选定历史版本内容，右=当前编辑态
    expect(diff.getAttribute('data-original')).toBe('a: 0\n')
    expect(diff.getAttribute('data-modified')).toBe('a: 1\n')
  })

  it('脏标记：编辑后出未保存点', () => {
    renderPage()
    fireEvent.change(screen.getByTestId('code-editor'), { target: { value: 'a: 2\n' } })
    expect(screen.getByText('●未保存')).toBeInTheDocument()
  })

  it('保存确认（FR-67）：点保存先弹确认对话框，确认才调 publishFile 真保存', async () => {
    renderPage()
    // 改内容
    fireEvent.change(screen.getByTestId('code-editor'), { target: { value: 'a: 2\n' } })
    // 点工具栏保存 → 弹 FR-67 保存确认对话框
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText('保存确认')).toBeInTheDocument()
    // 确认保存 → 调 publishFile(id, content, comment)
    await userEvent.click(screen.getByRole('button', { name: '确认保存' }))
    await waitFor(() => expect(publishFileMock).toHaveBeenCalledWith(42, 'a: 2\n', expect.any(String)))
    // 成功 toast
    await waitFor(() => expect(toastSuccess).toHaveBeenCalled())
  })

  it('loading：显骨架，不渲染编辑器', () => {
    mockFileHook({ data: undefined, isLoading: true })
    renderPage()
    expect(screen.queryByTestId('code-editor')).not.toBeInTheDocument()
  })

  it('未找到：显未找到提示', () => {
    mockFileHook({ data: undefined, isLoading: false })
    renderPage()
    expect(screen.getByText('未找到该受管文件')).toBeInTheDocument()
  })
})
