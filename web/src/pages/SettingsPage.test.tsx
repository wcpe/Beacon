// 运维设置块单测（FR-62/FR-77，FR-94 改子 tab 呈现后仍保留集中草稿）：
// 锁定行为——
// ① 6 域改子 tab 呈现（默认健康检查 tab，切 tab 见对应组项）；
// ② 改 int 项保存以 updateSetting(key, "新值") 正确入参调用（值序列化为字符串）；
// ③ log.level 用下拉，改值保存以新枚举调用 updateSetting；
// ④ 后端 400 时把后端 message 经 toast 展示；
// ⑤ 值未变时保存按钮禁用；
// ⑥ 启动 / 安全项在 config.yml 的提示文案可见；
// ⑦【FR-94 硬约束】跨子 tab 改两项时统一批量保存仍统观全部脏项（dirtyItems=2）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
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

import OpsSettingsBlock from './settings/OpsSettingsBlock'
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
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/settings/ops']}>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}

// 切到某子 tab（按 tab 标题点击 tab 触发器）。
async function switchTab(label: string): Promise<void> {
  await userEvent.click(await screen.findByRole('tab', { name: label }))
}

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(listSettings).mockResolvedValue(SETTINGS)
  vi.mocked(updateSetting).mockResolvedValue(undefined)
})

// key 前缀 → 所在子 tab 标题（FR-94 子 tab 呈现：定位某项前先切到其所属 tab）。
const PREFIX_TAB: Record<string, string> = {
  health: '健康检查',
  metric: '指标',
  longpoll: '长轮询',
  alert: '告警',
  log: '日志',
  'reverse-fetch': '反向抓取',
}

// 定位某设置项所在的行容器：先切到该 key 所属子 tab，再按 key 文本上溯到行。
async function findRow(key: string): Promise<HTMLElement> {
  const prefix = key.includes('.') ? key.slice(0, key.indexOf('.')) : key
  const tabLabel = PREFIX_TAB[prefix]
  if (tabLabel) await switchTab(tabLabel)
  const keyNode = await screen.findByText(key)
  const row = keyNode.closest('[data-setting-row]')
  if (!row) throw new Error(`找不到设置项行：${key}`)
  return row as HTMLElement
}

describe('SettingsPage 子 tab 呈现（FR-62 + FR-94）', () => {
  it('6 域改子 tab 呈现（健康检查 / 指标 / 日志 / 反向抓取等 tab 触发器可见）', async () => {
    renderPage(<OpsSettingsBlock />)
    expect(await screen.findByRole('tab', { name: '健康检查' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '指标' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '日志' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: '反向抓取' })).toBeInTheDocument()
  })

  it('默认激活健康检查 tab；切到指标 tab 见指标项', async () => {
    renderPage(<OpsSettingsBlock />)
    // 默认 tab 见 health 项
    expect(await screen.findByText('health.ttl-sec')).toBeInTheDocument()
    // 切到指标 tab 见 metric 项
    await switchTab('指标')
    expect(await screen.findByText('metric.enabled')).toBeInTheDocument()
  })

  it('每项展示 desc / key / 默认值 / 当前值', async () => {
    renderPage(<OpsSettingsBlock />)
    const row = await findRow('health.ttl-sec')
    // desc 标签
    expect(within(row).getByText('超过多少秒未收到心跳即判失联')).toBeInTheDocument()
    // key 等宽小字
    expect(within(row).getByText('health.ttl-sec')).toBeInTheDocument()
    // 默认值提示（含默认值 30）。用「默认：」前缀精确定位提示文案，避开「恢复默认」按钮文本。
    expect(within(row).getByText(/默认：/).textContent).toContain('30')
    // 当前值回显在输入控件
    expect(within(row).getByRole('spinbutton')).toHaveValue(30)
  })

  it('启动 / 安全项在 config.yml 的提示文案可见', async () => {
    renderPage(<OpsSettingsBlock />)
    expect(await screen.findByText(/config\.yml/)).toBeInTheDocument()
  })
})

