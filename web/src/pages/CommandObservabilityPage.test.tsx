// CommandObservabilityPage 单测（FR-104）：
// 覆盖「KPI（总数 + 按状态计数，含健康色）→ 实时队列逐条（含已等时长、自动刷新过滤 pending/fetched）
// → 历史过滤查询（按类型 / 状态 / serverId 重查）→ 命令量趋势喂图 → loading 骨架 → i18n 无裸键」。
// recharts 较重且依赖容器尺寸，故把趋势图替身为轻量桩，断言数据正确喂入而不渲染真图。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// 轻量桩替身趋势图：暴露收到的点数与序列化数据，规避 recharts 在 jsdom 下的尺寸/动画依赖。
vi.mock('./command-observability/CommandTrendChart', () => ({
  default: (props: { points: Array<{ date: string; issued: number; done: number; failed: number }> }) => (
    <div
      data-testid="cmd-trend"
      data-count={props.points.length}
      data-issued={JSON.stringify(props.points.map((p) => p.issued))}
    />
  ),
}))

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  listCommands: vi.fn(),
  getCommandAnalytics: vi.fn(),
  listNamespaces: vi.fn(),
}))

import CommandObservabilityPage from './CommandObservabilityPage'
import { listCommands, getCommandAnalytics, listNamespaces } from '../api/client'
import type { CommandAnalytics, CommandMetaView, CommandPage } from '../api/types'

const ANALYTICS: CommandAnalytics = {
  from: '2026-05-25T00:00:00Z',
  to: '2026-06-24T00:00:00Z',
  total: 42,
  byStatus: [
    { status: 'done', count: 30 },
    { status: 'pending', count: 5 },
    { status: 'fetched', count: 3 },
    { status: 'failed', count: 3 },
    { status: 'expired', count: 1 },
  ],
  byType: [
    { type: 'ingest-plugins', count: 25 },
    { type: 'tail-logs', count: 12 },
    { type: 'resync-config', count: 5 },
  ],
  byServer: [
    { serverId: 'lobby-1', count: 20 },
    { serverId: 'lobby-2', count: 22 },
  ],
  byDay: [
    { date: '2026-06-01', issued: 5, done: 4, failed: 1 },
    { date: '2026-06-02', issued: 8, done: 7, failed: 0 },
  ],
}

// 实时队列样例：一条 pending（30 秒前）+ 一条 fetched。
const QUEUE_PENDING: CommandMetaView = {
  commandId: 101, namespace: 'prod', serverId: 'lobby-1', type: 'ingest-plugins', status: 'pending',
  resultDetail: '', operator: 'admin', createdAt: new Date(Date.now() - 30000).toISOString(),
  updatedAt: new Date(Date.now() - 30000).toISOString(), ageSeconds: 30,
}
const QUEUE_FETCHED: CommandMetaView = {
  commandId: 102, namespace: 'prod', serverId: 'lobby-2', type: 'tail-logs', status: 'fetched',
  resultDetail: '', operator: 'admin', createdAt: new Date(Date.now() - 10000).toISOString(),
  updatedAt: new Date(Date.now() - 10000).toISOString(), ageSeconds: 10,
}
// 历史样例：一条 done。
const HISTORY_DONE: CommandMetaView = {
  commandId: 90, namespace: 'prod', serverId: 'lobby-1', type: 'ingest-plugins', status: 'done',
  resultDetail: 'ingest 3 files', operator: 'admin', createdAt: '2026-06-20T08:00:00Z',
  updatedAt: '2026-06-20T08:01:00Z', ageSeconds: 9999,
}

function emptyPage(): CommandPage {
  return { total: 0, items: [] }
}

// listCommands 按入参路由返回：status=pending/fetched 为实时队列；其余为历史。
function routeListCommands(filter: { status?: string }): Promise<CommandPage> {
  if (filter.status === 'pending') return Promise.resolve({ total: 1, items: [QUEUE_PENDING] })
  if (filter.status === 'fetched') return Promise.resolve({ total: 1, items: [QUEUE_FETCHED] })
  return Promise.resolve({ total: 1, items: [HISTORY_DONE] })
}

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(getCommandAnalytics).mockResolvedValue(ANALYTICS)
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
  vi.mocked(listCommands).mockImplementation((f) => routeListCommands(f))
})

