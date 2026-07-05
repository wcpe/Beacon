// 多级灰度配置同步中心页面单测：锁定 5 步向导、持久规划预览与千级目标操作性。
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import ConfigSyncCenterPage from './ConfigSyncCenterPage'
import type { FileSyncEvent, FileSyncTaskView, InstanceView } from '@/api/types'

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
  startFileSyncTask,
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
  sourceReady: false,
  sourceFileCount: 0,
  sourceTotalBytes: 0,
  totalTargets: 0,
  plannedTargets: 0,
  succeededTargets: 0,
  failedTargets: 0,
  skippedTargets: 0,
  currentBatch: 0,
  totalBatches: 0,
  batches: [],
  lastError: '',
  logs: [],
  targets: [],
  createdAt: '2026-07-04T00:00:00Z',
  updatedAt: '2026-07-04T00:00:00Z',
  startedAt: '',
  finishedAt: '',
}

const PLANNED_TASK: FileSyncTaskView = {
  ...TASK,
  status: 'planned',
  sourceReady: true,
  sourceFileCount: 128,
  sourceTotalBytes: 96 * 1024 * 1024,
  plannedTargets: 1,
  totalTargets: 1,
  totalBatches: 1,
  batches: [
    {
      id: 1,
      taskId: 1,
      batchNo: 1,
      status: 'pending',
      plannedCount: 1,
      successCount: 0,
      failedCount: 0,
      startedAt: '',
      finishedAt: '',
    },
  ],
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
  targets: [taskTarget({ serverId: 'target-01', zone: 'zone-02' })],
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

async function goToStep(name: string) {
  await userEvent.click(await screen.findByRole('button', { name }))
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
  vi.mocked(planFileSyncTask).mockResolvedValue(PLANNED_TASK)
  vi.mocked(startFileSyncTask).mockResolvedValue({ ...PLANNED_TASK, status: 'running' })
  vi.mocked(streamFileSyncTaskEvents).mockResolvedValue()
})

