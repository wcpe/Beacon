import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import EnvsPage from '../../pages/envs'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

describe('/envs 页 FR-205 双名称', () => {
  it('常规态创建时分别提交业务标识与显示名称', async () => {
    useScenario('normal')
    // 逐字输入不依赖定时器，避免并行 jsdom 负载把零延迟事件拖过测试时限。
    const user = userEvent.setup({ delay: null })
    renderPage(<EnvsPage />)

    await user.click(await screen.findByRole('button', { name: '创建环境' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('业务标识'), 'prod-shadow')
    await user.type(within(dialog).getByLabelText('显示名称'), '生产影子环境')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    expect(await screen.findByText('生产影子环境')).toBeInTheDocument()
    expect(screen.getByText('prod-shadow')).toBeInTheDocument()
  })

  it('编辑态业务标识只读，仅更新显示名称', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<EnvsPage />)

    const row = (await screen.findAllByText('生产'))[0].closest('tr')
    expect(row).not.toBeNull()
    await user.click(row as HTMLElement)
    await user.click(await screen.findByRole('button', { name: '编辑' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('业务标识')).toHaveAttribute('readonly')
    const displayName = within(dialog).getByLabelText('显示名称')
    await user.clear(displayName)
    await user.type(displayName, '生产主环境')
    await user.click(within(dialog).getByRole('button', { name: '保存' }))

    expect((await screen.findAllByText('生产主环境')).length).toBeGreaterThan(0)
    expect(screen.getAllByText('生产').length).toBeGreaterThan(0)
  })

  it('空态仍提供创建入口', async () => {
    useScenario('empty')
    renderPage(<EnvsPage />)

    expect(await screen.findByText('暂无环境，点击「创建环境」新增第一个')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '创建环境' })).toBeInTheDocument()
  })

  it('异常态展示脱敏后的加载错误', async () => {
    useScenario('error')
    renderPage(<EnvsPage />)

    await waitFor(() => {
      expect(screen.getByText(/模拟内部错误/)).toBeInTheDocument()
    })
  })
})
