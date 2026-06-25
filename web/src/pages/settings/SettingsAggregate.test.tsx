// 设置聚合页骨架单测（FR-94，见 ADR-0043）：
// 锁定行为——
// ① 三块顶层 tab 渲染；/settings 重定向到 /settings/ops；
// ② 顶层块用嵌套子路由切换、深链可达系统信息 / 系统设置块（含空壳占位文案）；
// ③ 运维设置块子 tab 选择落 search param，深链 ?tab=log 直达日志 tab；
// ④【FR-94 硬约束】跨子 tab 改 health + log 各一项时统一批量保存仍统观全部脏项（保存全部变更（2））。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route, Navigate } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { SettingView } from '../../api/types'

vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showError: vi.fn(), showSuccess: vi.fn() }),
}))

vi.mock('../../api/client', async () => {
  const actual = await vi.importActual<typeof import('../../api/client')>('../../api/client')
  return {
    ApiClientError: actual.ApiClientError,
    listSettings: vi.fn(),
    updateSetting: vi.fn(),
    // FR-100：VersionInfoTab 内嵌 UpdateModal，其 useConnectionStatus 复用 systemStatus 心跳
    systemStatus: vi.fn().mockResolvedValue({}),
    // FR-100：系统信息块「版本与更新」子 tab（VersionInfoTab）经 useUpdateCheck 用到
    checkUpdate: vi.fn().mockResolvedValue({
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
    }),
    updateProgress: vi.fn().mockResolvedValue({ phase: 'idle', percent: 0, targetVersion: '', error: '' }),
    triggerUpdate: vi.fn(),
  }
})

import SettingsPage from '../SettingsPage'
import OpsSettingsBlock from './OpsSettingsBlock'
import SystemInfoBlock from './SystemInfoBlock'
import SystemConfigBlock from './SystemConfigBlock'
import { listSettings, updateSetting, checkUpdate } from '../../api/client'

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
  { key: 'health.ttl-sec', value: '30', valueType: 'int', default: '30', desc: '失联阈值', isStartup: false },
  { key: 'log.level', value: 'INFO', valueType: 'string', default: 'INFO', desc: '日志级别', isStartup: false },
]

// 完整嵌套路由装配（镜像 App.tsx 的 /settings 子树）。
function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/settings" element={<SettingsPage />}>
            <Route index element={<Navigate to="/settings/ops" replace />} />
            <Route path="ops" element={<OpsSettingsBlock />} />
            <Route path="system-info" element={<SystemInfoBlock />} />
            <Route path="system-config" element={<SystemConfigBlock />} />
          </Route>
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listSettings).mockResolvedValue(SETTINGS)
  vi.mocked(updateSetting).mockResolvedValue(undefined)
  // FR-100：clearAllMocks 清掉了 checkUpdate 的默认返回，重置为「无可用更新」
  vi.mocked(checkUpdate).mockResolvedValue({
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
  })
})

describe('设置聚合页三块顶层 tab（FR-94）', () => {
  it('渲染三块顶层 tab', async () => {
    renderAt('/settings/ops')
    expect(await screen.findByRole('tab', { name: '运维设置' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '系统信息' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '系统设置' })).toBeInTheDocument()
  })

  it('/settings 重定向到运维设置块 /settings/ops（默认见运维设置内容）', async () => {
    renderAt('/settings')
    // 运维设置块的 config.yml 提示文案可见，证明已落到 ops 块
    expect(await screen.findByText(/config\.yml/)).toBeInTheDocument()
  })

  it('深链 /settings/system-info 直达系统信息块（版本与更新内容可见，FR-100）', async () => {
    renderAt('/settings/system-info')
    // 版本与更新子 tab 接入 FR-99 检查：展示当前版本与更新状态
    expect(await screen.findByText('v0.10.0')).toBeInTheDocument()
    expect(await screen.findByText('已是最新版本')).toBeInTheDocument()
  })

  it('深链 /settings/system-config 直达系统设置块（网络代理子 tab 兜底占位可见，mock 无 update.* 项）', async () => {
    renderAt('/settings/system-config')
    expect(await screen.findByText(/暂无网络代理设置项/)).toBeInTheDocument()
  })
})

describe('运维设置块子 tab search param 深链（FR-94）', () => {
  it('深链 /settings/ops?tab=log 直达日志子 tab（log.level 可见）', async () => {
    renderAt('/settings/ops?tab=log')
    expect(await screen.findByText('log.level')).toBeInTheDocument()
  })
})

describe('运维设置块跨子 tab 统一批量保存（FR-94 硬约束）', () => {
  it('改 health + log 各一项后批量保存条统观两项脏项（保存全部变更（2））', async () => {
    renderAt('/settings/ops')
    // 默认健康检查 tab：改 health.ttl-sec 30→45
    const healthKey = await screen.findByText('health.ttl-sec')
    const healthRow = healthKey.closest('[data-setting-row]') as HTMLElement
    const healthInput = within(healthRow).getByRole('spinbutton')
    await userEvent.clear(healthInput)
    await userEvent.type(healthInput, '45')

    // 切到日志子 tab：改 log.level INFO→DEBUG
    await userEvent.click(screen.getByRole('tab', { name: '日志' }))
    const logKey = await screen.findByText('log.level')
    const logRow = logKey.closest('[data-setting-row]') as HTMLElement
    await userEvent.click(within(logRow).getByRole('combobox'))
    const listbox = await screen.findByRole('listbox')
    await userEvent.click(within(listbox).getByRole('option', { name: 'DEBUG' }))

    // 跨子 tab 统一批量保存条计脏项 = 2
    const batchBtn = await screen.findByRole('button', { name: /保存全部变更（2）/ })
    expect(batchBtn).toBeEnabled()
    await userEvent.click(batchBtn)

    await waitFor(() => {
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('health.ttl-sec', '45')
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('log.level', 'DEBUG')
    })
    expect(vi.mocked(updateSetting)).toHaveBeenCalledTimes(2)
  })
})
