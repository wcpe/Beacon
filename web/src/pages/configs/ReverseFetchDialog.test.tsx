// ReverseFetchDialog 关键路径测试（FR-39）：
// 打开对话框 → 选在线实例源 + 目标组 → 触发 → 以正确入参调用 triggerReverseFetch；
// 切到实例层后未选目标实例时校验拦截、不发请求；离线实例不出现在抓取源候选里。
// api/client 被 mock，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView } from '../../api/types'

// mock 后端调用，由用例断言
vi.mock('../../api/client', () => ({
  triggerReverseFetch: vi.fn(),
}))

// mock 全局消息提示，避免 toast 依赖
vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ReverseFetchDialog from './ReverseFetchDialog'
import { triggerReverseFetch } from '../../api/client'

// 构造一条实例视图（仅填测试关心字段，其余给安全缺省值）
function inst(serverId: string, group: string, status: string): InstanceView {
  return {
    namespace: 'prod',
    serverId,
    role: 'bukkit',
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
  inst('server-01', 'server-a', 'online'),
  inst('server-09', 'server-b', 'offline'),
]

function renderDialog(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(triggerReverseFetch).mockResolvedValue({
    id: 1,
    namespace: 'prod',
    serverId: 'server-01',
    type: 'ingest-plugins',
    status: 'pending',
    createdAt: '',
    updatedAt: '',
  })
})

describe('ReverseFetchDialog', () => {
  it('选在线源与目标组后触发，以 group 层入参调用 triggerReverseFetch', async () => {
    renderDialog(<ReverseFetchDialog instances={instances} groups={['server-a', 'server-b']} />)

    // 打开对话框
    await userEvent.click(screen.getByRole('button', { name: '反向抓取' }))
    await screen.findByRole('dialog')

    // 源默认取首个在线实例 server-01；选目标组（FR-51：combobox 展开点选，下拉渲染到 body）
    await userEvent.click(screen.getByLabelText('目标组'))
    await userEvent.click(within(await screen.findByRole('listbox')).getByText('server-a'))

    // 触发（默认组级，无需选目标实例）
    await userEvent.click(screen.getByRole('button', { name: '触发抓取' }))

    await waitFor(() => {
      // namespace 随源实例（server-01 属 prod）带上，作为第二个实参
      expect(vi.mocked(triggerReverseFetch)).toHaveBeenCalledWith('server-01', 'prod', {
        scope: 'group',
        group: 'server-a',
        target: undefined,
      })
    })
  })

  it('实例层未选目标实例时不调用 triggerReverseFetch', async () => {
    renderDialog(<ReverseFetchDialog instances={instances} groups={['server-a']} />)
    await userEvent.click(screen.getByRole('button', { name: '反向抓取' }))
    await screen.findByRole('dialog')

    await userEvent.click(screen.getByLabelText('目标组'))
    await userEvent.click(within(await screen.findByRole('listbox')).getByText('server-a'))
    // 切到实例层但不选目标实例（目标层为枚举，仍是原生 select）
    await userEvent.selectOptions(screen.getByLabelText('目标层'), 'server')

    await userEvent.click(screen.getByRole('button', { name: '触发抓取' }))
    // 目标实例为空：校验拦截，不发请求
    expect(vi.mocked(triggerReverseFetch)).not.toHaveBeenCalled()
  })

  it('离线实例不出现在抓取源候选里', async () => {
    renderDialog(<ReverseFetchDialog instances={instances} groups={['server-a']} />)
    await userEvent.click(screen.getByRole('button', { name: '反向抓取' }))
    await screen.findByRole('dialog')

    // FR-51：抓取源改为 combobox，展开后断言候选仅含在线实例
    await userEvent.click(screen.getByLabelText('抓取源（在线实例）'))
    const listbox = await screen.findByRole('listbox')
    const values = within(listbox).getAllByRole('option').map((o) => o.textContent)
    expect(values).toContain('server-01')
    expect(values).not.toContain('server-09')
  })
})
