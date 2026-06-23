// ZonesPage 单测（FR-71 区分配安全化 + 沿用 FR-40/FR-51 指派表单）：
// 锁定行为——
// ① 看板默认只读（未解锁不出改派/取消入口），解锁后出现；
// ② 取消指派需显式二次确认才调 unassignZone；
// ③ 后端返 ZONE_SERVER_ONLINE_NONEMPTY（409 排空门）时展示中文「先排空」提示；
// ④ 页面文案显「区分配」（zone→区 i18n）；
// ⑤ 指派表单：环境/serverId/大区/小区下拉来自 API、serverId 仅列 bukkit、缺选拦截、合法提交携值。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView } from '../api/types'

// mock 写操作反馈：spy showError/showSuccess，断言提示文案
const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('../components/useMessage', () => ({
  useMessage: () => ({ showError, showSuccess }),
}))

// mock 后端调用，由各用例注入数据。ApiClientError 取真实实现（onError 按 code 分支需要）。
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return {
    ApiClientError: actual.ApiClientError,
    assignZone: vi.fn(),
    unassignZone: vi.fn(),
    listAssignments: vi.fn(),
    listInstances: vi.fn(),
    listNamespaces: vi.fn(),
    zoneSummary: vi.fn(),
  }
})

import ZonesPage from './ZonesPage'
import {
  ApiClientError,
  assignZone,
  listAssignments,
  listInstances,
  listNamespaces,
  unassignZone,
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

// lobby-1 已指派到 (gA, z1)，落在 zone 桶内（可改派/取消）；bc-1 为 BC 代理（看板排除）。
const INSTANCES: InstanceView[] = [
  inst({ serverId: 'lobby-1', role: 'bukkit', group: 'gA', zone: 'z1', assigned: true }),
  inst({ serverId: 'bc-1', role: 'bungee', group: 'gB', zone: null, assigned: false }),
]

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  // 清掉所有 mock 的调用历史（含 assignZone/unassignZone），避免跨用例计数串台
  vi.clearAllMocks()
  vi.mocked(listInstances).mockResolvedValue(INSTANCES)
  vi.mocked(listNamespaces).mockResolvedValue([
    { code: 'prod', name: '生产' },
    { code: 'test', name: '测试' },
  ])
  vi.mocked(zoneSummary).mockResolvedValue([
    { group: 'gA', zone: 'z1', serverCount: 1, onlineCount: 1 },
    { group: 'gA', zone: 'z2', serverCount: 0, onlineCount: 0 },
  ])
  vi.mocked(listAssignments).mockResolvedValue([
    { namespace: 'prod', serverId: 'lobby-1', group: 'gA', zone: 'z1', note: '原备注', updatedAt: '' },
  ])
  vi.mocked(assignZone).mockResolvedValue({
    namespace: 'prod',
    serverId: 'lobby-1',
    group: 'gA',
    zone: 'z2',
    note: '',
    updatedAt: '2026-01-01T00:00:00Z',
  })
  vi.mocked(unassignZone).mockResolvedValue(undefined)
})

// 打开「新增 区 / 指派」对话框，返回对话框容器（页面搜索栏也有同名标签，需在对话框内 scoped 查询消歧）。
async function openAssignDialog() {
  await userEvent.click(screen.getByRole('button', { name: '新增 区 / 指派' }))
  const dialog = await screen.findByRole('dialog')
  await within(dialog).findByLabelText('serverId')
  return dialog
}

// combobox 下拉渲染到 body（Portal），在 screen 层查 listbox。
async function openCombobox(dialog: HTMLElement, label: string) {
  await userEvent.click(within(dialog).getByLabelText(label))
  const listbox = await screen.findByRole('listbox')
  return listbox
}

async function pick(dialog: HTMLElement, label: string, value: string) {
  const listbox = await openCombobox(dialog, label)
  await userEvent.click(within(listbox).getByText(value))
}

// 打开「解锁改派」开关
async function unlock() {
  await userEvent.click(screen.getByRole('checkbox', { name: '解锁改派' }))
}

describe('ZonesPage 看板默认只读 + 解锁（FR-71）', () => {
  it('默认未解锁：看板不出现改派/取消入口', async () => {
    renderPage(<ZonesPage />)
    // 等卡片渲染（lobby-1 落在 zone 桶）
    await screen.findAllByText('lobby-1')
    expect(screen.queryByRole('button', { name: '改派' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '取消指派' })).not.toBeInTheDocument()
  })

  it('解锁后：看板出现改派/取消入口', async () => {
    renderPage(<ZonesPage />)
    await screen.findAllByText('lobby-1')
    await unlock()
    expect(await screen.findByRole('button', { name: '改派' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '取消指派' })).toBeInTheDocument()
  })

  it('页面文案显「区分配」（zone→区 i18n）', async () => {
    renderPage(<ZonesPage />)
    expect(await screen.findByRole('heading', { name: '区分配' })).toBeInTheDocument()
  })
})

