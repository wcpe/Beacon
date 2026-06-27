// 版本与更新独立页单测（FR-100，ADR-0048）：
// 锁定行为——
// ① 版本信息渲染当前版本 + 渠道选择（stable/prerelease 下拉）；切渠道写 update.channel 并触发强制重查；
// ② 有更新时展示可用版本 / release 日志（纯文本安全渲染）+「立即更新」；点击二次确认后调 triggerUpdate；
// ③「立即检查」调 checkUpdate(force=true)；
// ④ 网络代理表单编辑 update.proxy-url 并保存（未改禁用保存）；
// ⑤ 更新设置：auto-check 开关即时保存、周期改值保存。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { SettingView, UpdateCheckView } from '@/api/types'
import PageHeader, { PageHeaderProvider } from '@/components/PageHeader'

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('@/components/useMessage', () => ({
  useMessage: () => ({ showError, showSuccess }),
}))

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ApiClientError: actual.ApiClientError,
    listSettings: vi.fn(),
    updateSetting: vi.fn(),
    checkUpdate: vi.fn(),
    triggerUpdate: vi.fn(),
    updateProgress: vi.fn().mockResolvedValue({ phase: 'idle', percent: 0, targetVersion: '', error: '' }),
    // useConnectionStatus 复用 systemStatus 心跳
    systemStatus: vi.fn().mockResolvedValue({}),
  }
})

import VersionUpdatePage from './VersionUpdatePage'
import { listSettings, updateSetting, checkUpdate, triggerUpdate, updateProgress } from '@/api/client'

// jsdom 垫片：radix Select 打开需要指针捕获 / scrollIntoView
if (!HTMLElement.prototype.hasPointerCapture) {
  HTMLElement.prototype.hasPointerCapture = () => false
  HTMLElement.prototype.setPointerCapture = () => {}
  HTMLElement.prototype.releasePointerCapture = () => {}
}
if (!HTMLElement.prototype.scrollIntoView) {
  HTMLElement.prototype.scrollIntoView = () => {}
}

const SETTINGS: SettingView[] = [
  { key: 'update.channel', value: 'stable', valueType: 'string', default: 'stable', desc: '更新渠道', isStartup: false },
  { key: 'update.proxy-url', value: '', valueType: 'string', default: '', desc: '出站代理', isStartup: false },
  { key: 'update.auto-check-enabled', value: 'true', valueType: 'bool', default: 'true', desc: '自动检查', isStartup: false },
  { key: 'update.check-interval-hours', value: '6', valueType: 'int', default: '6', desc: '检查周期', isStartup: false },
]

const UPDATE_NONE: UpdateCheckView = {
  status: 'ok',
  currentVersion: 'v0.10.0',
  channel: 'stable',
  hasUpdate: false,
  isDevBuild: false,
  latestVersion: '',
  releaseNotes: '',
  releaseUrl: '',
  publishedAt: '',
  checkedAt: '',
  cacheExpiresAt: '',
}

const UPDATE_HAS: UpdateCheckView = {
  ...UPDATE_NONE,
  hasUpdate: true,
  latestVersion: 'v0.11.0',
  releaseNotes: '修复若干问题',
  releaseUrl: 'https://github.com/wcpe/Beacon/releases/tag/v0.11.0',
  publishedAt: '2026-06-25T08:00:00Z',
}

// 标题与副标题已迁入第二层页眉 PageHeader（FR-105），故连同 PageHeader 在 /system/version 路由下渲染。
function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/system/version']}>
        <PageHeaderProvider>
          <PageHeader />
          <Routes>
            <Route path="/system/version" element={ui} />
          </Routes>
        </PageHeaderProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listSettings).mockResolvedValue(SETTINGS)
  vi.mocked(updateSetting).mockResolvedValue(undefined)
  vi.mocked(checkUpdate).mockResolvedValue(UPDATE_NONE)
  vi.mocked(triggerUpdate).mockResolvedValue({ accepted: true })
})

describe('VersionUpdatePage 版本信息 + 渠道（FR-100）', () => {
  it('渲染页标题 / 当前版本 / 渠道下拉', async () => {
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByRole('heading', { name: '版本与更新' })).toBeInTheDocument()
    expect(await screen.findByText('v0.10.0')).toBeInTheDocument()
    // 渠道下拉可见
    expect(await screen.findByLabelText('更新渠道')).toBeInTheDocument()
  })

  it('切换渠道写 update.channel 并触发强制重查', async () => {
    renderPage(<VersionUpdatePage />)
    const channelSelect = await screen.findByLabelText('更新渠道')
    await userEvent.click(channelSelect)
    const listbox = await screen.findByRole('listbox')
    await userEvent.click(within(listbox).getByRole('option', { name: 'prerelease' }))
    await waitFor(() => expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.channel', 'prerelease'))
    // 切渠道后强制重查（force=true）
    await waitFor(() => expect(vi.mocked(checkUpdate)).toHaveBeenCalledWith(true))
  })

  it('「立即检查」调 checkUpdate(force=true)', async () => {
    renderPage(<VersionUpdatePage />)
    const btn = await screen.findByRole('button', { name: '立即检查' })
    await userEvent.click(btn)
    await waitFor(() => expect(vi.mocked(checkUpdate)).toHaveBeenCalledWith(true))
  })

  // FR-118 ①：「立即检查」在页面正文（紧挨版本/更新区），不在第二层页眉操作槽
  it('「立即检查」按钮位于页面正文版本分区内（FR-118 ①）', async () => {
    renderPage(<VersionUpdatePage />)
    const btn = await screen.findByRole('button', { name: '立即检查' })
    // 页眉 header 带为 <h1>「版本与更新」所在容器；按钮应落在版本信息 <section> 而非页眉
    const section = btn.closest('section')
    expect(section).not.toBeNull()
    expect(within(section as HTMLElement).getByText('当前版本')).toBeInTheDocument()
  })
})

