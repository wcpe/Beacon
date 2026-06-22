// ZonesPage 指派表单单测（增强 FR-40）：锁定三项行为——
// ① 环境 / serverId / 大区 / 小区下拉来自 API（serverId 仅列 bukkit、排除 BC 代理）；
// ② 缺选必填项被拦下、不调 assignZone；③ 合法选齐后提交携带所选值。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView } from '../api/types'

// mock 写操作反馈：spy showError，断言校验拦截
const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('../components/useMessage', () => ({
  useMessage: () => ({ showError, showSuccess }),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  assignZone: vi.fn(),
  unassignZone: vi.fn(),
  listAssignments: vi.fn(),
  listInstances: vi.fn(),
  listNamespaces: vi.fn(),
  zoneSummary: vi.fn(),
}))

import ZonesPage from './ZonesPage'
import {
  assignZone,
  listAssignments,
  listInstances,
  listNamespaces,
  zoneSummary,
} from '../api/client'

// 实例样例：1 个 bukkit 子服 + 1 个 BC 代理（bungee）
function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'srv',
    role: 'bukkit',
    group: 'gA',
    zone: 'z1',
    assigned: true,
    address: '',
    version: '',
    status: 'online',
    capacity: 0,
    weight: 0,
    metadata: {},
    lastHeartbeat: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
    backends: [],
    zoneDefaultEntry: false,
    proxy: {
      onlineConnections: 0,
      threadCount: 0,
      uptimeMs: 0,
      backendUp: 0,
      backendTotal: 0,
      backendAvgLatencyMs: -1,
    },
    registeredAt: '',
    ...overrides,
  }
}

const INSTANCES: InstanceView[] = [
  inst({ serverId: 'lobby-1', role: 'bukkit', group: 'gA', zone: 'z1' }),
  inst({ serverId: 'bc-1', role: 'bungee', group: 'gB', zone: null, assigned: false }),
]

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  showError.mockClear()
  showSuccess.mockClear()
  vi.mocked(listInstances).mockResolvedValue(INSTANCES)
  vi.mocked(listNamespaces).mockResolvedValue([
    { code: 'prod', name: '生产' },
    { code: 'test', name: '测试' },
  ])
  vi.mocked(zoneSummary).mockResolvedValue([
    { group: 'gA', zone: 'z1', serverCount: 1, onlineCount: 1 },
    { group: 'gA', zone: 'z2', serverCount: 0, onlineCount: 0 },
  ])
  vi.mocked(listAssignments).mockResolvedValue([])
  vi.mocked(assignZone).mockResolvedValue({
    namespace: 'prod',
    serverId: 'lobby-1',
    group: 'gA',
    zone: 'z1',
    note: '',
    updatedAt: '2026-01-01T00:00:00Z',
  })
})

// 打开「新增 zone / 指派」对话框，返回对话框容器（页面搜索栏也有同名「环境/大区/小区」标签，
// 需在对话框内 scoped 查询以消歧）。
async function openDialog() {
  await userEvent.click(screen.getByRole('button', { name: '新增 zone / 指派' }))
  const dialog = await screen.findByRole('dialog')
  await within(dialog).findByLabelText('serverId')
  return dialog
}

describe('ZonesPage 指派表单（FR-40）', () => {
  it('环境下拉来自 listNamespaces', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openDialog()
    const nsSelect = within(dialog).getByLabelText('环境') as HTMLSelectElement
    await waitFor(() => {
      const opts = within(nsSelect).getAllByRole('option').map((o) => o.textContent)
      expect(opts).toContain('prod')
      expect(opts).toContain('test')
    })
  })

  it('serverId 下拉仅列 bukkit 子服，排除 BC 代理（bungee）', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openDialog()
    const sidSelect = within(dialog).getByLabelText('serverId') as HTMLSelectElement
    await waitFor(() => {
      const opts = within(sidSelect).getAllByRole('option').map((o) => o.textContent)
      expect(opts).toContain('lobby-1')
      expect(opts).not.toContain('bc-1')
    })
  })

  it('大区 / 小区下拉来自 zone 汇总与实例并集', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openDialog()
    const groupSelect = within(dialog).getByLabelText('大区') as HTMLSelectElement
    const zoneSelect = within(dialog).getByLabelText('小区') as HTMLSelectElement
    await waitFor(() => {
      const gopts = within(groupSelect).getAllByRole('option').map((o) => o.textContent)
      expect(gopts).toContain('gA')
      expect(gopts).toContain('gB')
      const zopts = within(zoneSelect).getAllByRole('option').map((o) => o.textContent)
      expect(zopts).toContain('z1')
      expect(zopts).toContain('z2')
    })
  })

  it('缺选必填项被拦下、不调 assignZone', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openDialog()
    // 仅选环境，其余留空
    await userEvent.selectOptions(within(dialog).getByLabelText('环境'), 'prod')
    await userEvent.click(within(dialog).getByRole('button', { name: '指派' }))
    expect(showError).toHaveBeenCalled()
    expect(vi.mocked(assignZone)).not.toHaveBeenCalled()
  })

  it('选齐合法项后提交携带所选值', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openDialog()
    // 等候选项就绪后逐项选择
    await waitFor(() =>
      expect(
        within(within(dialog).getByLabelText('serverId')).queryByText('lobby-1'),
      ).toBeInTheDocument(),
    )
    await userEvent.selectOptions(within(dialog).getByLabelText('环境'), 'prod')
    await userEvent.selectOptions(within(dialog).getByLabelText('serverId'), 'lobby-1')
    await userEvent.selectOptions(within(dialog).getByLabelText('大区'), 'gA')
    await userEvent.selectOptions(within(dialog).getByLabelText('小区'), 'z1')
    await userEvent.click(within(dialog).getByRole('button', { name: '指派' }))
    await waitFor(() =>
      expect(vi.mocked(assignZone)).toHaveBeenCalledWith(
        expect.objectContaining({
          namespace: 'prod',
          serverId: 'lobby-1',
          group: 'gA',
          zone: 'z1',
        }),
      ),
    )
  })
})