describe('ZonesPage 取消指派需显式确认（FR-71）', () => {
  it('点「取消指派」弹确认，确认后才调 unassignZone', async () => {
    renderPage(<ZonesPage />)
    await screen.findAllByText('lobby-1')
    await unlock()
    await userEvent.click(await screen.findByRole('button', { name: '取消指派' }))
    // 未确认前不调用
    expect(vi.mocked(unassignZone)).not.toHaveBeenCalled()
    const alert = await screen.findByRole('alertdialog')
    await userEvent.click(within(alert).getByRole('button', { name: '确认取消指派' }))
    await waitFor(() =>
      expect(vi.mocked(unassignZone)).toHaveBeenCalledWith('prod', 'lobby-1'),
    )
  })
})

describe('ZonesPage 排空门 409 提示（FR-71）', () => {
  it('改派遇 ZONE_SERVER_ONLINE_NONEMPTY 展示「先排空」提示', async () => {
    vi.mocked(assignZone).mockRejectedValue(
      new ApiClientError('服务器在线且有玩家', 'ZONE_SERVER_ONLINE_NONEMPTY'),
    )
    renderPage(<ZonesPage />)
    await screen.findAllByText('lobby-1')
    await unlock()
    await userEvent.click(await screen.findByRole('button', { name: '改派' }))
    const dialog = await screen.findByRole('dialog')
    // 选目标区 + 手输 serverId 复述
    await pick(dialog, '大区', 'gA')
    await pick(dialog, '小区', 'z2')
    await userEvent.type(within(dialog).getByLabelText('手输 serverId 确认'), 'lobby-1')
    await userEvent.click(within(dialog).getByRole('button', { name: '确认改派' }))
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(
        expect.stringContaining('请先排空'),
      ),
    )
  })

  it('取消指派遇 409 同样展示「先排空」提示', async () => {
    vi.mocked(unassignZone).mockRejectedValue(
      new ApiClientError('服务器在线且有玩家', 'ZONE_SERVER_ONLINE_NONEMPTY'),
    )
    renderPage(<ZonesPage />)
    await screen.findAllByText('lobby-1')
    await unlock()
    await userEvent.click(await screen.findByRole('button', { name: '取消指派' }))
    const alert = await screen.findByRole('alertdialog')
    await userEvent.click(within(alert).getByRole('button', { name: '确认取消指派' }))
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith(expect.stringContaining('请先排空')),
    )
  })
})

describe('ZonesPage 指派表单（FR-40 / FR-51）', () => {
  it('环境下拉来自 listNamespaces', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openAssignDialog()
    await waitFor(async () => {
      const listbox = await openCombobox(dialog, '环境')
      const opts = within(listbox).getAllByRole('option').map((o) => o.textContent)
      expect(opts).toContain('prod · 生产')
      expect(opts).toContain('test · 测试')
    })
  })

  it('serverId 下拉仅列 bukkit 子服，排除 BC 代理（bungee）', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openAssignDialog()
    await waitFor(async () => {
      const listbox = await openCombobox(dialog, 'serverId')
      const opts = within(listbox).getAllByRole('option').map((o) => o.textContent)
      expect(opts).toContain('lobby-1')
      expect(opts).not.toContain('bc-1')
    })
  })

  it('大区 / 小区下拉来自 zone 汇总与实例并集', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openAssignDialog()
    await waitFor(async () => {
      const glist = await openCombobox(dialog, '大区')
      const gopts = within(glist).getAllByRole('option').map((o) => o.textContent)
      expect(gopts).toContain('gA')
      expect(gopts).toContain('gB')
    })
    await waitFor(async () => {
      const zlist = await openCombobox(dialog, '小区')
      const zopts = within(zlist).getAllByRole('option').map((o) => o.textContent)
      expect(zopts).toContain('z1')
      expect(zopts).toContain('z2')
    })
  })

  it('缺选必填项被拦下、不调 assignZone', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openAssignDialog()
    await pick(dialog, '环境', 'prod · 生产')
    await userEvent.click(within(dialog).getByRole('button', { name: '指派' }))
    expect(showError).toHaveBeenCalled()
    expect(vi.mocked(assignZone)).not.toHaveBeenCalled()
  })

  it('选齐合法项后提交携带所选值', async () => {
    renderPage(<ZonesPage />)
    const dialog = await openAssignDialog()
    await waitFor(async () => {
      const listbox = await openCombobox(dialog, 'serverId')
      expect(within(listbox).queryByText('lobby-1')).toBeInTheDocument()
    })
    await pick(dialog, '环境', 'prod · 生产')
    await pick(dialog, 'serverId', 'lobby-1')
    await pick(dialog, '大区', 'gA')
    await pick(dialog, '小区', 'z1')
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
