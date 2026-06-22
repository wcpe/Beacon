// ImprintTrigger 关键路径测试（FR-46）：
// 选在线源 + 文件 path → 触发 → 以正确入参调用 triggerImprint（namespace 随源实例带上）；
// path 为空时校验拦截、不发请求；离线实例不出现在拓印源候选里。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView } from '../../api/types'

vi.mock('../../api/client', () => ({
  triggerImprint: vi.fn(),
}))

vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ImprintTrigger from './ImprintTrigger'
import { triggerImprint } from '../../api/client'

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
    registeredAt: '',
  }
}

const instances: InstanceView[] = [
  inst('lobby-1', 'area1', 'online'),
  inst('lobby-9', 'area2', 'offline'),
]

function renderTrigger(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(triggerImprint).mockResolvedValue({
    id: 1,
    namespace: 'prod',
    serverId: 'lobby-1',
    type: 'ingest-plugins',
    status: 'pending',
    createdAt: '',
    updatedAt: '',
  })
})

describe('ImprintTrigger', () => {
  it('选在线源 + 文件 path 触发，以正确入参调用 triggerImprint', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)

    // 源默认取首个在线实例 lobby-1；填目标 path
    await userEvent.type(
      screen.getByLabelText('目标文件 path（相对 plugins/）'),
      'AllinCore/config.yml',
    )
    await userEvent.click(screen.getByRole('button', { name: '拓印此文件' }))

    await waitFor(() => {
      // namespace 随源实例（lobby-1 属 prod）带上
      expect(vi.mocked(triggerImprint)).toHaveBeenCalledWith('lobby-1', 'prod', {
        path: 'AllinCore/config.yml',
      })
    })
  })

  it('path 为空时不调用 triggerImprint', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)
    await userEvent.click(screen.getByRole('button', { name: '拓印此文件' }))
    expect(vi.mocked(triggerImprint)).not.toHaveBeenCalled()
  })

  it('离线实例不出现在拓印源候选里', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)
    const source = screen.getByLabelText('拓印源（在线实例）') as HTMLSelectElement
    const values = Array.from(source.options).map((o) => o.value)
    expect(values).toContain('lobby-1')
    expect(values).not.toContain('lobby-9')
  })
})
