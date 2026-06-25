// 系统设置块「网络代理 / 更新设置」子 tab 编辑单测（FR-101，沿用 ADR-0038/0047）：
// 锁定行为——
// ① 网络代理子 tab 渲染 update.proxy-url 一项；更新设置子 tab 渲染 channel/auto-check-enabled/check-interval-hours 三项（项归类正确，不串）；
// ② update.channel 走固定枚举下拉（stable/rc）；
// ③ 跨「网络代理」+「更新设置」两子 tab 改动后，块内统一批量保存条统观两 tab 全部脏项（保持 FR-77 跨子 tab 统一保存不回归）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Routes, Route } from 'react-router-dom'
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
    // ApiKeysPage / NamespacesPage 折叠在另两子 tab，挂载时不激活、默认卸载，但 mock 防御性置空。
    listApiKeys: vi.fn().mockResolvedValue([]),
    createApiKey: vi.fn(),
    resetApiKey: vi.fn(),
    revokeApiKey: vi.fn(),
    listNamespaces: vi.fn().mockResolvedValue([]),
    createNamespace: vi.fn(),
    updateNamespace: vi.fn(),
    deleteNamespace: vi.fn(),
  }
})

import SystemConfigBlock from './SystemConfigBlock'
import { listSettings, updateSetting } from '../../api/client'

// jsdom 垫片：radix Select 打开需要指针捕获 / scrollIntoView
if (!HTMLElement.prototype.hasPointerCapture) {
  HTMLElement.prototype.hasPointerCapture = () => false
  HTMLElement.prototype.setPointerCapture = () => {}
  HTMLElement.prototype.releasePointerCapture = () => {}
}
if (!HTMLElement.prototype.scrollIntoView) {
  HTMLElement.prototype.scrollIntoView = () => {}
}

// 含 FR-98 proxy-url + FR-101 三项更新设置，另混入一项运维域（不应出现在本块任何 tab）。
const SETTINGS: SettingView[] = [
  { key: 'update.proxy-url', value: '', valueType: 'string', default: '', desc: '更新出站代理地址', isStartup: false },
  { key: 'update.channel', value: 'stable', valueType: 'string', default: 'stable', desc: '更新渠道', isStartup: false },
  { key: 'update.auto-check-enabled', value: 'true', valueType: 'bool', default: 'true', desc: '是否启用自动检查更新', isStartup: false },
  { key: 'update.check-interval-hours', value: '6', valueType: 'int', default: '6', desc: '自动检查更新周期（小时）', isStartup: false },
  { key: 'health.ttl-sec', value: '30', valueType: 'int', default: '30', desc: '失联阈值', isStartup: false },
]

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/settings/system-config" element={<SystemConfigBlock />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listSettings).mockResolvedValue(SETTINGS)
  vi.mocked(updateSetting).mockResolvedValue(undefined)
})

describe('网络代理 / 更新设置子 tab 项归类（FR-101）', () => {
  it('网络代理子 tab 仅渲染 update.proxy-url（不含 channel / 运维域项）', async () => {
    renderAt('/settings/system-config?tab=proxy')
    expect(await screen.findByText('update.proxy-url')).toBeInTheDocument()
    expect(screen.queryByText('update.channel')).toBeNull()
    expect(screen.queryByText('health.ttl-sec')).toBeNull()
  })

  it('更新设置子 tab 渲染 channel/auto-check-enabled/check-interval-hours 三项（不含 proxy / 运维域项）', async () => {
    renderAt('/settings/system-config?tab=update')
    expect(await screen.findByText('update.channel')).toBeInTheDocument()
    expect(screen.getByText('update.auto-check-enabled')).toBeInTheDocument()
    expect(screen.getByText('update.check-interval-hours')).toBeInTheDocument()
    expect(screen.queryByText('update.proxy-url')).toBeNull()
    expect(screen.queryByText('health.ttl-sec')).toBeNull()
  })

  it('update.channel 走固定枚举下拉（stable/rc）', async () => {
    renderAt('/settings/system-config?tab=update')
    const channelKey = await screen.findByText('update.channel')
    const channelRow = channelKey.closest('[data-setting-row]') as HTMLElement
    await userEvent.click(within(channelRow).getByRole('combobox'))
    const listbox = await screen.findByRole('listbox')
    expect(within(listbox).getByRole('option', { name: 'stable' })).toBeInTheDocument()
    expect(within(listbox).getByRole('option', { name: 'rc' })).toBeInTheDocument()
  })
})

describe('跨网络代理 + 更新设置两子 tab 统一批量保存（FR-101 复用 FR-77）', () => {
  it('改 proxy + update 各一项后批量保存条统观两项脏项并各 PUT 一次', async () => {
    renderAt('/settings/system-config?tab=proxy')
    // 网络代理子 tab：改 update.proxy-url '' → http://127.0.0.1:7890
    const proxyKey = await screen.findByText('update.proxy-url')
    const proxyRow = proxyKey.closest('[data-setting-row]') as HTMLElement
    const proxyInput = within(proxyRow).getByRole('textbox')
    await userEvent.type(proxyInput, 'http://127.0.0.1:7890')

    // 切到更新设置子 tab：改 update.check-interval-hours 6 → 12
    await userEvent.click(screen.getByRole('tab', { name: '更新设置' }))
    const hoursKey = await screen.findByText('update.check-interval-hours')
    const hoursRow = hoursKey.closest('[data-setting-row]') as HTMLElement
    const hoursInput = within(hoursRow).getByRole('spinbutton')
    await userEvent.clear(hoursInput)
    await userEvent.type(hoursInput, '12')

    // 跨子 tab 统一批量保存条计脏项 = 2
    const batchBtn = await screen.findByRole('button', { name: /保存全部变更（2）/ })
    expect(batchBtn).toBeEnabled()
    await userEvent.click(batchBtn)

    await waitFor(() => {
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.proxy-url', 'http://127.0.0.1:7890')
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('update.check-interval-hours', '12')
    })
    expect(vi.mocked(updateSetting)).toHaveBeenCalledTimes(2)
  })
})
