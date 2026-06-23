// ImprintTrigger 关键路径测试（FR-46 + FR-69）：
// FR-46：选在线源 + 文件 path → 触发 → 以正确入参调用 triggerImprint（namespace 随源实例带上）；
//        path 为空时校验拦截、不发请求；离线实例不出现在拓印源候选里。
// FR-69：以关键字即时搜该源已知文件替代手输——键入关键字即时过滤候选、点选后 path 被填入并可触发；
//        候选列表外仍可手输自定义 path（兜底未追踪文件）；候选为空时给提示且仍可手输。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { EffectiveFileTreeView } from '../../api/client'
import type { InstanceView } from '../../api/types'

vi.mock('../../api/client', () => ({
  triggerImprint: vi.fn(),
  effectiveFiles: vi.fn(),
}))

vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ImprintTrigger from './ImprintTrigger'
import { triggerImprint, effectiveFiles } from '../../api/client'

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
  inst('lobby-1', 'area1', 'online'),
  inst('lobby-9', 'area2', 'offline'),
]

// 该源已知有效文件树：供 FR-69 关键字搜文件取候选 path。
function fileTree(paths: string[]): EffectiveFileTreeView {
  return {
    namespace: 'prod',
    serverId: 'lobby-1',
    group: 'area1',
    zone: null,
    fileTreeMd5: 'abc',
    files: paths.map((p) => ({
      path: p,
      md5: 'x',
      content: '',
      wholeFile: true,
      sources: [],
      deletions: [],
    })),
  }
}

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
  // 默认源 lobby-1 已知三份文件，供候选搜索断言
  vi.mocked(effectiveFiles).mockResolvedValue(
    fileTree(['AllinCore/config.yml', 'AllinCore/lang/zh.yml', 'Residence/config.yml']),
  )
})

describe('ImprintTrigger', () => {
  it('选在线源 + 文件 path 触发，以正确入参调用 triggerImprint', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)

    // 源默认取首个在线实例 lobby-1；在搜文件框手输目标 path（兜底录入）
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

  // ===== FR-69：关键字搜文件替代手输 =====

  it('键入关键字即时过滤出该源已知文件候选', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)
    // 等候选拉取就绪（effectiveFiles 解析）
    await waitFor(() => expect(vi.mocked(effectiveFiles)).toHaveBeenCalled())

    const input = screen.getByLabelText('目标文件 path（相对 plugins/）')
    await userEvent.type(input, 'AllinCore')
    // 仅 AllinCore 前缀两份命中，Residence 被过滤掉
    await waitFor(() => {
      const opts = screen.getAllByRole('option').map((o) => o.textContent)
      expect(opts).toContain('AllinCore/config.yml')
      expect(opts).toContain('AllinCore/lang/zh.yml')
      expect(opts).not.toContain('Residence/config.yml')
    })
  })

  it('点选候选后 path 被填入并可触发', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)
    await waitFor(() => expect(vi.mocked(effectiveFiles)).toHaveBeenCalled())

    const input = screen.getByLabelText('目标文件 path（相对 plugins/）')
    await userEvent.type(input, 'Residence')
    await userEvent.click(await screen.findByRole('option', { name: 'Residence/config.yml' }))
    await userEvent.click(screen.getByRole('button', { name: '拓印此文件' }))

    await waitFor(() => {
      expect(vi.mocked(triggerImprint)).toHaveBeenCalledWith('lobby-1', 'prod', {
        path: 'Residence/config.yml',
      })
    })
  })

  it('候选列表外仍可手输自定义 path 触发（兜底未追踪文件）', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)
    await waitFor(() => expect(vi.mocked(effectiveFiles)).toHaveBeenCalled())

    const input = screen.getByLabelText('目标文件 path（相对 plugins/）')
    // 输入候选清单里没有的 path（未被 Beacon 追踪的文件）
    await userEvent.type(input, 'Untracked/secret.yml')
    await userEvent.click(screen.getByRole('button', { name: '拓印此文件' }))

    await waitFor(() => {
      expect(vi.mocked(triggerImprint)).toHaveBeenCalledWith('lobby-1', 'prod', {
        path: 'Untracked/secret.yml',
      })
    })
  })

  it('候选为空时给提示且仍可手输触发', async () => {
    vi.mocked(effectiveFiles).mockResolvedValue(fileTree([]))
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)
    await waitFor(() => expect(vi.mocked(effectiveFiles)).toHaveBeenCalled())

    // 空候选提示
    expect(
      await screen.findByText('该实例暂无已知文件，可直接手输 path'),
    ).toBeInTheDocument()

    // 仍可手输并触发
    await userEvent.type(
      screen.getByLabelText('目标文件 path（相对 plugins/）'),
      'Foo/bar.yml',
    )
    await userEvent.click(screen.getByRole('button', { name: '拓印此文件' }))
    await waitFor(() => {
      expect(vi.mocked(triggerImprint)).toHaveBeenCalledWith('lobby-1', 'prod', {
        path: 'Foo/bar.yml',
      })
    })
  })

  it('展示使用提示文案', async () => {
    renderTrigger(<ImprintTrigger instances={instances} onTriggered={() => {}} />)
    expect(
      await screen.findByText(/输入关键字即时搜该实例已知文件/),
    ).toBeInTheDocument()
  })
})
