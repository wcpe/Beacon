// IngestReviewOverlay 单测（FR-115）：反向抓取 ingest 审核浮层。
// mock useIngestScanList 注入扫描清单，覆盖：loading 骨架、标题/队列名、
// 按 defaultPick 初始化勾选与计数、全选/全不选切换、忽略项标记、确认钮按选数禁用/放行并回传 count、取消回调。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

import IngestReviewOverlay from './IngestReviewOverlay'
import { useIngestScanList } from './useWorkbenchData'
import type { IngestScanItem } from '@/api/mock/workbench'

vi.mock('./useWorkbenchData', () => ({
  useIngestScanList: vi.fn(),
}))

const mockedHook = vi.mocked(useIngestScanList)

const ITEMS: IngestScanItem[] = [
  { path: 'Essentials/config.yml', size: '12.4 KB', ignored: false, defaultPick: true },
  { path: 'WorldGuard/regions.yml', size: '88.0 KB', ignored: false, defaultPick: true },
  { path: 'cache.db', size: '4.0 MB', ignored: true, defaultPick: false },
]
const RULES = ['userdata/**', '*.db']

function mockScan(over: Partial<ReturnType<typeof useIngestScanList>>) {
  mockedHook.mockReturnValue({ data: undefined, isLoading: false, ...over } as ReturnType<typeof useIngestScanList>)
}

describe('IngestReviewOverlay（FR-115）', () => {
  it('loading：显骨架，无清单项', () => {
    mockScan({ isLoading: true })
    render(<IngestReviewOverlay queueName="regions.yml" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.queryByText('Essentials/config.yml')).not.toBeInTheDocument()
  })

  it('标题与队列名呈现', () => {
    mockScan({ data: { items: ITEMS, ignoreRules: RULES } })
    // 用不在清单项里的队列名，避免与列表项文本撞车
    render(<IngestReviewOverlay queueName="some-queue-item" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('反向抓取 · 审核纳管清单')).toBeInTheDocument()
    expect(screen.getByText('some-queue-item')).toBeInTheDocument()
  })

  it('按 defaultPick 初始化勾选 + 计数（2/3）；忽略项打标', () => {
    mockScan({ data: { items: ITEMS, ignoreRules: RULES } })
    render(<IngestReviewOverlay queueName="x" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    // 默认两条 defaultPick=true → 已选 2 / 3
    expect(screen.getByText('已选 2 / 3 项')).toBeInTheDocument()
    expect(screen.getByText('已忽略')).toBeInTheDocument()
    // 确认钮文案带当前选数
    expect(screen.getByRole('button', { name: '确认纳管 2 项' })).toBeInTheDocument()
  })

  it('全选：把 3 项全勾，计数变 3/3', async () => {
    mockScan({ data: { items: ITEMS, ignoreRules: RULES } })
    render(<IngestReviewOverlay queueName="x" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    await userEvent.click(screen.getByLabelText('全选'))
    expect(screen.getByText('已选 3 / 3 项')).toBeInTheDocument()
  })

  it('确认：回传当前选数', async () => {
    mockScan({ data: { items: ITEMS, ignoreRules: RULES } })
    const onConfirm = vi.fn()
    render(<IngestReviewOverlay queueName="x" onConfirm={onConfirm} onCancel={vi.fn()} />)
    await userEvent.click(screen.getByRole('button', { name: '确认纳管 2 项' }))
    expect(onConfirm).toHaveBeenCalledWith(2)
  })

  it('全不选后确认钮禁用', async () => {
    mockScan({ data: { items: ITEMS, ignoreRules: RULES } })
    render(<IngestReviewOverlay queueName="x" onConfirm={vi.fn()} onCancel={vi.fn()} />)
    // 当前非全选 → 点全选先到全选；再点一次全选切到全不选
    const selectAll = screen.getByLabelText('全选')
    await userEvent.click(selectAll) // → 3/3 全选
    await userEvent.click(selectAll) // → 0/3 全不选
    expect(screen.getByText('已选 0 / 3 项')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '确认纳管 0 项' })).toBeDisabled()
  })

  it('取消触发 onCancel', async () => {
    mockScan({ data: { items: ITEMS, ignoreRules: RULES } })
    const onCancel = vi.fn()
    render(<IngestReviewOverlay queueName="x" onConfirm={vi.fn()} onCancel={onCancel} />)
    // 头部 X 与底部按钮的可达名都是「取消」，取最后一个（底部）
    const cancels = screen.getAllByRole('button', { name: '取消' })
    await userEvent.click(cancels[cancels.length - 1])
    expect(onCancel).toHaveBeenCalled()
  })
})
