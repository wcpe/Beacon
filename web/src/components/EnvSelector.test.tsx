// EnvSelector 单测（FR-105）：含「全部环境」选项；选某环境写入全局 store + 持久化 localStorage。
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端环境列表
vi.mock('@/api/client', () => ({
  listNamespaces: vi.fn(),
}))

import EnvSelector from './EnvSelector'
import { listNamespaces } from '@/api/client'
import { currentEnvironment, setEnvironment } from '@/state/environment'

function renderUI(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  localStorage.clear()
  // 复位全局环境到「全部」，避免跨用例残留
  setEnvironment('')
  vi.mocked(listNamespaces).mockReset()
  vi.mocked(listNamespaces).mockResolvedValue([
    { code: 'prod', name: '生产' },
    { code: 'test', name: '测试' },
  ])
})

afterEach(() => {
  vi.restoreAllMocks()
})

describe('EnvSelector', () => {
  it('下拉含「全部环境」选项与各环境候选', async () => {
    renderUI(<EnvSelector />)
    // 打开下拉（全局环境选择器 aria-label＝「全局环境」，与各页内部「环境」筛选区分）
    await userEvent.click(screen.getByLabelText('全局环境'))
    expect(await screen.findByRole('option', { name: '全部环境' })).toBeInTheDocument()
    expect(await screen.findByRole('option', { name: 'prod · 生产' })).toBeInTheDocument()
    expect(await screen.findByRole('option', { name: 'test · 测试' })).toBeInTheDocument()
  })

  it('选某环境写入全局 store 并持久化 localStorage', async () => {
    renderUI(<EnvSelector />)
    await userEvent.click(screen.getByLabelText('全局环境'))
    // 候选显示「编码 · 名称」（FR-70），选中后按 code 写入 store
    await userEvent.click(await screen.findByRole('option', { name: 'prod · 生产' }))
    await waitFor(() => expect(currentEnvironment()).toBe('prod'))
    expect(localStorage.getItem('beacon.environment')).toBe('prod')
  })

  it('选「全部环境」写入空串', async () => {
    setEnvironment('prod')
    renderUI(<EnvSelector />)
    await userEvent.click(screen.getByLabelText('全局环境'))
    await userEvent.click(await screen.findByRole('option', { name: '全部环境' }))
    await waitFor(() => expect(currentEnvironment()).toBe(''))
    expect(localStorage.getItem('beacon.environment')).toBe('')
  })
})
