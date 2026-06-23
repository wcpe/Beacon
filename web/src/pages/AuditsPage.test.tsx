// AuditsPage 过滤冒烟测试：验证「操作人」过滤输入存在，且点查询时把 operator 透传给 listAudits。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  listAudits: vi.fn(),
  // FR-51：环境筛选下拉的候选来源
  listNamespaces: vi.fn(),
}))

import AuditsPage from './AuditsPage'
import { listAudits, listNamespaces } from '../api/client'

const EMPTY_PAGE = { total: 0, items: [] }

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(listAudits).mockResolvedValue(EMPTY_PAGE)
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
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

  // FR-70：环境下拉显示「编码 · 名称」，但选中并查询时提交的过滤值仍是 code。
  it('环境下拉显示「编码 · 名称」而过滤值仍为 code', async () => {
    renderPage(<AuditsPage />)
    const ns = await screen.findByLabelText('环境')
    await userEvent.click(ns)
    // 候选展示编码 · 名称
    const opt = await screen.findByRole('option', { name: 'prod · 生产' })
    await userEvent.click(opt)
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    // 提交给后端的 namespace 仍是纯 code，不含 name
    await waitFor(() =>
      expect(vi.mocked(listAudits)).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: 'prod' }),
      ),
    )
  })
})

// FR-50：审计 action 英文枚举经 i18n 映射成中文展示；未知枚举回退原文。
describe('AuditsPage 审计动作 i18n 映射', () => {
  // 一条已知动作 + 一条未知动作的审计样例
  const auditRow = (action: string) => ({
    createdAt: '2026-01-01T00:00:00Z',
    namespace: 'prod',
    operator: 'admin',
    action,
    targetType: 'config',
    targetRef: 'app.yml',
    result: 'ok' as const,
    clientIp: '127.0.0.1',
    detail: '',
  })

  it('已知动作 config.publish 显示中文「发布配置」', async () => {
    vi.mocked(listAudits).mockResolvedValue({ total: 1, items: [auditRow('config.publish')] })
    renderPage(<AuditsPage />)
    expect(await screen.findByText('发布配置')).toBeInTheDocument()
    // 不出现裸枚举
    expect(screen.queryByText('config.publish')).not.toBeInTheDocument()
  })

  it('未知动作回退展示原文英文枚举', async () => {
    vi.mocked(listAudits).mockResolvedValue({ total: 1, items: [auditRow('custom.unknown-action')] })
    renderPage(<AuditsPage />)
    expect(await screen.findByText('custom.unknown-action')).toBeInTheDocument()
  })
})
