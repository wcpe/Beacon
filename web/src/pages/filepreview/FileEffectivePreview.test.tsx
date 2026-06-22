// FileEffectivePreview 单测（FR-45）：锁定逐文件渲染契约——
// 深合并文件展示逐键来源 + 被删键；整文件文件展示「整文件」徽标与单层来源；空/加载态。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import FileEffectivePreview from './FileEffectivePreview'
import type { EffectiveFileTreeView } from '../../api/client'

const noop = () => {}

// 对齐后端 effectiveFileView：sources/deletions 在每个 file 内
const DATA: EffectiveFileTreeView = {
  namespace: 'prod',
  serverId: 's1',
  group: 'area1',
  zone: 'zoneA',
  fileTreeMd5: 'abc12345deadbeef',
  files: [
    {
      path: 'app.yml',
      md5: 'deadbeef',
      content: 'b:\n  x: 1\n  y: 2\nc: 3\n',
      wholeFile: false,
      sources: [
        { path: ['b', 'x'], scope: 'global' },
        { path: ['c'], scope: 'server' },
      ],
      deletions: [{ path: ['a'], scope: 'server' }],
    },
    {
      path: 'boot.allin',
      md5: 'cafef00d',
      content: 's\n',
      wholeFile: true,
      sources: [{ path: [], scope: 'server' }],
      deletions: [],
    },
  ],
}

describe('FileEffectivePreview', () => {
  it('深合并文件展示逐键来源与被删除的键', () => {
    render(
      <FileEffectivePreview
        instances={[]}
        target={{ serverId: 's1' }}
        onTargetChange={noop}
        isLoading={false}
        data={DATA}
      />,
    )
    expect(screen.getByText('app.yml')).toBeInTheDocument()
    expect(screen.getByText('深合并')).toBeInTheDocument()
    expect(screen.getByText(/b\.x.*global/)).toBeInTheDocument()
    expect(screen.getByText('被删除的键（1 条）')).toBeInTheDocument()
    expect(screen.getByText(/a.*server/)).toBeInTheDocument()
  })

  it('整文件文件展示「整文件」徽标与单层来源（空路径只显示层名）', () => {
    render(
      <FileEffectivePreview
        instances={[]}
        target={{ serverId: 's1' }}
        onTargetChange={noop}
        isLoading={false}
        data={DATA}
      />,
    )
    expect(screen.getByText('boot.allin')).toBeInTheDocument()
    expect(screen.getByText('整文件')).toBeInTheDocument()
    expect(screen.getByText('整文件来自：')).toBeInTheDocument()
  })

  it('无文件时提示空，不渲染删除块', () => {
    const empty: EffectiveFileTreeView = { ...DATA, files: [] }
    render(
      <FileEffectivePreview
        instances={[]}
        target={{ serverId: 's1' }}
        onTargetChange={noop}
        isLoading={false}
        data={empty}
      />,
    )
    expect(screen.getByText('该目标无有效文件')).toBeInTheDocument()
    expect(screen.queryByText(/被删除的键/)).not.toBeInTheDocument()
  })

  it('加载中显示加载文案', () => {
    render(
      <FileEffectivePreview
        instances={[]}
        target={{}}
        onTargetChange={noop}
        isLoading
        data={undefined}
      />,
    )
    expect(screen.getByText('加载中…')).toBeInTheDocument()
  })
})
