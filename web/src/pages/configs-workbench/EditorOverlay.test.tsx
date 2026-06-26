// EditorOverlay 单测（FR-115）：悬浮覆盖编辑器（多标签 / 历史 / 保存确认）。
// mock useWorkbenchFile 注入文件 + CodeEditor 替身（暴露 textarea 驱动 onChange）。
// 覆盖：loading 骨架、面包屑、多标签渲染与切换（onActivate）、关闭标签（onCloteTab）、关闭浮层（onClose）、
// 历史修订列表（含「当前」徽标）、保存（Ctrl+S / 工具栏保存钮触发 toast）、最大化同步 URL（onSyncUrl）、
// 脏标记（编辑后出未保存点，保存后清）、tabLabel 辅助。
import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

vi.mock('@/components/CodeEditor', () => ({
  default: (props: { value?: string; onChange?: (v: string) => void }) => (
    <textarea data-testid="code-editor" value={props.value ?? ''} onChange={(e) => props.onChange?.(e.target.value)} />
  ),
}))

const toastSuccess = vi.fn()
vi.mock('@/components/useMessage', () => ({
  useMessage: () => ({ showSuccess: toastSuccess, showError: vi.fn() }),
}))

vi.mock('./useWorkbenchData', () => ({
  useWorkbenchFile: vi.fn(),
}))

import EditorOverlay, { tabLabel } from './EditorOverlay'
import { useWorkbenchFile } from './useWorkbenchData'
import type { WorkbenchFile } from './types'

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

const TABS = [
  { key: 'plugins/Essentials/config.yml', name: 'config.yml' },
  { key: 'plugins/spawn.yml', name: 'spawn.yml' },
]

function mockFileHook(over: Partial<ReturnType<typeof useWorkbenchFile>> = {}) {
  mockedFile.mockReturnValue({ data: FILE, isLoading: false, refetch: vi.fn(), ...over } as ReturnType<typeof useWorkbenchFile>)
}

function renderOverlay(over: Partial<Parameters<typeof EditorOverlay>[0]> = {}) {
  const props = {
    tabs: TABS,
    activeKey: TABS[0].key,
    onActivate: vi.fn(),
    onClose: vi.fn(),
    onCloseTab: vi.fn(),
    onSyncUrl: vi.fn(),
    ...over,
  } as Parameters<typeof EditorOverlay>[0]
  render(<EditorOverlay {...props} />)
  return props
}

describe('EditorOverlay（FR-115）', () => {
  beforeEach(() => {
    toastSuccess.mockClear()
    mockFileHook()
  })

  it('loading：显骨架，不渲染编辑器', () => {
    mockFileHook({ data: undefined, isLoading: true })
    renderOverlay()
    expect(screen.queryByTestId('code-editor')).not.toBeInTheDocument()
  })

  it('面包屑显环境·组 + 文件名', () => {
    renderOverlay()
    expect(screen.getByText('prod · main')).toBeInTheDocument()
  })

  it('多标签渲染：两个标签都在；点非活跃标签触发 onActivate', async () => {
    const props = renderOverlay()
    expect(screen.getByText('spawn.yml')).toBeInTheDocument()
    await userEvent.click(screen.getByText('spawn.yml'))
    expect(props.onActivate).toHaveBeenCalledWith('plugins/spawn.yml')
  })

  it('历史修订面板：列出版本，最新版带「当前」徽标', () => {
    renderOverlay()
    expect(screen.getByText('历史修订')).toBeInTheDocument()
    expect(screen.getByText('v7')).toBeInTheDocument()
    expect(screen.getByText('v6')).toBeInTheDocument()
    expect(screen.getByText('当前')).toBeInTheDocument()
  })

  it('底部状态栏显历史版本份数', () => {
    renderOverlay()
    expect(screen.getByText('历史版本 2 份')).toBeInTheDocument()
  })

  it('保存：工具栏「保存」钮触发 toast（保存确认反馈）', async () => {
    renderOverlay()
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(toastSuccess).toHaveBeenCalledWith('已保存（原型示意）')
  })

  it('Ctrl+S 保存触发 toast', () => {
    renderOverlay()
    fireEvent.keyDown(window, { key: 's', ctrlKey: true })
    expect(toastSuccess).toHaveBeenCalledWith('已保存（原型示意）')
  })

  it('脏标记：编辑后出未保存点；保存后清除', async () => {
    renderOverlay()
    // 改内容 → 脏
    fireEvent.change(screen.getByTestId('code-editor'), { target: { value: 'a: 2\n' } })
    expect(screen.getByText('●未保存')).toBeInTheDocument()
    // 保存清脏
    await userEvent.click(screen.getByRole('button', { name: '保存' }))
    expect(screen.queryByText('●未保存')).not.toBeInTheDocument()
  })

  it('关闭标签触发 onCloseTab', async () => {
    const props = renderOverlay()
    // 标签上的关闭 X：spawn.yml 标签内 X（取标签按钮内的关闭区域）
    const spawnTab = screen.getByText('spawn.yml').closest('button')!
    const closeIcon = spawnTab.querySelector('span.cursor-pointer')!
    await userEvent.click(closeIcon)
    expect(props.onCloseTab).toHaveBeenCalledWith('plugins/spawn.yml')
  })

  it('关闭浮层：面包屑返回钮触发 onClose', async () => {
    const props = renderOverlay()
    await userEvent.click(screen.getByRole('button', { name: '关闭' }))
    expect(props.onClose).toHaveBeenCalled()
  })

  it('最大化态同步 URL（onSyncUrl 传 activeKey）；常态传 null', async () => {
    const props = renderOverlay()
    // 初次挂载常态 → onSyncUrl(null)
    expect(props.onSyncUrl).toHaveBeenCalledWith(null)
    await userEvent.click(screen.getByRole('button', { name: '最大化' }))
    expect(props.onSyncUrl).toHaveBeenCalledWith(TABS[0].key)
  })

  it('tabLabel 取 dataId 末段', () => {
    expect(tabLabel(FILE)).toBe('config.yml')
  })
})
