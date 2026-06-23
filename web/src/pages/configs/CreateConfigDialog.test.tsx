// CreateConfigDialog 单测：锁定 FR-40 三项行为——
// ① 选项动态化（namespace/group/zone/server 来自传入数据，无硬编码示例）；
// ② scope↔target 联动（global 隐藏 target，group/zone/server 切换对应下拉）；
// ③ 复制预填（initial 注入源内容与 server 覆盖目标，提交走 createConfig）。
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactElement } from 'react'

vi.mock('../../api/client', () => ({
  createConfig: vi.fn(),
}))

import CreateConfigDialog from './CreateConfigDialog'
import { createConfig } from '../../api/client'

// 动态数据源：环境 / 大区 / 小区 / 实例（替代旧硬编码示例）
// 环境候选「值/显示分离」（FR-70）：value=code、label=「编码 · 名称」
const NAMESPACES = [
  { value: 'prod', label: 'prod · 生产' },
  { value: 'test', label: 'test · 测试' },
]
const GROUPS = ['gA', 'gB']
const ZONES = ['z1', 'z2']
const INSTANCES = [
  { serverId: 'srv-1', group: 'gA', zone: 'z1' },
  { serverId: 'srv-2', group: 'gB', zone: 'z2' },
]

function renderDialog(ui: ReactElement) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>)
}

beforeEach(() => {
  vi.mocked(createConfig).mockResolvedValue({
    id: 9,
    namespace: 'prod',
    group: 'gA',
    dataId: 'app.yml',
    scopeLevel: 'server',
    scopeTarget: 'srv-1',
    format: 'yaml',
    version: 1,
    md5: 'abc',
    enabled: true,
    updatedAt: '2026-01-01T00:00:00Z',
  })
})

const baseProps = {
  namespaces: NAMESPACES,
  groups: GROUPS,
  zones: ZONES,
  instances: INSTANCES,
}

// FR-51：维度输入改为 combobox。展开某维度下拉，返回其下拉候选容器（Popover 渲染到 body）。
async function openCombobox(label: string) {
  await userEvent.click(screen.getByLabelText(label))
  return screen.findByRole('listbox')
}

describe('CreateConfigDialog', () => {
  it('环境/大区下拉来自传入数据，无硬编码示例', async () => {
    renderDialog(<CreateConfigDialog {...baseProps} open onOpenChange={() => {}} />)
    const nsList = await openCombobox('环境')
    const nsOptions = within(nsList).getAllByRole('option').map((o) => o.textContent)
    // 候选显示「编码 · 名称」（FR-70）
    expect(nsOptions).toEqual(NAMESPACES.map((n) => n.label))
    // 旧硬编码大区示例不应再出现
    expect(within(nsList).queryByText('server-a')).not.toBeInTheDocument()
    expect(within(nsList).queryByText('server-b')).not.toBeInTheDocument()
  })

  it('scopeLevel=global 时隐藏覆盖目标，切到 server 出现实例下拉', async () => {
    renderDialog(<CreateConfigDialog {...baseProps} open onOpenChange={() => {}} />)
    // 默认 global：无覆盖目标控件
    expect(screen.queryByLabelText('覆盖目标')).not.toBeInTheDocument()
    // 切到 server（覆盖层仍为原生 select，枚举非维度）
    await userEvent.selectOptions(screen.getByLabelText('覆盖层'), 'server')
    await screen.findByLabelText('覆盖目标')
    const list = await openCombobox('覆盖目标')
    const opts = within(list).getAllByRole('option').map((o) => o.textContent)
    expect(opts).toContain('srv-1')
    expect(opts).toContain('srv-2')
  })

  it('scopeLevel=group 时覆盖目标为大区下拉', async () => {
    renderDialog(<CreateConfigDialog {...baseProps} open onOpenChange={() => {}} />)
    await userEvent.selectOptions(screen.getByLabelText('覆盖层'), 'group')
    await screen.findByLabelText('覆盖目标')
    const list = await openCombobox('覆盖目标')
    const opts = within(list).getAllByRole('option').map((o) => o.textContent)
    expect(opts).toContain('gA')
    expect(opts).toContain('gB')
  })

  it('initial 预填源内容与 server 覆盖目标，提交携带 server 层覆盖', async () => {
    renderDialog(
      <CreateConfigDialog
        {...baseProps}
        open
        onOpenChange={() => {}}
        initial={{
          namespace: 'prod',
          group: 'gA',
          dataId: 'app.yml',
          scopeLevel: 'server',
          scopeTarget: 'srv-1',
          format: 'yaml',
          content: 'k: v',
          comment: '',
        }}
      />,
    )
    // 预填的初始内容可见
    expect((screen.getByLabelText('初始内容') as HTMLInputElement).value).toBe('k: v')
    // 覆盖目标已为 server 层下拉并选中 srv-1
    expect((screen.getByLabelText('覆盖目标') as HTMLSelectElement).value).toBe('srv-1')
    await userEvent.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() =>
      expect(vi.mocked(createConfig)).toHaveBeenCalledWith(
        expect.objectContaining({
          namespace: 'prod',
          group: 'gA',
          dataId: 'app.yml',
          scopeLevel: 'server',
          scopeTarget: 'srv-1',
          content: 'k: v',
        }),
      ),
    )
  })
})
