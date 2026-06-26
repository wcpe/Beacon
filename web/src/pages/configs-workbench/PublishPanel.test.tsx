// PublishPanel 单测（FR-115）：发布 + 影响面（仿 FR-2 热推）。
// mock usePublishImpact 注入受控影响面 + CodeEditor 规避 Monaco。
// 覆盖：loading 骨架、将发布清单（文件 + 覆盖层徽标 + vN→vN+1）、影响面按层分组与在线/有变化/未在线标、
// 拓印门——driftCount>0 时须勾审阅闸才放行发布、driftCount=0 时直接可发布、确认回传在线台数、
// 「查看」打开批量 diff 子浮层、取消回调。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'

vi.mock('@/components/CodeEditor', () => ({
  default: () => <div data-testid="diff-editor" />,
}))

vi.mock('./useWorkbenchData', () => ({
  usePublishImpact: vi.fn(),
}))

import PublishPanel from './PublishPanel'
import { usePublishImpact } from './useWorkbenchData'
import type { PublishImpact } from './types'

const mockedHook = vi.mocked(usePublishImpact)

// 有差异的影响面：motd.yml 全局，组 main 两台（lobby-1 有变化在线 / lobby-2 无变化在线 / pvp-1 离线有变化）
const IMPACT_DRIFT: PublishImpact = {
  files: [{ name: 'motd.yml', scope: 'global', fromVersion: 2, toVersion: 3 }],
  groups: [
    {
      scope: 'global',
      label: '全局',
      servers: [
        { serverId: 'lobby-1', online: true, changed: true },
        { serverId: 'lobby-2', online: true, changed: false },
        { serverId: 'pvp-1', online: false, changed: true },
      ],
    },
  ],
  driftCount: 1,
}

// 无差异影响面：driftCount=0 → 无拓印门
const IMPACT_CLEAN: PublishImpact = {
  files: [{ name: 'kits.yml', scope: 'global', fromVersion: 3, toVersion: 4 }],
  groups: [{ scope: 'global', label: '全局', servers: [{ serverId: 'lobby-1', online: true, changed: false }] }],
  driftCount: 0,
}

function mockImpact(over: Partial<ReturnType<typeof usePublishImpact>>) {
  mockedHook.mockReturnValue({ data: undefined, isLoading: false, ...over } as ReturnType<typeof usePublishImpact>)
}

describe('PublishPanel（FR-115）', () => {
  it('loading：显骨架', () => {
    mockImpact({ isLoading: true })
    render(<PublishPanel names={['motd.yml']} onPublish={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.queryByText('motd.yml')).not.toBeInTheDocument()
  })

  it('将发布清单：文件名 + 全局徽标 + v2→v3', () => {
    mockImpact({ data: IMPACT_DRIFT })
    render(<PublishPanel names={['motd.yml']} onPublish={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('motd.yml')).toBeInTheDocument()
    expect(screen.getByText(/v2/)).toBeInTheDocument()
    expect(screen.getByText('v3')).toBeInTheDocument()
  })

  it('影响面：在线有变化 / 在线无变化 / 未在线 三类标', () => {
    mockImpact({ data: IMPACT_DRIFT })
    render(<PublishPanel names={['motd.yml']} onPublish={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('有变化')).toBeInTheDocument()
    expect(screen.getByText('无变化')).toBeInTheDocument()
    expect(screen.getByText('未在线')).toBeInTheDocument()
    // 服务器 chip
    expect(screen.getByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('pvp-1')).toBeInTheDocument()
  })

  it('拓印门（driftCount>0）：未勾审阅闸时发布钮禁用，勾选后放行', async () => {
    mockImpact({ data: IMPACT_DRIFT })
    render(<PublishPanel names={['motd.yml']} onPublish={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.getByText('拓印审核：1 台有差异')).toBeInTheDocument()
    // 在线台数=2（lobby-1 + lobby-2，去重）
    const publishBtn = screen.getByRole('button', { name: '发布并热推（2 台）' })
    expect(publishBtn).toBeDisabled()
    await userEvent.click(screen.getByLabelText('我已审阅全部 diff'))
    expect(publishBtn).not.toBeDisabled()
  })

  it('确认发布回传在线台数', async () => {
    mockImpact({ data: IMPACT_DRIFT })
    const onPublish = vi.fn()
    render(<PublishPanel names={['motd.yml']} onPublish={onPublish} onCancel={vi.fn()} />)
    await userEvent.click(screen.getByLabelText('我已审阅全部 diff'))
    await userEvent.click(screen.getByRole('button', { name: '发布并热推（2 台）' }))
    expect(onPublish).toHaveBeenCalledWith(2)
  })

  it('无差异（driftCount=0）：无拓印门、发布钮直接可点', () => {
    mockImpact({ data: IMPACT_CLEAN })
    render(<PublishPanel names={['kits.yml']} onPublish={vi.fn()} onCancel={vi.fn()} />)
    expect(screen.queryByText(/拓印审核/)).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '发布并热推（1 台）' })).not.toBeDisabled()
  })

  it('「查看」打开批量 diff 子浮层', async () => {
    mockImpact({ data: IMPACT_DRIFT })
    render(<PublishPanel names={['motd.yml']} onPublish={vi.fn()} onCancel={vi.fn()} />)
    await userEvent.click(screen.getByRole('button', { name: '查看' }))
    expect(screen.getByText('拓印批量 diff · 期望值 ⟷ 服务器现状')).toBeInTheDocument()
  })

  it('取消触发 onCancel', async () => {
    mockImpact({ data: IMPACT_DRIFT })
    const onCancel = vi.fn()
    render(<PublishPanel names={['motd.yml']} onPublish={vi.fn()} onCancel={onCancel} />)
    // 底部「取消」（注意头部 X 的 aria-label 也是「取消」，取按钮文本「取消」中的可见文本钮）
    const cancels = screen.getAllByRole('button', { name: '取消' })
    await userEvent.click(cancels[cancels.length - 1])
    expect(onCancel).toHaveBeenCalled()
  })
})
