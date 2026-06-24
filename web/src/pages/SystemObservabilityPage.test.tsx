// SystemObservabilityPage 单测（FR-82）：
// 覆盖「四组指标渲染（DB 连接池 / 长轮询挂起 / 注册表规模 / 命令队列深度）→ 按状态机顺序拼接展示
// → maxOpenConnections=0 显无限 ∞ → 待拉取命令数取 commandByStatus.pending」。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  systemObservability: vi.fn(),
}))

import SystemObservabilityPage from './SystemObservabilityPage'
import { systemObservability } from '../api/client'
import type { ObservabilityView } from '../api/types'

// 各计数取互不相同的值，避免 getByText 因重复文本歧义抛错。
const OBS: ObservabilityView = {
  dbPool: { maxOpenConnections: 20, openConnections: 5, inUse: 12, idle: 13, waitCount: 7, waitDurationMs: 250 },
  longpoll: { config: 22, file: 11, topology: 0, command: 33, total: 66 },
  registryByStatus: { online: 41, degraded: 14, lost: 23 },
  registryTotal: 78,
  commandByStatus: { pending: 51, fetched: 17, done: 91 },
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(systemObservability).mockResolvedValue(OBS)
})

describe('SystemObservabilityPage', () => {
  it('渲染四组分区标题', async () => {
    renderPage(<SystemObservabilityPage />)
    expect(await screen.findByText('数据库连接池')).toBeInTheDocument()
    expect(screen.getByText('长轮询挂起')).toBeInTheDocument()
    expect(screen.getByText('注册表规模')).toBeInTheDocument()
    expect(screen.getByText('命令队列深度')).toBeInTheDocument()
  })

  it('DB 连接池字段就位（已建 / 使用中·空闲 / 累计等待）', async () => {
    renderPage(<SystemObservabilityPage />)
    // 已建连接 = openConnections
    expect(await screen.findByText('5')).toBeInTheDocument()
    // 使用中 / 空闲 = inUse / idle
    expect(screen.getByText('12 / 13')).toBeInTheDocument()
    // 累计等待次数 = waitCount
    expect(screen.getByText('7')).toBeInTheDocument()
    // 等待时长 hint 含毫秒
    expect(screen.getByText('累计等待时长 250 ms')).toBeInTheDocument()
  })

  it('长轮询合计与各通道拼接展示', async () => {
    renderPage(<SystemObservabilityPage />)
    // 合计
    expect(await screen.findByText('66')).toBeInTheDocument()
    // 各通道 config/file/topology/command
    expect(screen.getByText('22 / 11 / 0 / 33')).toBeInTheDocument()
  })

  it('注册表规模：总数 + 按状态机顺序仅列有计数的状态', async () => {
    renderPage(<SystemObservabilityPage />)
    // 总数
    expect(await screen.findByText('78')).toBeInTheDocument()
    // 按状态：online 41 · degraded 14 · lost 23（offline 无计数不出现）
    expect(screen.getByText('在线 41 · 亚健康 14 · 失联 23')).toBeInTheDocument()
  })

  it('命令队列：待拉取取 pending，按状态拼接有计数项', async () => {
    renderPage(<SystemObservabilityPage />)
    // 待拉取卡片值 = commandByStatus.pending
    expect(await screen.findByText('51')).toBeInTheDocument()
    // 按状态：待拉取 51 · 执行中 17 · 已完成 91（与状态机顺序一致）
    expect(screen.getByText('待拉取 51 · 执行中 17 · 已完成 91')).toBeInTheDocument()
  })

  it('maxOpenConnections=0 时上限显示无限 ∞', async () => {
    vi.mocked(systemObservability).mockResolvedValue({
      ...OBS,
      dbPool: { ...OBS.dbPool, maxOpenConnections: 0 },
    })
    renderPage(<SystemObservabilityPage />)
    expect(await screen.findByText('上限 ∞')).toBeInTheDocument()
  })
})
