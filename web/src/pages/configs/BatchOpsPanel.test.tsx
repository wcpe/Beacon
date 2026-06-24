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

// 稳定的消息提示替身：跨渲染共享，供用例断言成功 / 失败提示
const showSuccess = vi.fn()
const showError = vi.fn()
vi.mock('../../components/useMessage', () => ({
  useMessage: () => ({ showSuccess, showError }),
}))

import BatchOpsPanel from './BatchOpsPanel'
import { batchConfigs, getConfig } from '../../api/client'
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
  // jsdom 不实现 Blob URL 与 a.click()，导出用例打桩
  URL.createObjectURL = vi.fn(() => 'blob:mock')
  URL.revokeObjectURL = vi.fn()
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
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

  it('部分选中时表头全选框为 indeterminate 半选态', async () => {
    renderPanel(<BatchOpsPanel configs={CONFIGS} />)
    // 仅勾一条 → 未全选
    await userEvent.click(screen.getByLabelText('a.yml'))
    // radix Checkbox 半选时 aria-checked="mixed"
    expect(screen.getByLabelText('全选')).toHaveAttribute('aria-checked', 'mixed')
  })

  it('导出部分失败：仍下载成功子集并提示 N 项失败', async () => {
    // 第一条成功、第二条失败
    vi.mocked(getConfig).mockImplementation((id: number) =>
      id === 1 ? Promise.resolve(CONFIGS[0]) : Promise.reject(new Error('拉取失败')),
    )
    renderPanel(<BatchOpsPanel configs={CONFIGS} />)
    await userEvent.click(screen.getByLabelText('全选'))
    await userEvent.click(screen.getByRole('button', { name: '导出' }))

    await waitFor(() => {
      // 成功子集触发下载
      expect(URL.createObjectURL).toHaveBeenCalledTimes(1)
      // 成功提示按成功项计数
      expect(showSuccess).toHaveBeenCalledWith('已导出 1 项配置')
      // 失败计数提示
      expect(showError).toHaveBeenCalledWith('1 项导出失败')
    })
  })

  it('导出全部失败：不下载并报错', async () => {
    vi.mocked(getConfig).mockRejectedValue(new Error('拉取失败'))
    renderPanel(<BatchOpsPanel configs={CONFIGS} />)
    await userEvent.click(screen.getByLabelText('全选'))
    await userEvent.click(screen.getByRole('button', { name: '导出' }))

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith('拉取失败')
    })
    expect(URL.createObjectURL).not.toHaveBeenCalled()
  })
})