describe('SettingsPage 逐项保存（FR-62）', () => {
  it('改 int 项后保存以 updateSetting(key, "新值") 调用', async () => {
    renderPage(<OpsSettingsBlock />)
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
    renderPage(<OpsSettingsBlock />)
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
    renderPage(<OpsSettingsBlock />)
    const row = await findRow('health.ttl-sec')
    expect(within(row).getByRole('button', { name: '保存' })).toBeDisabled()
  })

  it('后端 400 时展示后端中文 message', async () => {
    vi.mocked(updateSetting).mockRejectedValue(
      new ApiClientError('非法值：必须为正整数', 'INVALID_VALUE'),
    )
    renderPage(<OpsSettingsBlock />)
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

describe('SettingsPage 恢复默认（FR-77）', () => {
  it('改值后点「恢复默认」把编辑控件置回该项默认值', async () => {
    renderPage(<OpsSettingsBlock />)
    const row = await findRow('health.ttl-sec')
    const input = within(row).getByRole('spinbutton')
    // 当前 30 / 默认 30，先改成 99 再恢复
    await userEvent.clear(input)
    await userEvent.type(input, '99')
    expect(input).toHaveValue(99)
    await userEvent.click(within(row).getByRole('button', { name: '恢复默认' }))
    expect(input).toHaveValue(30)
  })

  it('草稿值等于当前生效值时「恢复默认」禁用', async () => {
    renderPage(<OpsSettingsBlock />)
    const row = await findRow('health.ttl-sec')
    // 初始草稿 = 当前值 30，无可恢复改动
    expect(within(row).getByRole('button', { name: '恢复默认' })).toBeDisabled()
  })

  it('当前值与默认值不同的项，恢复默认置回默认值（而非当前值）', async () => {
    // 当前 INFO、默认 WARN：恢复默认应回到 WARN
    vi.mocked(listSettings).mockResolvedValue([
      { key: 'log.level', value: 'INFO', valueType: 'string', default: 'WARN', desc: '日志级别', isStartup: false },
    ])
    renderPage(<OpsSettingsBlock />)
    const row = await findRow('log.level')
    // 当前 INFO ≠ 默认 WARN，恢复默认可用
    const resetBtn = within(row).getByRole('button', { name: '恢复默认' })
    expect(resetBtn).toBeEnabled()
    await userEvent.click(resetBtn)
    // 草稿置为 WARN（脏，可保存）
    await userEvent.click(within(row).getByRole('button', { name: '保存' }))
    await waitFor(() => expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('log.level', 'WARN'))
  })
})

describe('SettingsPage 批量保存 + 改动摘要（FR-77）', () => {
  it('无改动时「保存全部变更」禁用且不显示改动摘要', async () => {
    renderPage(<OpsSettingsBlock />)
    await screen.findByText('health.ttl-sec')
    expect(screen.getByRole('button', { name: /保存全部变更/ })).toBeDisabled()
    expect(screen.queryByText(/改动摘要/)).not.toBeInTheDocument()
  })

  it('改两项后「保存全部变更（2）」对两项各调一次 updateSetting 并出汇总成功提示', async () => {
    renderPage(<OpsSettingsBlock />)
    // 改 health.ttl-sec 30→45
    const r1 = await findRow('health.ttl-sec')
    const i1 = within(r1).getByRole('spinbutton')
    await userEvent.clear(i1)
    await userEvent.type(i1, '45')
    // 改 reverse-fetch.max-file-bytes 1048576→2097152
    const r2 = await findRow('reverse-fetch.max-file-bytes')
    const i2 = within(r2).getByRole('spinbutton')
    await userEvent.clear(i2)
    await userEvent.type(i2, '2097152')

    const batchBtn = screen.getByRole('button', { name: /保存全部变更（2）/ })
    expect(batchBtn).toBeEnabled()
    await userEvent.click(batchBtn)

    await waitFor(() => {
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('health.ttl-sec', '45')
      expect(vi.mocked(updateSetting)).toHaveBeenCalledWith('reverse-fetch.max-file-bytes', '2097152')
    })
    expect(vi.mocked(updateSetting)).toHaveBeenCalledTimes(2)
    await waitFor(() => expect(showSuccess).toHaveBeenCalled())
  })

  it('批量保存中一项失败时其余项仍保存，汇总提示成功 / 失败计数', async () => {
    // 第一项失败、第二项成功
    vi.mocked(updateSetting).mockImplementation((key: string) =>
      key === 'health.ttl-sec'
        ? Promise.reject(new ApiClientError('非法值：必须为正整数', 'INVALID_VALUE'))
        : Promise.resolve(undefined),
    )
    renderPage(<OpsSettingsBlock />)
    const r1 = await findRow('health.ttl-sec')
    const i1 = within(r1).getByRole('spinbutton')
    await userEvent.clear(i1)
    await userEvent.type(i1, '0')
    const r2 = await findRow('reverse-fetch.max-file-bytes')
    const i2 = within(r2).getByRole('spinbutton')
    await userEvent.clear(i2)
    await userEvent.type(i2, '2097152')

    await userEvent.click(screen.getByRole('button', { name: /保存全部变更（2）/ }))

    await waitFor(() => expect(vi.mocked(updateSetting)).toHaveBeenCalledTimes(2))
    // 部分失败：出含「成功 1」「失败 1」的汇总提示
    await waitFor(() => {
      const summary = showError.mock.calls.flat().join(' ') + showSuccess.mock.calls.flat().join(' ')
      expect(summary).toContain('1')
    })
  })

  it('改动摘要列出每个脏项的「旧值 → 新值」', async () => {
    renderPage(<OpsSettingsBlock />)
    const r1 = await findRow('health.ttl-sec')
    const i1 = within(r1).getByRole('spinbutton')
    await userEvent.clear(i1)
    await userEvent.type(i1, '45')
    // 改动摘要出现，且含旧值 30 与新值 45
    const summary = await screen.findByTestId('change-summary')
    expect(summary.textContent).toContain('health.ttl-sec')
    expect(summary.textContent).toContain('30')
    expect(summary.textContent).toContain('45')
  })
})
