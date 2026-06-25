// AsyncSection 单测：加载/错误/内容三态。
import { describe, it, expect } from 'vitest'
import { render, screen } from '@testing-library/react'
import AsyncSection from './AsyncSection'

describe('AsyncSection', () => {
  it('加载中只显示加载文案，不渲染内容', () => {
    render(
      <AsyncSection isLoading>
        <div>内容</div>
      </AsyncSection>,
    )
    expect(screen.getByText('加载中…')).toBeInTheDocument()
    expect(screen.queryByText('内容')).not.toBeInTheDocument()
  })

  it('支持自定义加载文案', () => {
    render(
      <AsyncSection isLoading loadingText="拉取中">
        <div>内容</div>
      </AsyncSection>,
    )
    expect(screen.getByText('拉取中')).toBeInTheDocument()
  })

  it('提供 skeleton 时加载态渲染骨架而非文字，且不渲染内容', () => {
    render(
      <AsyncSection isLoading skeleton={<div data-testid="skeleton">骨架</div>}>
        <div>内容</div>
      </AsyncSection>,
    )
    expect(screen.getByTestId('skeleton')).toBeInTheDocument()
    // 有骨架时不再渲染纯文字「加载中…」
    expect(screen.queryByText('加载中…')).not.toBeInTheDocument()
    expect(screen.queryByText('内容')).not.toBeInTheDocument()
  })

  it('出错时显示错误 message', () => {
    render(
      <AsyncSection isLoading={false} isError error={new Error('网络异常')}>
        <div>内容</div>
      </AsyncSection>,
    )
    expect(screen.getByText('加载失败：网络异常')).toBeInTheDocument()
    expect(screen.queryByText('内容')).not.toBeInTheDocument()
  })

  it('成功时渲染子内容', () => {
    render(
      <AsyncSection isLoading={false}>
        <div>内容</div>
      </AsyncSection>,
    )
    expect(screen.getByText('内容')).toBeInTheDocument()
  })
})
