// EffectivePreviewView 单测（FR-115）：生效预览并排 diff + 覆盖面计数。
// mock useEffectivePreview 注入受控数据，覆盖：loading 骨架、空态、
// 顶部「共 X 处覆盖 · Y/Z 文件」总览计数、每文件「N 处定制」/「无定制」、被覆盖键左右值与生效层徽标。
import { describe, it, expect, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

import EffectivePreviewView from './EffectivePreviewView'
import { useEffectivePreview } from './useWorkbenchData'
import type { EffectiveFile } from './types'

vi.mock('./useWorkbenchData', () => ({
  useEffectivePreview: vi.fn(),
}))

const mockedHook = vi.mocked(useEffectivePreview)

// 两文件：file1 含 1 处定制（链长 2）+ 1 处未定制（链长 1）；file2 全未定制。
const FILES: EffectiveFile[] = [
  {
    name: 'Essentials/config.yml',
    keys: [
      { key: 'ops-name-color', chain: [{ scope: 'global', value: "'c'" }] },
      { key: 'teleport-cooldown', chain: [{ scope: 'global', value: '3' }, { scope: 'group', value: '0' }] },
    ],
  },
  {
    name: 'motd.yml',
    keys: [{ key: 'motd', chain: [{ scope: 'global', value: "'hi'" }] }],
  },
]

function mockHook(over: Partial<ReturnType<typeof useEffectivePreview>>) {
  mockedHook.mockReturnValue({ data: undefined, isLoading: false, ...over } as ReturnType<typeof useEffectivePreview>)
}

describe('EffectivePreviewView（FR-115）', () => {
  it('loading：显骨架，不渲染数据', () => {
    mockHook({ isLoading: true })
    const { container } = render(<EffectivePreviewView serverId="lobby-1" />)
    // 骨架占位（无文件名文本）
    expect(screen.queryByText('motd.yml')).not.toBeInTheDocument()
    expect(container.querySelector('.animate-pulse, [class*="skeleton"]')).toBeTruthy()
  })

  it('空数据：显空态文案', () => {
    mockHook({ data: [] })
    render(<EffectivePreviewView serverId="lobby-1" />)
    expect(screen.getByText('该目标暂无生效预览数据')).toBeInTheDocument()
  })

  it('总览计数：共 1 处覆盖 · 1/2 文件（仅 file1 有 1 处定制）', () => {
    mockHook({ data: FILES })
    render(<EffectivePreviewView serverId="lobby-1" />)
    expect(screen.getByText('共 1 处覆盖 · 1/2 文件')).toBeInTheDocument()
    expect(screen.getByText('实例 lobby-1')).toBeInTheDocument()
  })

  it('每文件定制标记：file1「1 处定制」、file2「无定制」', () => {
    mockHook({ data: FILES })
    render(<EffectivePreviewView serverId="lobby-1" />)
    expect(screen.getByText('1 处定制')).toBeInTheDocument()
    expect(screen.getByText('无定制')).toBeInTheDocument()
  })

  it('被覆盖键：左基线值 3 与右生效值 0 都呈现，且生效层徽标=组', () => {
    mockHook({ data: FILES })
    render(<EffectivePreviewView serverId="lobby-1" />)
    expect(screen.getByText('3')).toBeInTheDocument()
    expect(screen.getByText('0')).toBeInTheDocument()
    // group 覆盖层徽标文案「组」
    expect(screen.getByText('组')).toBeInTheDocument()
  })
})
