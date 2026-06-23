// SettingsPage 单测（FR-62 运维设置页）：
// 锁定行为——
// ① 列表按 key 前缀分组渲染（含 desc / key / 默认值 / 当前值）；
// ② 改 int 项保存以 updateSetting(key, "新值") 正确入参调用（值序列化为字符串）；
// ③ log.level 用下拉，改值保存以新枚举调用 updateSetting；
// ④ 后端 400 时把后端 message 经 toast 展示；
// ⑤ 值未变时保存按钮禁用；
// ⑥ 启动 / 安全项在 config.yml 的提示文案可见。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'
import type { SettingView } from '../api/types'

// mock 写操作反馈：spy showError/showSuccess，断言提示文案
const showError = vi.fn()
const showSuccess = vi.fn()
vi.mock('../components/useMessage', () => ({
  useMessage: () => ({ showError, showSuccess }),
}))

// mock 后端调用，由各用例注入数据。ApiClientError 取真实实现（按后端 message 提示需要）。
vi.mock('../api/client', async () => {
  const actual = await vi.importActual<typeof import('../api/client')>('../api/client')
  return {
    ApiClientError: actual.ApiClientError,
    listSettings: vi.fn(),
    updateSetting: vi.fn(),
  }
})

import SettingsPage from './SettingsPage'
import { ApiClientError, listSettings, updateSetting } from '../api/client'

// jsdom 未实现指针捕获 / scrollIntoView：radix Select（log.level 下拉）打开时会调用它们，缺失会抛错。
// 提供最小空垫片，使下拉交互可在 jsdom 下驱动（与 setup.ts 的 ResizeObserver 垫片同理）。
if (!HTMLElement.prototype.hasPointerCapture) {
  HTMLElement.prototype.hasPointerCapture = () => false
  HTMLElement.prototype.setPointerCapture = () => {}
  HTMLElement.prototype.releasePointerCapture = () => {}
}
if (!HTMLElement.prototype.scrollIntoView) {
  HTMLElement.prototype.scrollIntoView = () => {}
}

// 设置项样例：覆盖 health（int）/ metric（bool）/ log（string，log.level 下拉特例）/ reverse-fetch（int）四组。
const SETTINGS: SettingView[] = [
  {
    key: 'health.ttl-sec',
    value: '30',
    valueType: 'int',
    default: '30',
    desc: '超过多少秒未收到心跳即判失联',
    isStartup: false,
  },
  {
    key: 'metric.enabled',
    value: 'true',
    valueType: 'bool',
    default: 'true',
    desc: '是否启用指标采样器',
    isStartup: false,
  },
  {
    key: 'log.level',
    value: 'INFO',
    valueType: 'string',
    default: 'INFO',
    desc: '日志级别',
    isStartup: false,
  },
  {
    key: 'reverse-fetch.max-file-bytes',
    value: '1048576',
    valueType: 'int',
    default: '1048576',
    desc: '反向抓取单文件大小阈值（字节）',
    isStartup: false,
  },
]

function renderPage(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listSettings).mockResolvedValue(SETTINGS)
  vi.mocked(updateSetting).mockResolvedValue(undefined)
})

// 定位某设置项所在的行容器（按 key 文本上溯到行）
async function findRow(key: string): Promise<HTMLElement> {
  const keyNode = await screen.findByText(key)
  const row = keyNode.closest('[data-setting-row]')
  if (!row) throw new Error(`找不到设置项行：${key}`)
  return row as HTMLElement
}

describe('SettingsPage 列表与分组渲染（FR-62）', () => {
  it('按 key 前缀分组渲染（健康检查 / 指标 / 日志 / 反向抓取组标题可见）', async () => {
    renderPage(<SettingsPage />)
    expect(await screen.findByRole('heading', { name: '健康检查' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '指标' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '日志' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '反向抓取' })).toBeInTheDocument()
  })

  it('每项展示 desc / key / 默认值 / 当前值', async () => {
    renderPage(<SettingsPage />)
    const row = await findRow('health.ttl-sec')
    // desc 标签
    expect(within(row).getByText('超过多少秒未收到心跳即判失联')).toBeInTheDocument()
    // key 等宽小字
    expect(within(row).getByText('health.ttl-sec')).toBeInTheDocument()
    // 默认值提示（含默认值 30）
    expect(within(row).getByText(/默认/).textContent).toContain('30')
    // 当前值回显在输入控件
    expect(within(row).getByRole('spinbutton')).toHaveValue(30)
  })

  it('启动 / 安全项在 config.yml 的提示文案可见', async () => {
    renderPage(<SettingsPage />)
    expect(await screen.findByText(/config\.yml/)).toBeInTheDocument()
  })
})

describe('SettingsPage 逐项保存（FR-62）', () => {
  it('改 int 项后保存以 updateSetting(key, "新值") 调用', async () => {
    renderPage(<SettingsPage />)
    const row = await findRow('health.ttl-sec')
    const input = within(row).getByRole('spinbutton')
    await userEvent.clear(input)
    await userEvent.type(input, '45')
    await userEvent.click(within(row).getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('health.ttl-sec', '45'),
    )
    expect(showSuccess).toHaveBeenCalled()
  })

  it('改 log.level 下拉后保存以新枚举调用 updateSetting', async () => {
    renderPage(<SettingsPage />)
    const row = await findRow('log.level')
    // log.level 用下拉（combobox），选 DEBUG
    await userEvent.click(within(row).getByRole('combobox'))
    const listbox = await screen.findByRole('listbox')
    await userEvent.click(within(listbox).getByRole('option', { name: 'DEBUG' }))
    await userEvent.click(within(row).getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('log.level', 'DEBUG'),
    )
  })

  it('值未变时保存按钮禁用', async () => {
    renderPage(<SettingsPage />)
    const row = await findRow('health.ttl-sec')
    expect(within(row).getByRole('button', { name: '保存' })).toBeDisabled()
  })

  it('后端 400 时展示后端中文 message', async () => {
    vi.mocked(updateSetting).mockRejectedValue(
      new ApiClientError('非法值：必须为正整数', 'INVALID_VALUE'),
    )
    renderPage(<SettingsPage />)
    const row = await findRow('health.ttl-sec')
    const input = within(row).getByRole('spinbutton')
    await userEvent.clear(input)
    await userEvent.type(input, '0')
    await userEvent.click(within(row).getByRole('button', { name: '保存' }))
    await waitFor(() =>
      expect(showError).toHaveBeenCalledWith('非法值：必须为正整数'),
    )
  })
})
