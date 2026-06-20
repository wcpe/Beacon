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
const NAMESPACES = ['prod', 'test']
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

describe('CreateConfigDialog', () => {
  it('环境/大区下拉来自传入数据，无硬编码示例', async () => {
    renderDialog(<CreateConfigDialog {...baseProps} open onOpenChange={() => {}} />)
    const nsSelect = screen.getByLabelText('环境') as HTMLSelectElement
    const nsOptions = within(nsSelect).getAllByRole('option').map((o) => o.textContent)
    expect(nsOptions).toEqual(NAMESPACES)
    // 旧硬编码大区示例不应再出现
    expect(screen.queryByRole('option', { name: 'server-a' })).not.toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'server-b' })).not.toBeInTheDocument()
  })

  it('scopeLevel=global 时隐藏覆盖目标，切到 server 出现实例下拉', async () => {
    renderDialog(<CreateConfigDialog {...baseProps} open onOpenChange={() => {}} />)
    // 默认 global：无覆盖目标控件
    expect(screen.queryByLabelText('覆盖目标')).not.toBeInTheDocument()
    // 切到 server
    await userEvent.selectOptions(screen.getByLabelText('覆盖层'), 'server')
    const targetSelect = (await screen.findByLabelText('覆盖目标')) as HTMLSelectElement
    const opts = within(targetSelect).getAllByRole('option').map((o) => o.textContent)
    expect(opts).toContain('srv-1')
    expect(opts).toContain('srv-2')
  })

  it('scopeLevel=group 时覆盖目标为大区下拉', async () => {
    renderDialog(<CreateConfigDialog {...baseProps} open onOpenChange={() => {}} />)
    await userEvent.selectOptions(screen.getByLabelText('覆盖层'), 'group')
    const targetSelect = (await screen.findByLabelText('覆盖目标')) as HTMLSelectElement
    const opts = within(targetSelect).getAllByRole('option').map((o) => o.textContent)
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