// FR-118 ③：切渠道后回显重检结果（发现更新 / 已最新 / 检查失败）
describe('VersionUpdatePage 切渠道重检结果（FR-118 ③）', () => {
  async function switchChannel() {
    const channelSelect = await screen.findByLabelText('更新渠道')
    await userEvent.click(channelSelect)
    const listbox = await screen.findByRole('listbox')
    await userEvent.click(within(listbox).getByRole('option', { name: 'prerelease' }))
  }

  it('重检发现更新 → 提示新版本', async () => {
    // 重检（force=true）返回有更新
    vi.mocked(checkUpdate).mockResolvedValue(UPDATE_HAS)
    renderPage(<VersionUpdatePage />)
    await switchChannel()
    await waitFor(() =>
      expect(showSuccess).toHaveBeenCalledWith(expect.stringContaining('发现新版本 v0.11.0')),
    )
  })

  it('重检未发现更新 → 提示已最新', async () => {
    // 默认 UPDATE_NONE（无更新）
    renderPage(<VersionUpdatePage />)
    await switchChannel()
    await waitFor(() =>
      expect(showSuccess).toHaveBeenCalledWith(expect.stringContaining('当前已是最新版本')),
    )
  })

  it('重检失败（check-failed）→ 提示检查失败', async () => {
    vi.mocked(checkUpdate).mockResolvedValue({ ...UPDATE_NONE, status: 'check-failed' })
    renderPage(<VersionUpdatePage />)
    await switchChannel()
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(expect.stringContaining('检查更新失败')),
    )
  })
})

describe('VersionUpdatePage 有更新（FR-100）', () => {
  beforeEach(() => {
    vi.mocked(checkUpdate).mockResolvedValue(UPDATE_HAS)
  })

  it('展示可用版本 + release 日志（纯文本）+「立即更新」', async () => {
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByText('v0.11.0')).toBeInTheDocument()
    expect(screen.getByText('修复若干问题')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '立即更新' })).toBeInTheDocument()
  })

  it('点「立即更新」二次确认后调 triggerUpdate', async () => {
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('button', { name: '立即更新' }))
    // 二次确认对话框确认
    const confirm = await screen.findByRole('button', { name: '确认更新' })
    await userEvent.click(confirm)
    await waitFor(() => expect(vi.mocked(triggerUpdate)).toHaveBeenCalled())
  })

  // FR-118 ②：更新走完给明确失败裁决（成功重连裁决依赖 offline→online 真链路，真机验）
  it('更新进度 phase=failed 时给明确失败裁决 toast', async () => {
    vi.mocked(updateProgress).mockResolvedValue({
      phase: 'failed',
      percent: 0,
      targetVersion: 'v0.11.0',
      error: '下载校验失败',
    })
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('button', { name: '立即更新' }))
    await userEvent.click(await screen.findByRole('button', { name: '确认更新' }))
    // 进度区先反映失败阶段（确认进度查询已启用并取到 failed）
    expect(await screen.findByText(/更新失败/)).toBeInTheDocument()
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(expect.stringContaining('更新失败：下载校验失败')),
    )
  })
})

describe('VersionUpdatePage 网络代理（FR-98）', () => {
  it('编辑 proxy-url 并保存以 updateSetting 调用；未改时保存禁用', async () => {
    renderPage(<VersionUpdatePage />)
    const input = await screen.findByLabelText('出站代理地址')
    // 网络代理分区的保存按钮（FR-108 卡片降级为 AnchorSectionBlock <section>，在该分区内定位）
    const proxyCard = input.closest('section') as HTMLElement
    const saveBtn = within(proxyCard).getByRole('button', { name: '保存' })
    expect(saveBtn).toBeDisabled()
    await userEvent.type(input, 'http://127.0.0.1:7890')
    expect(saveBtn).toBeEnabled()
    await userEvent.click(saveBtn)
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.proxy-url', 'http://127.0.0.1:7890'),
    )
  })
})

describe('VersionUpdatePage 更新设置（FR-101）', () => {
  it('切自动检查开关即时保存 update.auto-check-enabled', async () => {
    renderPage(<VersionUpdatePage />)
    const checkbox = await screen.findByRole('checkbox', { name: /自动检查更新/ })
    await userEvent.click(checkbox)
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.auto-check-enabled', 'false'),
    )
  })

  it('改检查周期并保存 update.check-interval-hours', async () => {
    renderPage(<VersionUpdatePage />)
    const input = await screen.findByLabelText('自动检查周期（小时）')
    await userEvent.clear(input)
    await userEvent.type(input, '12')
    const prefsCard = input.closest('section') as HTMLElement
    await userEvent.click(within(prefsCard).getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.check-interval-hours', '12'),
    )
  })
})
