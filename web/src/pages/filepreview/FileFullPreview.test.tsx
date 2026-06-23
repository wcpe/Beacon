// FileFullPreview 测试（FR-68 文件树全量预览 + 追踪态）：
// 触发全量预览 → createScanTask → 轮询 getReverseFetchTask 至 pending-review → effectiveFiles 取追踪 path 集 →
// 交叉比对：全量清单逐文件列出、在有效树的标「追踪」、不在的标「未追踪」；
// 追踪文件可点开看合并结果（逐键来源）；读完清单调 cancelReverseFetchTask（预览只读不 ingest）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { InstanceView, ReverseFetchScanFileView, ReverseFetchTaskView } from '../../api/types'
import type { EffectiveFileTreeView } from '../../api/client'

vi.mock('../../api/client', () => ({
  createScanTask: vi.fn(),
  getReverseFetchTask: vi.fn(),
  cancelReverseFetchTask: vi.fn(),
  effectiveFiles: vi.fn(),
}))

vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import FileFullPreview from './FileFullPreview'
import {
  createScanTask,
  getReverseFetchTask,
  cancelReverseFetchTask,
  effectiveFiles,
} from '../../api/client'

// 在线 bukkit 源实例（可作扫描源）
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

function scanFile(path: string, size: number): ReverseFetchScanFileView {
  return { path, size, isText: true, overThreshold: false, ignoredByRule: false }
}

// pending-review 任务：files 为全量磁盘清单
function task(files: ReverseFetchScanFileView[], status = 'pending-review'): ReverseFetchTaskView {
  return {
    id: 77,
    namespace: 'prod',
    serverId: 'lobby-1',
    scope: 'server',
    group: 'area1',
    target: 'lobby-1',
    status,
    scanCommandId: 1,
    submitCommandId: 0,
    totalFiles: files.length,
    selectedCount: 0,
    overThresholdCount: 0,
    skippedCount: 0,
    files,
    selectedPaths: [],
    operator: 'admin',
    note: '',
    createdAt: '',
    updatedAt: '',
  }
}

// 有效树：只含 AllinCore/config.yml（追踪），ServerProbe/runtime.yml 不在其中（未追踪）
const EFFECTIVE: EffectiveFileTreeView = {
  namespace: 'prod',
  serverId: 'lobby-1',
  group: 'area1',
  zone: null,
  fileTreeMd5: 'abcd1234deadbeef',
  files: [
    {
      path: 'AllinCore/config.yml',
      md5: 'deadbeef',
      content: 'a: 1\nb: 2\n',
      wholeFile: false,
      sources: [{ path: ['a'], scope: 'global' }],
      deletions: [],
    },
  ],
}

function renderPreview(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(createScanTask).mockResolvedValue(task([], 'scanning'))
  vi.mocked(getReverseFetchTask).mockResolvedValue(
    task([scanFile('AllinCore/config.yml', 1024), scanFile('ServerProbe/runtime.yml', 512)]),
  )
  vi.mocked(cancelReverseFetchTask).mockResolvedValue(task([], 'cancelled'))
  vi.mocked(effectiveFiles).mockResolvedValue(EFFECTIVE)
})

describe('FileFullPreview', () => {
  it('触发全量预览：createScanTask → 轮询至 pending-review → effectiveFiles 比对，全量列出追踪/未追踪', async () => {
    renderPreview(<FileFullPreview instances={[inst('lobby-1', 'prod', 'area1')]} />)

    await userEvent.click(screen.getByRole('button', { name: '全量预览（含未追踪）' }))

    // createScanTask 以 server 层 + 源 serverId 调用
    await waitFor(() => {
      expect(vi.mocked(createScanTask)).toHaveBeenCalledWith('lobby-1', 'prod', {
        scope: 'server',
        group: 'area1',
        target: 'lobby-1',
      })
    })

    // 全量清单两文件均列出
    expect(await screen.findByText('AllinCore/config.yml')).toBeInTheDocument()
    expect(screen.getByText('ServerProbe/runtime.yml')).toBeInTheDocument()

    // 追踪 / 未追踪分类正确
    const tracked = screen.getByText('AllinCore/config.yml').closest('li') as HTMLElement
    const untracked = screen.getByText('ServerProbe/runtime.yml').closest('li') as HTMLElement
    expect(within(tracked).getByText('追踪')).toBeInTheDocument()
    expect(within(untracked).getByText('未追踪')).toBeInTheDocument()
  })

  it('读完清单调 cancelReverseFetchTask（预览只读、不 ingest）', async () => {
    renderPreview(<FileFullPreview instances={[inst('lobby-1', 'prod', 'area1')]} />)
    await userEvent.click(screen.getByRole('button', { name: '全量预览（含未追踪）' }))
    await screen.findByText('AllinCore/config.yml')
    await waitFor(() => {
      expect(vi.mocked(cancelReverseFetchTask)).toHaveBeenCalledWith(77)
    })
  })

  it('追踪文件可点开看合并结果（逐键来源）；未追踪文件无合并可看', async () => {
    renderPreview(<FileFullPreview instances={[inst('lobby-1', 'prod', 'area1')]} />)
    await userEvent.click(screen.getByRole('button', { name: '全量预览（含未追踪）' }))
    const tracked = (await screen.findByText('AllinCore/config.yml')).closest('li') as HTMLElement

    // 点开追踪文件 → 展示合并结果与逐键来源
    await userEvent.click(within(tracked).getByRole('button', { name: /查看合并/ }))
    // 逐键来源徽标（合并卡片渲染才有）：a 键来自 global 层
    expect(await screen.findByText(/a.*global/)).toBeInTheDocument()
    // 合并后内容（<pre> 内多行，getByText 归一化空白，用片段匹配）
    expect(within(tracked).getByText(/a: 1/)).toBeInTheDocument()

    // 未追踪文件没有「查看合并」入口
    const untracked = screen.getByText('ServerProbe/runtime.yml').closest('li') as HTMLElement
    expect(within(untracked).queryByRole('button', { name: /查看合并/ })).not.toBeInTheDocument()
  })

  it('未追踪文件附「去反向抓取纳管」链接跳 /reverse-fetch', async () => {
    renderPreview(<FileFullPreview instances={[inst('lobby-1', 'prod', 'area1')]} />)
    await userEvent.click(screen.getByRole('button', { name: '全量预览（含未追踪）' }))
    const untracked = (await screen.findByText('ServerProbe/runtime.yml')).closest('li') as HTMLElement
    const link = within(untracked).getByRole('link', { name: /反向抓取/ })
    expect(link).toHaveAttribute('href', '/reverse-fetch')
  })
})
