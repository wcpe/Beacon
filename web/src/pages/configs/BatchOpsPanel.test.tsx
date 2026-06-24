// BatchOpsPanel 关键路径测试（FR-74）：
// 空选时按钮禁用；选中后禁用/启用直发 batchConfigs；删除须先过轻量确认才发请求。
// api/client 被 mock，用例在 jsdom 下稳定可跑。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

// mock 后端调用，由用例断言
vi.mock('../../api/client', () => ({
  batchConfigs: vi.fn(),
  getConfig: vi.fn(),
}))

// mock 全局消息提示，避免 toast 依赖
vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}))

import BatchOpsPanel from './BatchOpsPanel'
import { batchConfigs } from '../../api/client'
import type { ConfigView } from '../../api/types'

function renderPanel(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

// 两条配置铺底
const CONFIGS: ConfigView[] = [
  {
    id: 1,
    namespace: 'prod',
    group: '__GLOBAL__',
    dataId: 'a.yml',
    scopeLevel: 'global',
    scopeTarget: '',
    format: 'yaml',
    version: 1,
    md5: 'm1',
    enabled: true,
    updatedAt: '2026-06-24T00:00:00Z',
  },
  {
    id: 2,
    namespace: 'prod',
    group: 'bw',
    dataId: 'b.yml',
    scopeLevel: 'group',
    scopeTarget: '',
    format: 'yaml',
    version: 1,
    md5: 'm2',
    enabled: false,
    updatedAt: '2026-06-24T00:00:00Z',
  },
]

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(batchConfigs).mockResolvedValue({ action: 'disable', count: 1 })
})

describe('BatchOpsPanel', () => {
  it('空选时批量按钮禁用、不发请求', () => {
    renderPanel(<BatchOpsPanel configs={CONFIGS} />)
    expect(screen.getByRole('button', { name: '删除' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '禁用' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '启用' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '导出' })).toBeDisabled()
    expect(vi.mocked(batchConfigs)).not.toHaveBeenCalled()
  })

  it('选中后点禁用，以 disable 动作与所选 id 调 batchConfigs', async () => {
    renderPanel(<BatchOpsPanel configs={CONFIGS} />)
    // 勾选第一条
    await userEvent.click(screen.getByLabelText('a.yml'))
    expect(screen.getByText('已选 1 项')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '禁用' }))
    await waitFor(() => {
      expect(vi.mocked(batchConfigs)).toHaveBeenCalledWith('disable', [1])
    })
  })

  it('批量删除须先过轻量确认，确认才发请求', async () => {
    renderPanel(<BatchOpsPanel configs={CONFIGS} />)
    await userEvent.click(screen.getByLabelText('a.yml'))

    // 点删除：先弹确认，尚未发请求
    await userEvent.click(screen.getByRole('button', { name: '删除' }))
    expect(await screen.findByText('确认批量删除')).toBeInTheDocument()
    expect(vi.mocked(batchConfigs)).not.toHaveBeenCalled()

    // 点确认才发 delete 请求
    await userEvent.click(screen.getByRole('button', { name: '确认删除' }))
    await waitFor(() => {
      expect(vi.mocked(batchConfigs)).toHaveBeenCalledWith('delete', [1])
    })
  })

  it('全选勾选两条，禁用以两条 id 调用', async () => {
    renderPanel(<BatchOpsPanel configs={CONFIGS} />)
    await userEvent.click(screen.getByLabelText('全选'))
    expect(screen.getByText('已选 2 项')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '禁用' }))
    await waitFor(() => {
      expect(vi.mocked(batchConfigs)).toHaveBeenCalledWith('disable', [1, 2])
    })
  })
})
