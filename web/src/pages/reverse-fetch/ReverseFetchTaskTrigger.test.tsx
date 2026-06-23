// ReverseFetchTaskTrigger 关键路径测试（FR-60）：
// 选在线 bukkit 源 + 入库目标层 + 目标组 → 以正确入参调用 createScanTask（namespace 随源实例带上）；
// group 层不带 target；server 层须选目标子服、否则不发请求；离线 / 非 bukkit 不出现在源候选里。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView, ReverseFetchTaskView } from '../../api/types'

vi.mock('../../api/client', () => ({
  createScanTask: vi.fn(),
}))

vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ReverseFetchTaskTrigger from './ReverseFetchTaskTrigger'
import { createScanTask } from '../../api/client'

function inst(serverId: string, group: string, status: string, role = 'bukkit'): InstanceView {
  return {
    namespace: 'prod',
    serverId,
    role,
    group,
    zone: null,
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.20.4',
    status,
    capacity: 100,
    weight: 1,
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
  }
}

const instances: InstanceView[] = [
  inst('lobby-1', 'area1', 'online'),
  inst('lobby-9', 'area2', 'offline'),
  inst('proxy-1', 'area1', 'online', 'bungee'),
]

const taskStub: ReverseFetchTaskView = {
  id: 7,
  namespace: 'prod',
  serverId: 'lobby-1',
  scope: 'group',
  group: 'area1',
  target: '',
  status: 'scanning',
  scanCommandId: 1,
  submitCommandId: 0,
  totalFiles: 0,
  selectedCount: 0,
  overThresholdCount: 0,
  skippedCount: 0,
  files: [],
  selectedPaths: [],
  operator: 'admin',
  note: '',
  createdAt: '',
  updatedAt: '',
}

function renderTrigger(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(createScanTask).mockResolvedValue(taskStub)
})

describe('ReverseFetchTaskTrigger', () => {
  it('group 层：以正确入参调用 createScanTask（namespace 随源，target 省略）', async () => {
    renderTrigger(
      <ReverseFetchTaskTrigger instances={instances} groups={['area1', 'area2']} onCreated={() => {}} />,
    )
    // 源默认取首个在线 bukkit（lobby-1），目标层默认 group，目标组默认随源 group=area1
    await userEvent.click(screen.getByRole('button', { name: '开始扫描' }))

    await waitFor(() => {
      expect(vi.mocked(createScanTask)).toHaveBeenCalledWith('lobby-1', 'prod', {
        scope: 'group',
        group: 'area1',
        target: undefined,
      })
    })
  })

  it('server 层：带目标子服调用 createScanTask', async () => {
    renderTrigger(
      <ReverseFetchTaskTrigger instances={instances} groups={['area1', 'area2']} onCreated={() => {}} />,
    )
    await userEvent.selectOptions(screen.getByLabelText('入库目标层'), 'server')
    // server 层出现目标子服选择器
    await userEvent.selectOptions(screen.getByLabelText('目标子服'), 'lobby-1')
    await userEvent.click(screen.getByRole('button', { name: '开始扫描' }))

    await waitFor(() => {
      expect(vi.mocked(createScanTask)).toHaveBeenCalledWith('lobby-1', 'prod', {
        scope: 'server',
        group: 'area1',
        target: 'lobby-1',
      })
    })
  })

  it('server 层未选目标子服时不调用 createScanTask', async () => {
    renderTrigger(
      <ReverseFetchTaskTrigger instances={instances} groups={['area1', 'area2']} onCreated={() => {}} />,
    )
    await userEvent.selectOptions(screen.getByLabelText('入库目标层'), 'server')
    await userEvent.click(screen.getByRole('button', { name: '开始扫描' }))
    expect(vi.mocked(createScanTask)).not.toHaveBeenCalled()
  })

  it('仅在线 bukkit 出现在抓取源候选里（离线 / bungee 排除）', () => {
    renderTrigger(
      <ReverseFetchTaskTrigger instances={instances} groups={['area1', 'area2']} onCreated={() => {}} />,
    )
    const source = screen.getByLabelText('抓取源（在线实例）') as HTMLSelectElement
    const values = Array.from(source.options).map((o) => o.value)
    expect(values).toContain('lobby-1')
    expect(values).not.toContain('lobby-9') // 离线
    expect(values).not.toContain('proxy-1') // bungee
  })

  it('无在线源时给提示且不可提交', () => {
    renderTrigger(
      <ReverseFetchTaskTrigger
        instances={[inst('lobby-9', 'area2', 'offline')]}
        groups={['area2']}
        onCreated={() => {}}
      />,
    )
    expect(screen.getByText('当前无在线实例可抓取')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '开始扫描' })).toBeDisabled()
  })
})
