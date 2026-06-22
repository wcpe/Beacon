// ImprintDiffPanel 关键路径测试（FR-46）：
// 拉到 diff 后 → 默认子服层 → 点确认同步 → confirmImprint 必带 diff 返回的 actualMd5 作 reviewedMd5（自审门）；
// 切并入层为大区层后确认 → 入参带 group、不带 target；diff 展示期望合并值来源徽标。
// api/client 与 CodeEditor 被 mock，保证用例在 jsdom 下稳定可跑。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { ImprintDiffView, InstanceView } from '../../api/types'

// mock Monaco DiffEditor，避免 jsdom 下加载真实编辑器
vi.mock('../../components/CodeEditor', () => ({
  default: (props: { original?: string; modified?: string }) => (
    <div data-testid="diff-editor" data-original={props.original} data-modified={props.modified} />
  ),
}))

// mock 后端调用，由用例断言
vi.mock('../../api/client', () => ({
  imprintDiff: vi.fn(),
  confirmImprint: vi.fn(),
}))

// mock 全局消息提示，避免 toast 依赖
vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import ImprintDiffPanel from './ImprintDiffPanel'
import { imprintDiff, confirmImprint } from '../../api/client'

function inst(serverId: string, group: string): InstanceView {
  return {
    namespace: 'prod',
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

const DIFF: ImprintDiffView = {
  path: 'AllinCore/config.yml',
  actualContent: 'a: 99\n',
  actualMd5: 'actual-md5-xyz',
  expectedContent: 'a: 1\n',
  expectedMd5: 'expected-md5-abc',
  expectedWholeFile: false,
  expectedSources: [{ path: ['a'], scope: 'group' }],
  expectedDeletions: [],
  differs: true,
}

function renderPanel(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(imprintDiff).mockResolvedValue(DIFF)
  vi.mocked(confirmImprint).mockResolvedValue({
    fileId: 1,
    scopeLevel: 'server',
    group: 'area1',
    target: 'lobby-1',
    version: 1,
    md5: 'actual-md5-xyz',
  })
})

describe('ImprintDiffPanel', () => {
  it('确认同步必带 diff 返回的 actualMd5 作 reviewedMd5（单人自审门）', async () => {
    renderPanel(
      <ImprintDiffPanel
        commandId={7}
        serverId="lobby-1"
        sourceGroup="area1"
        groups={['area1', 'area2']}
        instances={[inst('lobby-1', 'area1')]}
        onConfirmed={() => {}}
      />,
    )

    // 等 diff 加载完成（来源徽标出现）
    await screen.findByTestId('diff-editor')
    await screen.findByText('有差异')

    // 默认子服层 → 确认同步
    await userEvent.click(screen.getByRole('button', { name: '确认同步' }))

    await waitFor(() => {
      // reviewedMd5 必须是 diff 返回的 actualMd5；server 层带 target、不带 zone
      expect(vi.mocked(confirmImprint)).toHaveBeenCalledWith(7, {
        scope: 'server',
        group: 'area1',
        zone: undefined,
        target: 'lobby-1',
        reviewedMd5: 'actual-md5-xyz',
      })
    })
  })

  it('切并入层为大区层后确认，入参带 group、不带 target/zone', async () => {
    renderPanel(
      <ImprintDiffPanel
        commandId={9}
        serverId="lobby-1"
        sourceGroup="area1"
        groups={['area1', 'area2']}
        instances={[inst('lobby-1', 'area1')]}
        onConfirmed={() => {}}
      />,
    )
    await screen.findByTestId('diff-editor')

    // 并入层切为大区层
    await userEvent.selectOptions(screen.getByLabelText('并入层'), 'group')
    await userEvent.click(screen.getByRole('button', { name: '确认同步' }))

    await waitFor(() => {
      expect(vi.mocked(confirmImprint)).toHaveBeenCalledWith(9, {
        scope: 'group',
        group: 'area1',
        zone: undefined,
        target: undefined,
        reviewedMd5: 'actual-md5-xyz',
      })
    })
  })

  it('展示期望合并值逐键来源徽标（复用 FR-45 provenance）', async () => {
    renderPanel(
      <ImprintDiffPanel
        commandId={11}
        serverId="lobby-1"
        sourceGroup="area1"
        groups={['area1']}
        instances={[inst('lobby-1', 'area1')]}
        onConfirmed={() => {}}
      />,
    )
    await screen.findByTestId('diff-editor')
    // 期望合并值来源：a (group)
    expect(await screen.findByText('a (group)')).toBeInTheDocument()
  })
})
