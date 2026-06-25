// UpdateModal 单测（FR-100）：
// ① 有更新时渲染当前 / 可用版本 / 渠道 / release 日志 / 外链；
// ② releaseNotes 安全渲染——含 HTML 片段时作纯文本转义、不注入 DOM 节点；
// ③ 「立即检查」调 onRefresh（force）；
// ④ 「立即更新」二次确认后 POST triggerUpdate 并开始轮询进度（展示阶段文案）；
// ⑤ check-failed / dev 构建不出现「立即更新」按钮。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { UpdateCheckView, UpdateProgressView } from '@/api/types'

vi.mock('@/api/client', async () => {
  const actual = await vi.importActual<typeof import('@/api/client')>('@/api/client')
  return {
    ApiClientError: actual.ApiClientError,
    triggerUpdate: vi.fn(),
    updateProgress: vi.fn(),
    // useConnectionStatus 复用 system-status 查询，mock 防御性置空
    systemStatus: vi.fn().mockResolvedValue({}),
  }
})

vi.mock('@/components/useMessage', () => ({
  useMessage: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

import UpdateModal from './UpdateModal'
import { triggerUpdate, updateProgress } from '@/api/client'

const CHECK_OK: UpdateCheckView = {
  status: 'ok',
  currentVersion: 'v0.10.0',
  channel: 'stable',
  hasUpdate: true,
  isDevBuild: false,
  latestVersion: 'v0.11.0',
  releaseNotes: '变更点一\n变更点二',
  releaseUrl: 'https://github.com/wcpe/Beacon/releases/tag/v0.11.0',
  publishedAt: '2026-06-20T08:00:00Z',
  checkedAt: '2026-06-25T10:00:00Z',
  cacheExpiresAt: '2026-06-25T16:00:00Z',
}

const PROGRESS_IDLE: UpdateProgressView = { phase: 'idle', percent: 0, targetVersion: '', error: '' }

function renderModal(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(updateProgress).mockResolvedValue(PROGRESS_IDLE)
  vi.mocked(triggerUpdate).mockResolvedValue({ accepted: true })
})

describe('UpdateModal 字段渲染', () => {
  it('有更新时渲染当前/可用版本、渠道、release 日志与外链', () => {
    renderModal(
      <UpdateModal open data={CHECK_OK} isLoading={false} isError={false} onRefresh={vi.fn()} />,
    )
    // 状态行
    expect(screen.getByText('发现新版本 v0.11.0，可在线更新')).toBeInTheDocument()
    // 版本字段
    expect(screen.getByText('v0.10.0')).toBeInTheDocument()
    expect(screen.getByText('v0.11.0')).toBeInTheDocument()
    expect(screen.getByText('stable')).toBeInTheDocument()
    // release 日志正文
    expect(screen.getByText(/变更点一/)).toBeInTheDocument()
    // 外链指向 releaseUrl
    const link = screen.getByRole('link', { name: /查看 release 页/ })
    expect(link).toHaveAttribute('href', CHECK_OK.releaseUrl)
  })

  it('releaseNotes 含 HTML 片段时作纯文本渲染、不注入 DOM 节点（防 XSS）', () => {
    const malicious = { ...CHECK_OK, releaseNotes: '<img src=x onerror="alert(1)"><b>x</b>' }
    const { container } = renderModal(
      <UpdateModal open data={malicious} isLoading={false} isError={false} onRefresh={vi.fn()} />,
    )
    // 原始串作为可见文本出现（被转义），且不产生真实 img / b 元素
    expect(screen.getByText(/<img src=x onerror=/)).toBeInTheDocument()
    expect(container.querySelector('img')).toBeNull()
    expect(container.querySelector('b')).toBeNull()
  })
})

describe('UpdateModal 操作', () => {
  it('「立即检查」调 onRefresh', async () => {
    const onRefresh = vi.fn().mockResolvedValue(undefined)
    renderModal(<UpdateModal open data={CHECK_OK} isLoading={false} isError={false} onRefresh={onRefresh} />)
    await userEvent.click(screen.getByRole('button', { name: /立即检查/ }))
    await waitFor(() => expect(onRefresh).toHaveBeenCalledTimes(1))
  })

  it('「立即更新」二次确认后 POST triggerUpdate 并轮询进度', async () => {
    vi.mocked(updateProgress).mockResolvedValue({ phase: 'downloading', percent: 42, targetVersion: 'v0.11.0', error: '' })
    renderModal(<UpdateModal open data={CHECK_OK} isLoading={false} isError={false} onRefresh={vi.fn()} />)
    // 点「立即更新」弹二次确认
    await userEvent.click(screen.getByRole('button', { name: /立即更新/ }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText(/确认在线更新到 v0.11.0/)).toBeInTheDocument()
    // 确认 → POST
    await userEvent.click(within(dialog).getByRole('button', { name: '确认更新' }))
    await waitFor(() => expect(vi.mocked(triggerUpdate)).toHaveBeenCalledTimes(1))
    // 进度轮询展示阶段文案
    expect(await screen.findByText(/下载中…（42%）/)).toBeInTheDocument()
  })
})

describe('UpdateModal 不可更新分支', () => {
  it('check-failed 时不出现「立即更新」按钮且提示检查失败', () => {
    const failed: UpdateCheckView = { ...CHECK_OK, status: 'check-failed', hasUpdate: false, latestVersion: '' }
    renderModal(<UpdateModal open data={failed} isLoading={false} isError={false} onRefresh={vi.fn()} />)
    expect(screen.getByText('检查更新失败（GitHub 不可达或限流），稍后重试')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /立即更新/ })).toBeNull()
  })

  it('dev 构建时不提示更新、无「立即更新」按钮', () => {
    const dev: UpdateCheckView = { ...CHECK_OK, isDevBuild: true, currentVersion: 'dev', hasUpdate: false }
    renderModal(<UpdateModal open data={dev} isLoading={false} isError={false} onRefresh={vi.fn()} />)
    expect(screen.getByText('开发构建（dev），不检查更新')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /立即更新/ })).toBeNull()
  })
})
