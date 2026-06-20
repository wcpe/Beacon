// AuditsPage 过滤冒烟测试：验证「操作人」过滤输入存在，且点查询时把 operator 透传给 listAudits。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  listAudits: vi.fn(),
}))

import AuditsPage from './AuditsPage'
import { listAudits } from '../api/client'

const EMPTY_PAGE = { total: 0, items: [] }

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(listAudits).mockResolvedValue(EMPTY_PAGE)
})

describe('AuditsPage 操作人过滤', () => {
  it('渲染操作人过滤输入框', async () => {
    renderPage(<AuditsPage />)
    expect(await screen.findByLabelText('操作人')).toBeInTheDocument()
  })

  it('输入操作人并查询时把 operator 透传给 listAudits', async () => {
    renderPage(<AuditsPage />)
    const input = await screen.findByLabelText('操作人')
    await userEvent.type(input, 'admin')
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    await waitFor(() =>
      expect(vi.mocked(listAudits)).toHaveBeenCalledWith(
        expect.objectContaining({ operator: 'admin' }),
      ),
    )
  })
})
