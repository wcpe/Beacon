// 多级灰度配置同步中心页面单测：锁定 bukkit-only 目标选择与创建后规划动作。
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import ConfigSyncCenterPage from './ConfigSyncCenterPage'
import type { InstanceView, FileSyncTaskView } from '@/api/types'

vi.mock('@/api/client', () => ({
  listInstances: vi.fn(),
  listFileSyncTasks: vi.fn(),
  createFileSyncTask: vi.fn(),
  getFileSyncTask: vi.fn(),
  planFileSyncTask: vi.fn(),
  startFileSyncTask: vi.fn(),
  pauseFileSyncTask: vi.fn(),
  resumeFileSyncTask: vi.fn(),
  terminateFileSyncTask: vi.fn(),
  streamFileSyncTaskEvents: vi.fn(),
}))

import {
  createFileSyncTask,
  listFileSyncTasks,
  listInstances,
  planFileSyncTask,
  streamFileSyncTaskEvents,
} from '@/api/client'

const ZERO_PROXY: InstanceView['proxy'] = {
  onlineConnections: 0,
  threadCount: 0,
  uptimeMs: 0,
  backendUp: 0,
  backendTotal: 0,
  backendAvgLatencyMs: -1,
}

function instance(
  partial: Partial<InstanceView> & Pick<InstanceView, 'serverId' | 'role'>,
): InstanceView {
  return {
    namespace: 'prod',
    serverId: partial.serverId,
    role: partial.role,
    group: partial.group ?? 'server-a',
    zone: partial.zone ?? 'zone-01',
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.20.4',
    agentVersion: '0.12.0',
    status: partial.status ?? 'online',
    capacity: 100,
    weight: 1,
    metadata: {},
    lastHeartbeat: '2026-07-04T00:00:00Z',
    lastHeartbeatAgeSec: 1,
    healthReason: '',
    appliedMd5: 'abc',
    playerCount: 0,
    tps: 20,
    backends: [],
    zoneDefaultEntry: false,
    proxy: partial.role === 'bungee' ? { ...ZERO_PROXY, onlineConnections: 20 } : ZERO_PROXY,
    registeredAt: '2026-07-04T00:00:00Z',
    ...partial,
  }
}

function targetInstances(count: number): InstanceView[] {
  return Array.from({ length: count }, (_, index) => {
    const n = index + 1
    return instance({
      serverId: `target-${String(n).padStart(4, '0')}`,
      role: 'bukkit',
      group: `server-${String((index % 8) + 1).padStart(2, '0')}`,
      zone: `zone-${String((index % 40) + 1).padStart(2, '0')}`,
      address: `10.0.${Math.floor(index / 255)}.${(index % 255) + 1}:25565`,
    })
  })
}

function taskTarget(
  partial: Partial<FileSyncTaskView['targets'][number]> &
    Pick<FileSyncTaskView['targets'][number], 'serverId'>,
): FileSyncTaskView['targets'][number] {
  return {
    taskId: 'task-1',
    batchNo: 1,
    serverId: partial.serverId,
    namespace: 'prod',
    group: 'server-a',
    zone: 'zone-01',
    status: 'pending',
    backupPath: '',
    currentFileCount: 0,
    changedFileCount: 0,
    skippedFileCount: 0,
    bytesTotal: 0,
    bytesDone: 0,
    error: '',
    updatedAt: '2026-07-04T00:00:00Z',
    ...partial,
  }
}

