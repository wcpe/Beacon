// ImprintPage K/L 测试（FR-46）：
// K——命令转 done 时显示「已确认同步」而非误导的「等待回传」；
// L——diff 面板的目标子服选择器只收到拓印源 namespace 的实例（跨 ns 实例被过滤）。
// ImprintTrigger / ImprintDiffPanel / api 客户端被 mock，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { AgentCommandView, InstanceView } from '../api/types'

// 待审拓印命令（pending）：点击触发桩即注入，驱动状态轮询。
const CMD: AgentCommandView = {
  id: 5,
  namespace: 'prod',
  serverId: 'lobby-1',
  type: 'ingest-plugins',
  status: 'pending',
  createdAt: '',
  updatedAt: '',
}

// 触发桩：点击即以 CMD 调 onTriggered（替代真实 ImprintTrigger 的交互）。
vi.mock('./imprint/ImprintTrigger', () => ({
  default: ({ onTriggered }: { onTriggered: (c: AgentCommandView) => void }) => (
    <button onClick={() => onTriggered(CMD)}>触发桩</button>
  ),
}))

// diff 面板桩：把收到的 instances（serverId@namespace）渲染出来供 L 断言。
vi.mock('./imprint/ImprintDiffPanel', () => ({
  default: ({ instances }: { instances: InstanceView[] }) => (
    <div data-testid="panel-instances">
      {instances.map((i) => `${i.serverId}@${i.namespace}`).join(',')}
    </div>
  ),
}))

vi.mock('../api/client', () => ({
  listInstances: vi.fn(),
  zoneSummary: vi.fn(),
  imprintStatus: vi.fn(),
}))

import ImprintPage from './ImprintPage'
import { listInstances, zoneSummary, imprintStatus } from '../api/client'

function inst(serverId: string, namespace: string, group: string): InstanceView {
  return {
    namespace,
    serverId,
    role: 'bukkit',
    group,
    zone: null,
    assigned: true,
    address: '10.0.0.1:25565',
    version: '1.20.4',
    status: 'online',
    capacity: 100,
    weight: 1,
    metadata: {},
    lastHeartbeat: '',
    appliedMd5: '',
    playerCount: 0,
    tps: 0,
    backends: [],
    registeredAt: '',
  }
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  // 跨 namespace 实例：prod 的 lobby-1 + staging 的 mini-1（L 应过滤掉后者）
  vi.mocked(listInstances).mockResolvedValue([
    inst('lobby-1', 'prod', 'area1'),
    inst('mini-1', 'staging', 'area2'),
  ])
  vi.mocked(zoneSummary).mockResolvedValue([])
})

describe('ImprintPage', () => {
  it('命令 done 态显示「已确认同步」而非「等待回传」（K）', async () => {
    vi.mocked(imprintStatus).mockResolvedValue({ ...CMD, status: 'done' })
    renderPage(<ImprintPage />)
    await userEvent.click(await screen.findByText('触发桩'))
    expect(await screen.findByText('拓印已确认同步（命令完成）')).toBeInTheDocument()
    expect(screen.queryByText(/等待实例.*回传/)).not.toBeInTheDocument()
  })

  it('diff 面板目标选择器仅限拓印源 namespace 的实例（L）', async () => {
    vi.mocked(imprintStatus).mockResolvedValue({ ...CMD, status: 'ready' })
    renderPage(<ImprintPage />)
    await userEvent.click(await screen.findByText('触发桩'))
    const panel = await screen.findByTestId('panel-instances')
    // 只含 prod 的 lobby-1，排除 staging 的 mini-1
    expect(panel.textContent).toBe('lobby-1@prod')
  })
})
