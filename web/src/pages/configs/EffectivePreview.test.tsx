// EffectivePreview 单测：锁定「deletions 按 item 维度渲染、无顶层 deletions」契约，防止再次整页崩溃。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import EffectivePreview from './EffectivePreview'
import type { EffectiveConfigView } from '../../api/client'

const noop = () => {}

// 对齐后端：无顶层 deletions，sources/deletions 均在 item 内
const DATA: EffectiveConfigView = {
  namespace: 'prod',
  serverId: '',
  group: '__GLOBAL__',
  zone: '',
  md5: 'abc12345',
  items: [
    {
      dataId: 'a.yml',
      format: 'yaml',
      content: 'k: v',
      md5: 'deadbeef',
      sources: [{ path: ['k'], scope: 'global' }],
      deletions: [{ path: ['old'], scope: 'group' }],
    },
  ],
}

describe('EffectivePreview', () => {
  it('渲染 item 内容、来源与 per-item 被删除的键（不读顶层 deletions）', () => {
    render(
      <EffectivePreview
        instances={[]}
        target={{ group: '__GLOBAL__' }}
        onTargetChange={noop}
        isLoading={false}
        data={DATA}
      />,
    )
    expect(screen.getByText(/a\.yml/)).toBeInTheDocument()
    expect(screen.getByText('被删除的键（1 条）')).toBeInTheDocument()
    expect(screen.getByText(/old.*group/)).toBeInTheDocument()
  })

  it('item 无 deletions 时不渲染删除块', () => {
    const data: EffectiveConfigView = {
      ...DATA,
      items: [{ ...DATA.items[0], deletions: [] }],
    }
    render(
      <EffectivePreview
        instances={[]}
        target={{ group: 'x' }}
        onTargetChange={noop}
        isLoading={false}
        data={data}
      />,
    )
    expect(screen.queryByText(/被删除的键/)).not.toBeInTheDocument()
  })

  it('加载中显示加载文案', () => {
    render(
      <EffectivePreview
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
