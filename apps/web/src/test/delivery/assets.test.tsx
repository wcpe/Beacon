// /assets 文件资产页测试：常规渲染扫描概要与清单、空态引导、预览写闭环（敏感原因弹窗）、比对交互。
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AssetsPage from '../../pages/assets'
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

describe('/assets 文件资产页', () => {
  it('常规态渲染扫描概要与文件清单', async () => {
    useScenario('normal')
    renderPage(<AssetsPage />)

    // 扫描概要区标题
    expect(await screen.findByText('扫描概要')).toBeInTheDocument()
    // 文件清单区标题
    expect(await screen.findByText('文件清单')).toBeInTheDocument()
    // 清单中出现已知子服（集群 backend 之一）
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()
  })

  it('空态给出引导文案', async () => {
    useScenario('empty')
    renderPage(<AssetsPage />)

    expect(
      await screen.findByText(
        '当前 namespace 下暂无扫描记录，接入 agent 并完成首次清单上报后出现在此',
      ),
    ).toBeInTheDocument()
  })

  it('预览文本文件展示内容（预览写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    // 等清单出现后点第一行「预览」
    await screen.findByText('文件清单')
    const previewButtons = await screen.findAllByRole('button', { name: '预览' })
    await user.click(previewButtons[0])

    // 预览弹窗出现（对话框角色）
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toBeInTheDocument()
  })

  it('跨服比对输入路径后返回哈希分组', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AssetsPage />)

    // 比对区标题
    expect(await screen.findByText('跨服比对')).toBeInTheDocument()
    const compareRegion = screen.getByRole('region', { name: '跨服比对' })
    const pathInput = within(compareRegion).getByLabelText('文件路径')
    await user.type(pathInput, 'plugins/Essentials/config.yml')
    const runBtn = within(compareRegion).getByRole('button', { name: '比对' })
    await user.click(runBtn)

    // 出现哈希分组标题
    expect(await screen.findByText('哈希分组')).toBeInTheDocument()
  })
})