describe('ConfigSyncCenterPage', () => {
  it('渲染 5 步向导，并且前 4 步不挂载任务目标明细', async () => {
    const { container } = renderPage()

    expect(await screen.findByRole('button', { name: '源与目录' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '目标范围' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '灰度策略' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '安全检查' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '预览与启动' })).toBeInTheDocument()
    expect(container.firstElementChild).not.toHaveClass('overflow-hidden')

    expect(screen.getByText('源清单状态')).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: '任务目标明细表格' })).not.toBeInTheDocument()

    await goToStep('目标范围')
    expect(screen.getByLabelText('选择 target-01')).toBeInTheDocument()
    expect(screen.queryByText('proxy-01')).not.toBeInTheDocument()
    expect(screen.queryByText('lost-01')).not.toBeInTheDocument()
    expect(screen.queryByRole('table', { name: '任务目标明细表格' })).not.toBeInTheDocument()

    await goToStep('灰度策略')
    expect(screen.getByText('批次编排')).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: '任务目标明细表格' })).not.toBeInTheDocument()

    await goToStep('安全检查')
    expect(screen.getByText('覆盖前备份')).toBeInTheDocument()
    expect(screen.queryByRole('table', { name: '任务目标明细表格' })).not.toBeInTheDocument()
  })

  it('未选择目标时不能生成规划预览', async () => {
    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await goToStep('预览与启动')

    const previewButton = screen.getByRole('button', { name: '生成规划预览' })
    expect(previewButton).toBeDisabled()
    expect(screen.getByRole('button', { name: '开始同步' })).toBeDisabled()
    expect(createFileSyncTask).not.toHaveBeenCalled()
    expect(planFileSyncTask).not.toHaveBeenCalled()
  })

  it('最后一步才调用 create+plan，且 planned 后才能开始同步', async () => {
    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await goToStep('目标范围')
    await userEvent.click(screen.getByLabelText('选择 target-01'))
    await goToStep('灰度策略')
    await goToStep('安全检查')

    expect(createFileSyncTask).not.toHaveBeenCalled()
    expect(planFileSyncTask).not.toHaveBeenCalled()

    await goToStep('预览与启动')
    expect(screen.getByRole('button', { name: '开始同步' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: '生成规划预览' }))

    await waitFor(() =>
      expect(createFileSyncTask).toHaveBeenCalledWith(
        expect.objectContaining({
          sourceServerId: 'source-01',
          directory: 'plugins/AllinCore',
        }),
      ),
    )
    expect(planFileSyncTask).toHaveBeenCalledWith('task-1', { targetServerIds: ['target-01'] })
    expect(await screen.findByText('持久规划预览')).toBeInTheDocument()
    expect(screen.getByText('源清单已就绪')).toBeInTheDocument()

    const startButton = screen.getByRole('button', { name: '开始同步' })
    expect(startButton).toBeEnabled()
    await userEvent.click(startButton)

    await waitFor(() => expect(startFileSyncTask).toHaveBeenCalledWith('task-1'))
  })

  it('源清单未就绪时禁止启动，收到 SSE 任务补丁后才允许启动', async () => {
    let onEvent: ((event: FileSyncEvent) => void) | undefined
    vi.mocked(planFileSyncTask).mockResolvedValue({ ...PLANNED_TASK, sourceReady: false })
    vi.mocked(streamFileSyncTaskEvents).mockImplementation(async (_id, cb) => {
      onEvent = cb
    })

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await goToStep('目标范围')
    await userEvent.click(screen.getByLabelText('选择 target-01'))
    await goToStep('预览与启动')
    await userEvent.click(screen.getByRole('button', { name: '生成规划预览' }))

    const startButton = await screen.findByRole('button', { name: '开始同步' })
    expect(startButton).toBeDisabled()

    act(() => {
      onEvent?.({
        type: 'task',
        status: 'planned',
        task: {
          status: 'planned',
          sourceReady: true,
          sourceFileCount: 128,
          sourceTotalBytes: 96 * 1024 * 1024,
        },
      })
    })

    await waitFor(() => expect(startButton).toBeEnabled())
  })

  it('1000+ 目标在目标范围步骤仍只渲染分页窗口并可翻页', async () => {
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targetInstances(1000),
    ])

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await goToStep('目标范围')
    const targetTable = screen.getByRole('table', { name: '目标服务器表格' })
    expect(within(targetTable).getAllByRole('checkbox')).toHaveLength(25)
    expect(within(targetTable).getAllByRole('row')).toHaveLength(26)

    const targetPager = screen.getByRole('navigation', { name: '目标服务器分页' })
    await userEvent.click(within(targetPager).getByRole('button', { name: '下一页' }))

    expect(within(targetTable).getByText('target-0026')).toBeInTheDocument()
    expect(within(targetTable).queryByText('target-0001')).not.toBeInTheDocument()
  })

  it('宽泛搜索和分组全选保持分页，并把页外匹配目标带入预览规划', async () => {
    const targets = targetInstances(80)
    vi.mocked(listInstances).mockResolvedValue([
      instance({ serverId: 'source-01', role: 'bukkit' }),
      ...targets,
    ])

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await goToStep('目标范围')
    await userEvent.type(screen.getByLabelText('搜索目标服务器'), 'target')

    const targetTable = screen.getByRole('table', { name: '目标服务器表格' })
    expect(within(targetTable).getAllByRole('checkbox')).toHaveLength(25)
    expect(screen.getByText('当前筛选 80 台')).toBeInTheDocument()

    await userEvent.clear(screen.getByLabelText('搜索目标服务器'))
    await userEvent.type(screen.getByLabelText('搜索目标服务器'), 'zone-02')
    await userEvent.click(screen.getByRole('button', { name: '全选当前筛选结果' }))
    await goToStep('预览与启动')
    await userEvent.click(screen.getByRole('button', { name: '生成规划预览' }))

    const expected = targets.filter((item) => item.zone === 'zone-02').map((item) => item.serverId)
    await waitFor(() =>
      expect(planFileSyncTask).toHaveBeenCalledWith('task-1', { targetServerIds: expected }),
    )
    expect(expected.length).toBeGreaterThan(1)
  })

  it('SSE target 事件能合并到规划预览明细', async () => {
    let onEvent: ((event: FileSyncEvent) => void) | undefined
    vi.mocked(streamFileSyncTaskEvents).mockImplementation(async (_id, cb) => {
      onEvent = cb
    })

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await goToStep('目标范围')
    await userEvent.click(screen.getByLabelText('选择 target-01'))
    await goToStep('预览与启动')
    await userEvent.click(screen.getByRole('button', { name: '生成规划预览' }))
    await screen.findByRole('table', { name: '任务目标明细表格' })

    act(() => {
      onEvent?.({
        type: 'target',
        target: {
          serverId: 'target-01',
          status: 'transferring',
          changedFileCount: 18,
          skippedFileCount: 110,
          bytesTotal: 1024,
          bytesDone: 512,
        },
      })
    })

    const detailTable = screen.getByRole('table', { name: '任务目标明细表格' })
    expect(await within(detailTable).findByText('传输中')).toBeInTheDocument()
    expect(within(detailTable).getByText('18')).toBeInTheDocument()
    expect(within(detailTable).getByText('512 B / 1 KB')).toBeInTheDocument()
  })

  it('SSE 日志只保留最近 200 条', async () => {
    let onEvent: ((event: FileSyncEvent) => void) | undefined
    vi.mocked(streamFileSyncTaskEvents).mockImplementation(async (_id, cb) => {
      onEvent = cb
    })

    renderPage()

    await screen.findByDisplayValue(/source-01/)
    await goToStep('目标范围')
    await userEvent.click(screen.getByLabelText('选择 target-01'))
    await goToStep('预览与启动')
    await userEvent.click(screen.getByRole('button', { name: '生成规划预览' }))
    await screen.findByText('已规划 1 台目标')

    act(() => {
      for (let id = 10; id <= 214; id++) {
        onEvent?.({
          type: 'log',
          log: {
            id,
            taskId: 'task-1',
            batchNo: 1,
            serverId: 'target-01',
            level: 'INFO',
            message: `日志 ${id}`,
            createdAt: `2026-07-04T00:00:${String(id % 60).padStart(2, '0')}Z`,
          },
        })
      }
    })

    expect(screen.queryByText('日志 10')).not.toBeInTheDocument()
    expect(screen.getByText('日志 214')).toBeInTheDocument()
  })
})
