// /configs 配置中心页测试：常规列表渲染、空态引导、新建写闭环、进入详情切「有效配置」看合并内容。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ConfigsPage from '../../pages/configs'
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

describe('/configs 配置中心页', () => {
  it('常规态渲染配置文件列表', async () => {
    useScenario('normal')
    renderPage(<ConfigsPage />)

    // 列表区标题
    expect(await screen.findByText('配置文件')).toBeInTheDocument()
    // 出现已知配置文件
    expect(await screen.findByText('plugins/Essentials/config.yml')).toBeInTheDocument()
    // 「下发走变更单」提示
    expect(screen.getByText('配置修改不即时下发，生效请到「变更单」发起')).toBeInTheDocument()
  })

  it('空态给出新建引导文案', async () => {
    useScenario('empty')
    renderPage(<ConfigsPage />)

    expect(
      await screen.findByText('当前 namespace 下暂无配置文件，点「新建配置文件」创建第一个'),
    ).toBeInTheDocument()
  })

  it('新建配置文件后列表出现该名（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    // 等列表就绪后点「新建配置文件」
    await screen.findByText('plugins/Essentials/config.yml')
    await user.click(screen.getByRole('button', { name: '新建配置文件' }))

    // 弹窗内填名称并创建
    const dialog = await screen.findByRole('dialog')
    const nameInput = within(dialog).getByLabelText('文件名')
    await user.type(nameInput, 'plugins/NewPlugin/config.yml')
    await user.click(within(dialog).getByRole('button', { name: '创建' }))

    // 列表中出现新文件名
    expect(await screen.findByText('plugins/NewPlugin/config.yml')).toBeInTheDocument()
  })

  it('进入详情切「有效配置」看到合并内容', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    // 进入第一个文件详情
    await screen.findByText('plugins/Essentials/config.yml')
    const detailButtons = await screen.findAllByRole('button', { name: '详情' })
    await user.click(detailButtons[0])

    // 切到「有效配置」Tab
    await user.click(await screen.findByRole('tab', { name: '有效配置' }))

    // 合并内容区出现（默认按 namespace 合并，含 economy-enabled 键）
    await waitFor(() => {
      expect(screen.getAllByText(/economy-enabled/).length).toBeGreaterThan(0)
    })
  })

  it('keyword 搜索过滤配置文件', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ConfigsPage />)

    await screen.findByText('plugins/Essentials/config.yml')
    const keywordInput = screen.getByLabelText('搜索文件名')
    await user.type(keywordInput, 'Economy')

    // 过滤后 Economy 在、Essentials 不在
    expect(await screen.findByText('plugins/Economy/config.yml')).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.queryByText('plugins/Essentials/config.yml')).not.toBeInTheDocument()
    })
  })
})
