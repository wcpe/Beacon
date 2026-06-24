// AddServerWizard 组件测试（FR-85 新服接入引导向导）：
// 锁定——① serverId 在目标环境内重复时拦截、无法进入下一步；② 通过校验后生成含 BEACON_AGENT_IDENTITY_* 的片段；
// ③ 填 zone 且角色 bukkit 时点「预建指派」调 assignZone；④ bungee 角色不展示 zone 预建。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView } from '../../api/types'

const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showError, showSuccess }),
}))

vi.mock('../../api/client', () => ({
  listInstances: vi.fn(),
  assignZone: vi.fn(),
}))

// jsdom 未实现指针捕获 / scrollIntoView：radix Select（角色下拉）打开时会调用它们，缺失会抛错。
if (!HTMLElement.prototype.hasPointerCapture) {
  HTMLElement.prototype.hasPointerCapture = () => false
  HTMLElement.prototype.setPointerCapture = () => {}
  HTMLElement.prototype.releasePointerCapture = () => {}
}
if (!HTMLElement.prototype.scrollIntoView) {
  HTMLElement.prototype.scrollIntoView = () => {}
}

import AddServerWizard from './AddServerWizard'
import { listInstances, assignZone } from '../../api/client'

function inst(overrides: Partial<InstanceView>): InstanceView {
  return {
    namespace: 'prod',
    serverId: 'lobby-1',
    role: 'bukkit',
    group: 'area1',
    zone: 'z1',
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.0',
    status: 'online',
    capacity: 100,
    weight: 10,
    metadata: {},
    lastHeartbeat: '2026-06-20T08:00:00Z',
    lastHeartbeatAgeSec: 5,
    healthReason: '',
    appliedMd5: 'abc123',
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
    registeredAt: '2026-06-20T07:00:00Z',
    ...overrides,
  }
}

function renderWizard(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

const nsOptions = [{ value: 'prod', label: 'prod · 生产' }]

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listInstances).mockResolvedValue([])
  vi.mocked(assignZone).mockResolvedValue({
    namespace: 'prod',
    serverId: 'lobby-2',
    group: 'area1',
    zone: 'z2',
    note: '',
    updatedAt: '',
  })
})

// 填步骤一身份表单的公共辅助：默认 namespace=prod、role 默认 bukkit。
async function fillStep1(
  user: ReturnType<typeof userEvent.setup>,
  serverId: string,
  group = 'area1',
  address = '10.0.0.9:25565',
) {
  await user.type(screen.getByLabelText('serverId'), serverId)
  await user.type(screen.getByLabelText('大区'), group)
  await user.type(screen.getByLabelText('地址'), address)
}

describe('AddServerWizard（FR-85 接入向导）', () => {
  it('serverId 在目标环境内重复时拦截，不进入下一步', async () => {
    vi.mocked(listInstances).mockResolvedValue([inst({ serverId: 'lobby-1', namespace: 'prod' })])
    const user = userEvent.setup()
    renderWizard(
      <AddServerWizard open onOpenChange={() => {}} namespace="prod" nsOptions={nsOptions} groupOptions={['area1']} />,
    )
    // 等查重数据就绪
    await waitFor(() => expect(listInstances).toHaveBeenCalled())
    await fillStep1(user, 'lobby-1')
    await user.click(screen.getByRole('button', { name: '下一步' }))
    // 仍停在步骤一：出现重复提示，未生成片段
    expect(await screen.findByText(/已存在|重复/)).toBeInTheDocument()
    expect(screen.queryByText(/BEACON_AGENT_IDENTITY_SERVER_ID/)).not.toBeInTheDocument()
  })

  it('校验通过后进入步骤二，生成含 BEACON_AGENT_IDENTITY_* 的片段', async () => {
    vi.mocked(listInstances).mockResolvedValue([])
    const user = userEvent.setup()
    renderWizard(
      <AddServerWizard open onOpenChange={() => {}} namespace="prod" nsOptions={nsOptions} groupOptions={['area1']} />,
    )
    await waitFor(() => expect(listInstances).toHaveBeenCalled())
    await fillStep1(user, 'lobby-2')
    await user.click(screen.getByRole('button', { name: '下一步' }))
    expect(await screen.findByText(/BEACON_AGENT_IDENTITY_SERVER_ID=lobby-2/)).toBeInTheDocument()
  })

  it('填 zone 且角色 bukkit 时点「预建指派」调 assignZone', async () => {
    vi.mocked(listInstances).mockResolvedValue([])
    const user = userEvent.setup()
    renderWizard(
      <AddServerWizard open onOpenChange={() => {}} namespace="prod" nsOptions={nsOptions} groupOptions={['area1']} />,
    )
    await waitFor(() => expect(listInstances).toHaveBeenCalled())
    await fillStep1(user, 'lobby-2')
    await user.click(screen.getByRole('button', { name: '下一步' }))
    // 步骤二填 zone 并预建指派
    await user.type(await screen.findByLabelText('小区'), 'z2')
    await user.click(screen.getByRole('button', { name: '预建指派' }))
    await waitFor(() =>
      expect(assignZone).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: 'prod', serverId: 'lobby-2', group: 'area1', zone: 'z2' }),
      ),
    )
  })

  it('bungee 角色不展示 zone 预建', async () => {
    vi.mocked(listInstances).mockResolvedValue([])
    const user = userEvent.setup()
    renderWizard(
      <AddServerWizard open onOpenChange={() => {}} namespace="prod" nsOptions={nsOptions} groupOptions={['area1']} />,
    )
    await waitFor(() => expect(listInstances).toHaveBeenCalled())
    // 切角色为 bungee
    await user.click(screen.getByRole('combobox', { name: '角色' }))
    await user.click(await screen.findByRole('option', { name: 'bungee' }))
    await fillStep1(user, 'proxy-2')
    await user.click(screen.getByRole('button', { name: '下一步' }))
    await screen.findByText(/BEACON_AGENT_IDENTITY_SERVER_ID=proxy-2/)
    // bungee 不进 zone 指派，不出现「预建指派」入口
    expect(screen.queryByRole('button', { name: '预建指派' })).not.toBeInTheDocument()
  })
})
