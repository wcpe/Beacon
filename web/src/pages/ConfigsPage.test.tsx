// ConfigsPage 关键路径冒烟测试（重构安全网）：
// 覆盖「渲染文件树 → 点击打开标签 → 切 Diff 视图 → 保存」核心交互。
// Monaco 编辑器与 api/client 均被 mock，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// 用 textarea 替身渲染 Monaco：暴露 value/onChange，编辑态可输入、Diff 态只读。
vi.mock('../components/CodeEditor', () => ({
  default: (props: { value?: string; modified?: string; onChange?: (v: string) => void }) => (
    <textarea
      data-testid="code-editor"
      value={props.value ?? props.modified ?? ''}
      onChange={(e) => props.onChange?.(e.target.value)}
      readOnly={props.onChange === undefined}
    />
  ),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  listConfigs: vi.fn(),
  listInstances: vi.fn(),
  listNamespaces: vi.fn(),
  zoneSummary: vi.fn(),
  getConfig: vi.fn(),
  listRevisions: vi.fn(),
  diffConfig: vi.fn(),
  effectiveConfig: vi.fn(),
  createConfig: vi.fn(),
  publishConfig: vi.fn(),
}))

import ConfigsPage from './ConfigsPage'
import {
  listConfigs,
  listInstances,
  listNamespaces,
  zoneSummary,
  getConfig,
  listRevisions,
  diffConfig,
  publishConfig,
} from '../api/client'

// 单条配置样例
const CONFIG = {
  id: 1,
  namespace: 'prod',
  group: 'gA',
  dataId: 'app.yml',
  scopeLevel: 'global',
  scopeTarget: '',
  format: 'yaml',
  version: 3,
  md5: 'abc123',
  enabled: true,
  updatedAt: '2026-01-01T00:00:00Z',
  content: 'k: v',
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(listConfigs).mockResolvedValue([CONFIG])
  vi.mocked(listInstances).mockResolvedValue([])
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
  vi.mocked(zoneSummary).mockResolvedValue([])
  vi.mocked(getConfig).mockResolvedValue({ ...CONFIG, content: 'k: v\n' })
  vi.mocked(listRevisions).mockResolvedValue([
    { version: 3, md5: 'abc', operator: 'a', comment: '', sourceRevision: null, createdAt: '2026-01-01T00:00:00Z' },
    { version: 2, md5: 'def', operator: 'a', comment: '', sourceRevision: null, createdAt: '2026-01-01T00:00:00Z' },
  ])
  vi.mocked(diffConfig).mockResolvedValue({ fromVersion: 2, toVersion: 3, fromContent: 'old', toContent: 'new' })
  vi.mocked(publishConfig).mockResolvedValue({ version: 4, md5: 'new' })
})

describe('ConfigsPage', () => {
  it('从配置列表渲染文件树（环境/大区/dataId）', async () => {
    renderPage(<ConfigsPage />)
    expect(await screen.findByText('app.yml')).toBeInTheDocument()
    expect(screen.getByText('prod')).toBeInTheDocument()
    expect(screen.getByText('gA')).toBeInTheDocument()
  })

  it('点击文件打开标签并展示编辑器', async () => {
    renderPage(<ConfigsPage />)
    await userEvent.click(await screen.findByText('app.yml'))
    // 编辑器替身渲染，且保存按钮出现（仅活跃标签时存在）
    expect(await screen.findByTestId('code-editor')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /保存/ })).toBeInTheDocument()
  })

  it('切到 Diff 视图后出现版本选择器', async () => {
    renderPage(<ConfigsPage />)
    await userEvent.click(await screen.findByText('app.yml'))
    await userEvent.click(await screen.findByRole('button', { name: 'Diff' }))
    // 版本选择器（旧/新版本）在有 ≥2 个版本时出现
    expect(await screen.findByText('旧版本')).toBeInTheDocument()
    expect(screen.getByText('新版本')).toBeInTheDocument()
  })

  it('点击保存调用 publishConfig', async () => {
    renderPage(<ConfigsPage />)
    await userEvent.click(await screen.findByText('app.yml'))
    await userEvent.click(await screen.findByRole('button', { name: /保存/ }))
    await waitFor(() =>
      expect(vi.mocked(publishConfig)).toHaveBeenCalledWith(1, expect.any(String), '管理台保存'),
    )
  })

  it('「复制到实例」唤起新建对话框并预填源内容与 server 覆盖层', async () => {
    renderPage(<ConfigsPage />)
    await userEvent.click(await screen.findByText('app.yml'))
    // 打开标签后出现「复制到实例」动作
    await userEvent.click(await screen.findByRole('button', { name: '复制到实例' }))
    // 对话框唤起：覆盖层预填为 server、初始内容来自源配置
    const dialog = await screen.findByRole('dialog')
    expect((within(dialog).getByLabelText('覆盖层') as HTMLSelectElement).value).toBe('server')
    expect((within(dialog).getByLabelText('初始内容') as HTMLInputElement).value).toContain('k: v')
    // server 层下出现覆盖目标下拉（实例选择）
    expect(within(dialog).getByLabelText('覆盖目标')).toBeInTheDocument()
  })
})