const TASK: FileSyncTaskView = {
  id: 'task-1',
  namespace: 'prod',
  sourceServerId: 'source-01',
  directory: 'plugins/AllinCore',
  status: 'draft',
  batchSize: 20,
  intervalSec: 30,
  failureThresholdPercent: 20,
  operator: 'admin',
  totalTargets: 0,
  plannedTargets: 0,
  succeededTargets: 0,
  failedTargets: 0,
  skippedTargets: 0,
  currentBatch: 0,
  totalBatches: 0,
  lastError: '',
  logs: [],
  targets: [],
  createdAt: '2026-07-04T00:00:00Z',
  updatedAt: '2026-07-04T00:00:00Z',
  startedAt: '',
  finishedAt: '',
}

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/file-sync']}>
        <Routes>
          <Route path="/file-sync" element={<ConfigSyncCenterPage />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listInstances).mockResolvedValue([
    instance({ serverId: 'source-01', role: 'bukkit' }),
    instance({ serverId: 'target-01', role: 'bukkit', zone: 'zone-02' }),
    instance({ serverId: 'proxy-01', role: 'bungee' }),
    instance({ serverId: 'lost-01', role: 'bukkit', status: 'lost' }),
  ])
  vi.mocked(listFileSyncTasks).mockResolvedValue([])
  vi.mocked(createFileSyncTask).mockResolvedValue(TASK)
  vi.mocked(planFileSyncTask).mockResolvedValue({
    ...TASK,
    status: 'planned',
    plannedTargets: 1,
    totalTargets: 1,
    totalBatches: 1,
    logs: [
      {
        id: 9,
        taskId: 'task-1',
        batchNo: 0,
        serverId: '',
        level: 'INFO',
        message: '已规划 1 台目标',
        createdAt: '2026-07-04T00:00:00Z',
      },
    ],
    targets: [
      {
        taskId: 'task-1',
        batchNo: 1,
        serverId: 'target-01',
        namespace: 'prod',
        group: 'server-a',
        zone: 'zone-02',
        status: 'pending',
        backupPath: '',
        currentFileCount: 0,
        changedFileCount: 0,
        skippedFileCount: 0,
        bytesTotal: 0,
        bytesDone: 0,
        error: '',
        updatedAt: '2026-07-04T00:00:00Z',
      },
    ],
  })
  vi.mocked(streamFileSyncTaskEvents).mockResolvedValue()
})

