// SystemObservabilityPage 单测（FR-82，ADR-0048 恢复独立页 + 补详细明细）：
// 覆盖「页标题 + 四组分区标题渲染 → DB 连接池逐项明细（含 inUse/idle 分项 + 等待时长 ms）
// → 长轮询四通道逐项 → 注册表总数 + 按状态逐项（含 0 计数状态）→ 命令队列按状态逐项
// → maxOpenConnections=0 显无限 ∞」。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
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

// 定位某分区卡片（按分区标题文本上溯到卡片容器）。
function sectionOf(title: string): HTMLElement {
  const heading = screen.getByText(title)
  const card = heading.closest('[data-slot="card"]')
  if (!card) throw new Error(`找不到分区卡片：${title}`)
  return card as HTMLElement
}

beforeEach(() => {
  vi.mocked(systemObservability).mockResolvedValue(OBS)
})

describe('SystemObservabilityPage（FR-82）', () => {
  it('渲染页标题与四组分区标题', async () => {
    renderPage(<SystemObservabilityPage />)
    expect(await screen.findByRole('heading', { name: '控制面健康' })).toBeInTheDocument()
    expect(screen.getByText('数据库连接池')).toBeInTheDocument()
    expect(screen.getByText('长轮询挂起')).toBeInTheDocument()
    expect(screen.getByText('注册表规模')).toBeInTheDocument()
    expect(screen.getByText('命令队列深度')).toBeInTheDocument()
  })

  it('DB 连接池逐项明细就位（已建 / 上限 / 使用中 / 空闲 / 累计等待 / 等待时长）', async () => {
    renderPage(<SystemObservabilityPage />)
    await screen.findByText('数据库连接池')
    const card = sectionOf('数据库连接池')
    expect(within(card).getByText('已建连接')).toBeInTheDocument()
    expect(within(card).getByText('5')).toBeInTheDocument()
    // inUse / idle 分项独立呈现（不再 '12 / 13' 合并）
    expect(within(card).getByText('使用中')).toBeInTheDocument()
    expect(within(card).getByText('12')).toBeInTheDocument()
    expect(within(card).getByText('空闲')).toBeInTheDocument()
    expect(within(card).getByText('13')).toBeInTheDocument()
    // 累计等待次数 + 等待时长（ms）
    expect(within(card).getByText('累计等待次数')).toBeInTheDocument()
    expect(within(card).getByText('7')).toBeInTheDocument()
    expect(within(card).getByText('250 ms')).toBeInTheDocument()
  })

  it('长轮询四通道逐项 + 合计', async () => {
    renderPage(<SystemObservabilityPage />)
    await screen.findByText('长轮询挂起')
    const card = sectionOf('长轮询挂起')
    expect(within(card).getByText('挂起合计')).toBeInTheDocument()
    expect(within(card).getByText('66')).toBeInTheDocument()
    expect(within(card).getByText('配置通道')).toBeInTheDocument()
    expect(within(card).getByText('22')).toBeInTheDocument()
    expect(within(card).getByText('文件通道')).toBeInTheDocument()
    expect(within(card).getByText('11')).toBeInTheDocument()
    expect(within(card).getByText('拓扑通道')).toBeInTheDocument()
    expect(within(card).getByText('命令通道')).toBeInTheDocument()
    expect(within(card).getByText('33')).toBeInTheDocument()
  })

  it('注册表规模：总数 + 按状态机顺序逐项（缺省状态显 0）', async () => {
    renderPage(<SystemObservabilityPage />)
    await screen.findByText('注册表规模')
    const card = sectionOf('注册表规模')
    expect(within(card).getByText('实例总数')).toBeInTheDocument()
    expect(within(card).getByText('78')).toBeInTheDocument()
    expect(within(card).getByText('在线')).toBeInTheDocument()
    expect(within(card).getByText('41')).toBeInTheDocument()
    expect(within(card).getByText('亚健康')).toBeInTheDocument()
    expect(within(card).getByText('14')).toBeInTheDocument()
    expect(within(card).getByText('失联')).toBeInTheDocument()
    expect(within(card).getByText('23')).toBeInTheDocument()
    // offline 无计数 → 显 0（逐项全列，不再省略）
    expect(within(card).getByText('离线')).toBeInTheDocument()
    expect(within(card).getByText('0')).toBeInTheDocument()
  })

  it('命令队列：按状态机顺序逐项（缺省状态显 0）', async () => {
    renderPage(<SystemObservabilityPage />)
    await screen.findByText('命令队列深度')
    const card = sectionOf('命令队列深度')
    expect(within(card).getByText('待拉取')).toBeInTheDocument()
    expect(within(card).getByText('51')).toBeInTheDocument()
    expect(within(card).getByText('执行中')).toBeInTheDocument()
    expect(within(card).getByText('17')).toBeInTheDocument()
    expect(within(card).getByText('已完成')).toBeInTheDocument()
    expect(within(card).getByText('91')).toBeInTheDocument()
    // ready / failed / expired 无计数 → 显 0
    expect(within(card).getByText('待审')).toBeInTheDocument()
  })

  it('maxOpenConnections=0 时上限显示无限 ∞', async () => {
    vi.mocked(systemObservability).mockResolvedValue({
      ...OBS,
      dbPool: { ...OBS.dbPool, maxOpenConnections: 0 },
    })
    renderPage(<SystemObservabilityPage />)
    await screen.findByText('数据库连接池')
    const card = sectionOf('数据库连接池')
    expect(within(card).getByText('∞')).toBeInTheDocument()
  })
})
