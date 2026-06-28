// 版本与更新独立页单测（FR-100，ADR-0048）：
// 锁定行为——
// ① 单卡片渲染当前版本 + 渠道分段控件（正式版/测试版）；切渠道写 update.channel 并触发强制重查；
// ② 有更新时展示可用版本 / release 日志（markdown 安全渲染）+「立即更新并重启」；点击二次确认后调 triggerUpdate；
// ③「立即检查」调 checkUpdate(force=true)；
// ④ 高级设置折叠区：网络代理表单编辑 update.proxy-url 并保存（未改禁用保存）；
// ⑤ 高级设置折叠区：更新设置 auto-check 开关即时保存、周期改值保存。
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
    rollbackUpdate: vi.fn(),
    updateProgress: vi
      .fn()
      .mockResolvedValue({ phase: 'idle', percent: 0, targetVersion: '', error: '', rollbackAvailable: false }),
    // useConnectionStatus 复用 systemStatus 心跳
    systemStatus: vi.fn().mockResolvedValue({}),
  }
})

import VersionUpdatePage from './VersionUpdatePage'
import {
  ApiClientError,
  listSettings,
  updateSetting,
  checkUpdate,
  triggerUpdate,
  rollbackUpdate,
  updateProgress,
} from '@/api/client'

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
  releaseNotes: '## 修复\n- 修复若干问题\n- 优化 **稳定性**',
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
  vi.mocked(rollbackUpdate).mockResolvedValue({ accepted: true })
  // 默认进度态：无可回退版本（回滚按钮隐藏），各用例按需覆盖
  vi.mocked(updateProgress).mockResolvedValue({
    phase: 'idle',
    percent: 0,
    targetVersion: '',
    error: '',
    rollbackAvailable: false,
  })
})

describe('VersionUpdatePage 版本信息 + 渠道（FR-100）', () => {
  it('渲染页标题 / 应用更新卡片 / 当前版本 / 渠道分段控件', async () => {
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByRole('heading', { name: '版本与更新' })).toBeInTheDocument()
    // 单卡片标题
    expect(await screen.findByText('应用更新')).toBeInTheDocument()
    expect(await screen.findByText('v0.10.0')).toBeInTheDocument()
    // 渠道分段控件：正式版 / 测试版 两个 tab 段
    expect(await screen.findByRole('tab', { name: '正式版' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '测试版' })).toBeInTheDocument()
  })

  it('切换渠道（点测试版段）写 update.channel 并触发强制重查', async () => {
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('tab', { name: '测试版' }))
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
})

// 状态徽标：有更新 / 已最新；预发布渠道额外挂「预发布」徽标
describe('VersionUpdatePage 状态徽标', () => {
  it('已最新时显示「已是最新」徽标', async () => {
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByText('已是最新')).toBeInTheDocument()
  })

  it('有更新时显示「有可用更新」徽标', async () => {
    vi.mocked(checkUpdate).mockResolvedValue(UPDATE_HAS)
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByText('有可用更新')).toBeInTheDocument()
  })

  it('预发布渠道显示「预发布」徽标', async () => {
    vi.mocked(listSettings).mockResolvedValue([
      { key: 'update.channel', value: 'prerelease', valueType: 'string', default: 'stable', desc: '更新渠道', isStartup: false },
      ...SETTINGS.slice(1),
    ])
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByText('预发布')).toBeInTheDocument()
  })
})

// FR-118 ③：切渠道后回显重检结果（发现更新 / 已最新 / 检查失败）
describe('VersionUpdatePage 切渠道重检结果（FR-118 ③）', () => {
  async function switchChannel() {
    await userEvent.click(await screen.findByRole('tab', { name: '测试版' }))
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

  it('展示可用版本 + release 日志（markdown 渲染）+「立即更新并重启」', async () => {
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByText('v0.11.0')).toBeInTheDocument()
    // markdown：## 标题渲染为标题、- 列表项渲染、**加粗**渲染为 <strong>
    expect(screen.getByRole('heading', { name: '修复' })).toBeInTheDocument()
    expect(screen.getByText('修复若干问题')).toBeInTheDocument()
    const strong = screen.getByText('稳定性')
    expect(strong.tagName).toBe('STRONG')
    expect(screen.getByRole('button', { name: '立即更新并重启' })).toBeInTheDocument()
  })

  it('点「立即更新并重启」二次确认后调 triggerUpdate', async () => {
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('button', { name: '立即更新并重启' }))
    // 二次确认对话框确认
    const confirm = await screen.findByRole('button', { name: '确认更新' })
    await userEvent.click(confirm)
    await waitFor(() => expect(vi.mocked(triggerUpdate)).toHaveBeenCalled())
  })

  // fix-1：触发被拒（如 409 已有更新进行中）→ 错误经 toast 展示，不静默；按钮不被锁死。
  it('触发更新返回 409 进行中 → toast 出错误（不静默）', async () => {
    vi.mocked(triggerUpdate).mockRejectedValue(new ApiClientError('已有更新正在进行中', 'UPDATE_IN_PROGRESS'))
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('button', { name: '立即更新并重启' }))
    await userEvent.click(await screen.findByRole('button', { name: '确认更新' }))
    await waitFor(() => expect(showError).toHaveBeenCalledWith('已有更新正在进行中'))
  })

  // FR-118 ②：更新走完给明确失败裁决（成功重连裁决依赖 offline→online 真链路，真机验）
  it('更新进度 phase=failed 时给明确失败裁决 toast', async () => {
    vi.mocked(updateProgress).mockResolvedValue({
      phase: 'failed',
      percent: 0,
      targetVersion: 'v0.11.0',
      error: '下载校验失败',
      rollbackAvailable: false,
    })
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('button', { name: '立即更新并重启' }))
    await userEvent.click(await screen.findByRole('button', { name: '确认更新' }))
    // 进度区先反映失败阶段（确认进度查询已启用并取到 failed）
    expect(await screen.findByText(/更新失败/)).toBeInTheDocument()
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(expect.stringContaining('更新失败：下载校验失败')),
    )
  })
})

