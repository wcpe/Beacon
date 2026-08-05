import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'

import LifecycleMockReview from '../../features/lifecycle/mock-review'
import { renderPage } from '../system/harness'

describe('生命周期评审 mock', () => {
  it('覆盖空态、加载态、错误态与超大量态', async () => {
    const user = userEvent.setup()
    renderPage(<LifecycleMockReview subject="namespace" />)

    await user.click(screen.getByRole('button', { name: '空态' }))
    expect(screen.getByText(/没有可执行生命周期操作/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '加载态' }))
    expect(screen.getByLabelText('影响快照加载中')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '错误态' }))
    expect(screen.getByText('影响快照已漂移')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '超大量态' }))
    expect(screen.getByText(/浏览器不会持有完整子树/)).toBeInTheDocument()
  })

  it('归档、恢复与永久删除均创建统一审批申请，不直接改变状态', async () => {
    const user = userEvent.setup()
    renderPage(<LifecycleMockReview subject="server" />)

    const archive = screen.getByRole('button', { name: '申请归档' })
    await user.type(screen.getByLabelText('申请原因'), '测试资产归档')
    await user.click(archive)
    expect(screen.getByRole('status')).toHaveTextContent('模拟审批申请')

    await user.click(screen.getByRole('button', { name: /game-legacy-01/ }))
    await user.type(screen.getByLabelText('申请原因'), '测试资产恢复')
    await user.click(screen.getAllByRole('button', { name: '申请恢复' })[1])
    expect(screen.getByRole('status')).toHaveTextContent('申请恢复')

    await user.click(screen.getAllByRole('button', { name: '申请永久删除' })[0])
    const permanent = screen.getAllByRole('button', { name: '申请永久删除' })[1]
    expect(permanent).toBeDisabled()
    await user.type(screen.getByLabelText('申请原因'), '测试资产退出')
    await user.type(screen.getByLabelText('输入完整服务器稳定标识以确认'), 'game-legacy-01')
    expect(permanent).toBeEnabled()
    await user.click(permanent)
    expect(screen.getByText(/不会发送后端请求/)).toBeInTheDocument()
  })

  it('墓碑详情没有恢复或再次删除入口', async () => {
    const user = userEvent.setup()
    renderPage(<LifecycleMockReview subject="namespace" />)

    await user.click(screen.getByRole('button', { name: /game-retired/ }))
    expect(screen.getByText('只读墓碑详情')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '申请恢复' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '申请永久删除' })).not.toBeInTheDocument()
  })

  it('命名空间永久删除展示同事务子树墓碑和关闭范围', async () => {
    const user = userEvent.setup()
    renderPage(<LifecycleMockReview subject="namespace" />)

    await user.click(screen.getByRole('button', { name: /game-legacy/ }))
    await user.click(screen.getAllByRole('button', { name: '申请永久删除' })[0])
    expect(screen.getByText(/整棵权威子树将在同一事务中墓碑化/)).toBeInTheDocument()
    expect(screen.getByText(/环境映射、信任和身份同时关闭/)).toBeInTheDocument()
  })

  it('可按生命周期筛选，并在墓碑对象中维持无动作终态', async () => {
    const user = userEvent.setup()
    renderPage(<LifecycleMockReview subject="server" />)

    await user.selectOptions(screen.getByLabelText('生命周期筛选'), 'tombstoned')
    expect(screen.getByRole('button', { name: /game-retired-01/ })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /game-prod-01/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /game-retired-01/ }))
    expect(screen.getByText(/serverId 不会复用/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '申请恢复' })).not.toBeInTheDocument()
  })
})