describe('ConfigSyncCenterPage', () => {
  it('只展示可选 bukkit 在线服，并用选中目标创建后规划任务', async () => {
    const { container } = renderPage()

    expect(await screen.findByDisplayValue(/source-01/)).toBeInTheDocument()
    expect(container.firstElementChild).not.toHaveClass('overflow-hidden')
    expect(screen.getByLabelText('选择 target-01')).toBeInTheDocument()
    expect(screen.queryByText('proxy-01')).not.toBeInTheDocument()
    expect(screen.queryByText('lost-01')).not.toBeInTheDocument()

    await userEvent.click(screen.getByLabelText('选择 target-01'))
    await userEvent.click(screen.getByRole('button', { name: '规划目标' }))

    await waitFor(() =>
      expect(createFileSyncTask).toHaveBeenCalledWith(
        expect.objectContaining({
          sourceServerId: 'source-01',
          directory: 'plugins/AllinCore',
        }),
      ),
    )
    expect(planFileSyncTask).toHaveBeenCalledWith('task-1', { targetServerIds: ['target-01'] })
    expect(streamFileSyncTaskEvents).toHaveBeenCalledWith(
      'task-1',
      expect.any(Function),
      expect.objectContaining({ afterLogId: 9 }),
    )
  })

  it('1000 实例首屏只渲染一个分页窗口的目标行和复选框', async () => {
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targetInstances(1000),
    ])

    renderPage()

    expect(await screen.findByDisplayValue(/source-01/)).toBeInTheDocument()
    const targetTable = screen.getByRole('table', { name: '目标服务器表格' })
    expect(within(targetTable).getAllByRole('checkbox')).toHaveLength(25)
    expect(within(targetTable).getAllByRole('row')).toHaveLength(26)
  })

  it('宽泛搜索命中大量目标时仍保持分页窗口', async () => {
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targetInstances(120),
    ])

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await userEvent.type(screen.getByLabelText('搜索目标服务器'), 'target')

    const targetTable = screen.getByRole('table', { name: '目标服务器表格' })
    expect(within(targetTable).getAllByRole('checkbox')).toHaveLength(25)
    expect(screen.getByText('当前筛选 120 台')).toBeInTheDocument()
  })

  it('普通模式下勾选当前页目标不会把分页重置到第一页', async () => {
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targetInstances(30),
    ])

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    const targetTable = screen.getByRole('table', { name: '目标服务器表格' })
    const targetPager = screen.getByRole('navigation', { name: '目标服务器分页' })
    await userEvent.click(within(targetPager).getByRole('button', { name: '下一页' }))
    expect(within(targetTable).getByText('target-0026')).toBeInTheDocument()

    await userEvent.click(screen.getByLabelText('选择 target-0026'))

    expect(within(targetTable).getByText('target-0026')).toBeInTheDocument()
    expect(within(targetTable).queryByText('target-0001')).not.toBeInTheDocument()
  })

  it('全选当前筛选结果会提交当前页外的隐藏匹配项', async () => {
    const targets = targetInstances(80)
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targets,
    ])

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await userEvent.type(screen.getByLabelText('搜索目标服务器'), 'zone-02')
    await userEvent.click(screen.getByRole('button', { name: '全选当前筛选结果' }))
    await userEvent.click(screen.getByRole('button', { name: '规划目标' }))

    const expected = targets.filter((item) => item.zone === 'zone-02').map((item) => item.serverId)
    await waitFor(() =>
      expect(planFileSyncTask).toHaveBeenCalledWith('task-1', { targetServerIds: expected }),
    )
    expect(expected.length).toBeGreaterThan(1)
  })

  it('清空当前筛选结果只移除匹配目标', async () => {
    const targets = targetInstances(20)
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targets,
    ])

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await userEvent.click(screen.getByRole('button', { name: '全选全部目标' }))
    await userEvent.type(screen.getByLabelText('搜索目标服务器'), 'zone-02')
    await userEvent.click(screen.getByRole('button', { name: '清空当前筛选结果' }))
    await userEvent.click(screen.getByRole('button', { name: '规划目标' }))

    const expected = targets.filter((item) => item.zone !== 'zone-02').map((item) => item.serverId)
    await waitFor(() =>
      expect(planFileSyncTask).toHaveBeenCalledWith('task-1', { targetServerIds: expected }),
    )
  })

  it('已选摘要支持单个移除', async () => {
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targetInstances(3),
    ])

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await userEvent.click(screen.getByLabelText('选择 target-0001'))
    await userEvent.click(screen.getByLabelText('选择 target-0002'))

    expect(screen.getByText('已选摘要')).toBeInTheDocument()
    expect(screen.getByText('已选 2 台')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '移除 target-0001' }))
    await userEvent.click(screen.getByRole('button', { name: '规划目标' }))

    await waitFor(() =>
      expect(planFileSyncTask).toHaveBeenCalledWith('task-1', {
        targetServerIds: ['target-0002'],
      }),
    )
  })

  it('任务目标明细支持 serverId 搜索、状态筛选和失败优先', async () => {
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targetInstances(2),
    ])
    vi.mocked(planFileSyncTask).mockResolvedValue({
      ...TASK,
      status: 'planned',
      plannedTargets: 3,
      totalTargets: 3,
      totalBatches: 1,
      targets: [
        taskTarget({ serverId: 'target-ok', status: 'succeeded' }),
        taskTarget({ serverId: 'target-failed', status: 'failed', error: '下载失败' }),
        taskTarget({ serverId: 'other-failed', status: 'failed', error: '备份失败' }),
      ],
    })

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await userEvent.click(screen.getByLabelText('选择 target-0001'))
    await userEvent.click(screen.getByRole('button', { name: '规划目标' }))
    await screen.findByText('target-ok')

    await userEvent.type(screen.getByLabelText('明细服务器搜索'), 'target')
    await userEvent.selectOptions(screen.getByLabelText('明细状态筛选'), 'failed')
    await userEvent.click(screen.getByRole('button', { name: '失败优先' }))

    const detailTable = screen.getByRole('table', { name: '任务目标明细表格' })
    expect(within(detailTable).queryByText('target-ok')).not.toBeInTheDocument()
    expect(within(detailTable).getByText('target-failed')).toBeInTheDocument()
    expect(within(detailTable).queryByText('other-failed')).not.toBeInTheDocument()
  })
})
