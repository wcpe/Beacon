// AuditsPage 过滤冒烟测试：验证「操作人」过滤输入存在，且点查询时把 operator 透传给 listAudits。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ReactElement } from 'react'
import { renderWithPageHeader } from '../test/renderWithPageHeader'

// mock 后端调用，由各用例注入数据
vi.mock('../api/client', () => ({
  listAudits: vi.fn(),
  // FR-51：环境筛选下拉的候选来源
  listNamespaces: vi.fn(),
  // FR-84：导出审计
  exportAudits: vi.fn(),
}))

import AuditsPage from './AuditsPage'
import { listAudits, listNamespaces, exportAudits } from '../api/client'
import { setEnvironment } from '@/state/environment'

const EMPTY_PAGE = { total: 0, items: [] }

// 标题与导出主操作已迁入第二层页眉 PageHeader（FR-105），故连同 PageHeader 在 /audits 路由下渲染。
function renderPage(ui: ReactElement) {
  return renderWithPageHeader(ui, { path: '/audits' })
}

beforeEach(() => {
  // 复位全局环境到「全部」，避免跨用例残留（FR-105 真机打磨：环境收口至页眉全局环境）
  setEnvironment('')
  vi.mocked(listAudits).mockResolvedValue(EMPTY_PAGE)
  vi.mocked(listNamespaces).mockResolvedValue([{ code: 'prod', name: '生产' }])
  vi.mocked(exportAudits).mockResolvedValue(undefined)
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

  // FR-105 真机打磨：环境收口至页眉全局环境槽（页内不再有环境筛选）。
  // 选页眉全局环境后，列表查询应带该 namespace（候选显「编码 · 名称」，提交仍是 code，FR-70）。
  it('选页眉全局环境后查询带该 namespace（值仍为 code）', async () => {
    renderPage(<AuditsPage />)
    // 全局环境选择器在第二层页眉，aria-label＝「全局环境」
    const ns = await screen.findByLabelText('全局环境')
    await userEvent.click(ns)
    const opt = await screen.findByRole('option', { name: 'prod · 生产' })
    await userEvent.click(opt)
    // 全局环境变化即驱动重查（无需点「查询」）：提交的 namespace 为纯 code
    await waitFor(() =>
      expect(vi.mocked(listAudits)).toHaveBeenCalledWith(
        expect.objectContaining({ namespace: 'prod' }),
      ),
    )
  })
})

// FR-84：detail 关键字检索 + 导出。
describe('AuditsPage detail 关键字检索与导出', () => {
  it('输入 detail 关键字并查询时把 detailKeyword 透传给 listAudits', async () => {
    renderPage(<AuditsPage />)
    const input = await screen.findByLabelText('详情关键字')
    await userEvent.type(input, 'mysql')
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    await waitFor(() =>
      expect(vi.mocked(listAudits)).toHaveBeenCalledWith(
        expect.objectContaining({ detailKeyword: 'mysql' }),
      ),
    )
  })

  it('点导出 CSV 时按当前过滤调 exportAudits(csv)', async () => {
    renderPage(<AuditsPage />)
    // 先设一个 detail 关键字并查询，使其进入已生效过滤
    await userEvent.type(await screen.findByLabelText('详情关键字'), 'redis')
    await userEvent.click(screen.getByRole('button', { name: '查询' }))
    await userEvent.click(screen.getByRole('button', { name: '导出 CSV' }))
    await waitFor(() =>
      expect(vi.mocked(exportAudits)).toHaveBeenCalledWith(
        expect.objectContaining({ detailKeyword: 'redis' }),
        'csv',
      ),
    )
  })

  it('点导出 JSON 时调 exportAudits(json)', async () => {
    renderPage(<AuditsPage />)
    await screen.findByRole('button', { name: '导出 JSON' })
    await userEvent.click(screen.getByRole('button', { name: '导出 JSON' }))
    await waitFor(() =>
      expect(vi.mocked(exportAudits)).toHaveBeenCalledWith(expect.anything(), 'json'),
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