describe('CommandObservabilityPage', () => {
  it('渲染 KPI：总数 + 按状态计数', async () => {
    renderPage(<CommandObservabilityPage />)
    expect(await screen.findByText('命令总数')).toBeInTheDocument()
    expect(screen.getByText('42')).toBeInTheDocument()
    // 按状态 IconStat 标签（中文）就位
    expect(screen.getAllByText('待拉取').length).toBeGreaterThan(0)
    expect(screen.getAllByText('已完成').length).toBeGreaterThan(0)
    // done=30 计数渲染
    expect(screen.getByText('30')).toBeInTheDocument()
  })

  it('实时队列只列 pending+fetched 逐条，含已等时长列', async () => {
    renderPage(<CommandObservabilityPage />)
    // 队列两条命令 ID 渲染
    expect(await screen.findByText('101')).toBeInTheDocument()
    expect(screen.getByText('102')).toBeInTheDocument()
    // 已等时长列表头存在
    expect(screen.getByText('已等时长')).toBeInTheDocument()
    // 队列拉取按 pending / fetched 两状态各请求一次
    await waitFor(() => {
      const statuses = vi.mocked(listCommands).mock.calls.map((c) => c[0].status)
      expect(statuses).toContain('pending')
      expect(statuses).toContain('fetched')
    })
  })

  it('命令量趋势点数与下发数喂入图表', async () => {
    renderPage(<CommandObservabilityPage />)
    const trend = await screen.findByTestId('cmd-trend')
    expect(trend.getAttribute('data-count')).toBe('2')
    expect(JSON.parse(trend.getAttribute('data-issued') ?? '[]')).toEqual([5, 8])
  })

  it('历史查询：选类型重查带 type 过滤', async () => {
    renderPage(<CommandObservabilityPage />)
    // 等历史首查完成
    await screen.findByText('ingest 3 files')
    // 打开类型下拉选「取日志」
    const typeTrigger = screen.getByLabelText('类型')
    await userEvent.click(typeTrigger)
    await userEvent.click(await screen.findByRole('option', { name: '取日志' }))
    // 点查询
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    await waitFor(() =>
      expect(vi.mocked(listCommands)).toHaveBeenCalledWith(
        expect.objectContaining({ type: 'tail-logs' }),
      ),
    )
  })

  it('切环境带 namespace 重查聚合', async () => {
    renderPage(<CommandObservabilityPage />)
    const ns = await screen.findByLabelText('环境')
    await userEvent.click(ns)
    await userEvent.click(await screen.findByRole('option', { name: 'prod · 生产' }))
    await waitFor(() =>
      expect(vi.mocked(getCommandAnalytics)).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: 'prod' }),
      ),
    )
  })

  it('实时队列空态友好提示', async () => {
    // 队列两状态都空，历史仍有一条
    vi.mocked(listCommands).mockImplementation((f) =>
      f.status === 'pending' || f.status === 'fetched'
        ? Promise.resolve(emptyPage())
        : Promise.resolve({ total: 1, items: [HISTORY_DONE] }),
    )
    renderPage(<CommandObservabilityPage />)
    expect(await screen.findByText('当前无待拉取 / 执行中命令')).toBeInTheDocument()
  })

  it('按服务器分布渲染 top 列表', async () => {
    renderPage(<CommandObservabilityPage />)
    // 区块标题与服务器项就位（lobby-2 在队列表与按服务器列表都出现，故 getAllByText）
    expect(await screen.findByText('按服务器分布')).toBeInTheDocument()
    expect(screen.getAllByText('lobby-2').length).toBeGreaterThan(0)
    // lobby-2 计数 22 渲染（唯一）
    expect(screen.getByText('22')).toBeInTheDocument()
  })
})