// 高级设置折叠区默认折叠，编辑前须先展开。
async function expandAdvanced() {
  const toggle = await screen.findByRole('button', { name: '高级设置' })
  await userEvent.click(toggle)
}

describe('VersionUpdatePage 高级设置-网络代理（FR-98）', () => {
  it('默认折叠：未展开时网络代理表单不可见', async () => {
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByRole('button', { name: '高级设置' })).toBeInTheDocument()
    expect(screen.queryByLabelText('出站代理地址')).toBeNull()
  })

  it('展开后编辑 proxy-url 并保存以 updateSetting 调用；未改时保存禁用', async () => {
    renderPage(<VersionUpdatePage />)
    await expandAdvanced()
    const input = await screen.findByLabelText('出站代理地址')
    // 网络代理子块的保存按钮（在该块容器内定位，避开更新设置块的同名按钮）
    const proxyBlock = input.closest('div.space-y-3') as HTMLElement
    const saveBtn = within(proxyBlock).getByRole('button', { name: '保存' })
    expect(saveBtn).toBeDisabled()
    await userEvent.type(input, 'http://127.0.0.1:7890')
    expect(saveBtn).toBeEnabled()
    await userEvent.click(saveBtn)
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.proxy-url', 'http://127.0.0.1:7890'),
    )
  })
})

describe('VersionUpdatePage 高级设置-更新设置（FR-101）', () => {
  it('展开后切自动检查开关即时保存 update.auto-check-enabled', async () => {
    renderPage(<VersionUpdatePage />)
    await expandAdvanced()
    const checkbox = await screen.findByRole('checkbox', { name: /自动检查更新/ })
    await userEvent.click(checkbox)
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.auto-check-enabled', 'false'),
    )
  })

  it('展开后改检查周期并保存 update.check-interval-hours', async () => {
    renderPage(<VersionUpdatePage />)
    await expandAdvanced()
    const input = await screen.findByLabelText('自动检查周期（小时）')
    await userEvent.clear(input)
    await userEvent.type(input, '12')
    // 周期输入与其保存按钮在同一 flex 行容器内，定位避开网络代理块的同名按钮
    const intervalRow = input.closest('div.flex') as HTMLElement
    await userEvent.click(within(intervalRow).getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.check-interval-hours', '12'),
    )
  })
})

// FR-120：手动回滚——按 rollbackAvailable 显隐回滚按钮、二次确认、触发调 rollbackUpdate、409 回显
describe('VersionUpdatePage 手动回滚（FR-120）', () => {
  // 状态端点回 rollbackAvailable=true（有可回退的上一版本）
  function withRollbackAvailable() {
    vi.mocked(updateProgress).mockResolvedValue({
      phase: 'idle',
      percent: 0,
      targetVersion: '',
      error: '',
      rollbackAvailable: true,
    })
  }

  it('rollbackAvailable=false 时不显示「回滚到上一版本」按钮', async () => {
    // 默认 beforeEach 即 rollbackAvailable=false
    renderPage(<VersionUpdatePage />)
    // 等版本信息渲染完成（当前版本可见）后再断言回滚按钮缺席，避免过早断言
    expect(await screen.findByText('v0.10.0')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '回滚到上一版本' })).toBeNull()
  })

  it('rollbackAvailable=true 时显示「回滚到上一版本」按钮', async () => {
    withRollbackAvailable()
    renderPage(<VersionUpdatePage />)
    expect(await screen.findByRole('button', { name: '回滚到上一版本' })).toBeInTheDocument()
  })

  it('点「回滚到上一版本」二次确认后调 rollbackUpdate', async () => {
    withRollbackAvailable()
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('button', { name: '回滚到上一版本' }))
    // 二次确认对话框确认
    const confirm = await screen.findByRole('button', { name: '确认回滚' })
    await userEvent.click(confirm)
    await waitFor(() => expect(vi.mocked(rollbackUpdate)).toHaveBeenCalled())
  })

  it('回滚触发返回 409 NO_ROLLBACK_AVAILABLE 时回显「无可回退的上一版本」', async () => {
    withRollbackAvailable()
    vi.mocked(rollbackUpdate).mockRejectedValue(
      new ApiClientError('无可回退的上一版本', 'NO_ROLLBACK_AVAILABLE'),
    )
    renderPage(<VersionUpdatePage />)
    await userEvent.click(await screen.findByRole('button', { name: '回滚到上一版本' }))
    await userEvent.click(await screen.findByRole('button', { name: '确认回滚' }))
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(expect.stringContaining('无可回退的上一版本')),
    )
  })
})
