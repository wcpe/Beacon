// /settings 运维设置页测试：常规渲染、保存热改项、健康权重保存 rev 递增、归档总览渲染。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import SettingsPage from '../../pages/settings'
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

describe('/settings 运维设置页', () => {
  it('分区导航切换渲染对应分区内容（不同时长堆三块）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<SettingsPage />)

    // 默认「运行参数」分区：热改项种子含 health.ttl-sec；其它分区内容此时不渲染
    expect(await screen.findByText('health.ttl-sec')).toBeInTheDocument()
    expect(screen.queryByText('健康因子权重')).not.toBeInTheDocument()

    // 切到「健康权重」分区
    await user.click(screen.getByRole('button', { name: '健康权重' }))
    expect(await screen.findByText('健康因子权重')).toBeInTheDocument()

    // 切到「归档清理」分区
    await user.click(screen.getByRole('button', { name: '归档清理' }))
    expect(await screen.findByText('归档与清理')).toBeInTheDocument()
  })

  it('归档任务点行展开非模态详情面板（不产生遮罩）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<SettingsPage />)

    await user.click(screen.getByRole('button', { name: '归档清理' }))

    // 点第一个任务行（任务号 #N）
    const jobCell = (await screen.findAllByText(/^#\d+$/))[0]
    await user.click(jobCell)

    // 详情面板为布局内列（非 role=dialog 遮罩）
    expect(await screen.findByText('归档任务详情')).toBeInTheDocument()
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
  })

  it('编辑并保存单个热改项后提示已保存（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<SettingsPage />)

    // 定位 health.ttl-sec 行的编辑按钮
    const row = (await screen.findByText('health.ttl-sec')).closest('tr')
    expect(row).not.toBeNull()
    await user.click(within(row as HTMLElement).getByRole('button', { name: '编辑' }))

    // 输入框出现，改值并保存
    const input = within(row as HTMLElement).getByRole('textbox')
    await user.clear(input)
    await user.type(input, '45')
    await user.click(within(row as HTMLElement).getByRole('button', { name: '保存' }))

    // 保存成功后行内展示新值
    await waitFor(() => {
      expect(within(screen.getByText('health.ttl-sec').closest('tr') as HTMLElement).getByText('45')).toBeInTheDocument()
    })
  })

  it('健康权重块展示当前版本 rev', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<SettingsPage />)

    // 切到「健康权重」分区
    await user.click(screen.getByRole('button', { name: '健康权重' }))

    // normal 场景种子权重历史含 rev 2
    expect(await screen.findByText(/当前版本/)).toBeInTheDocument()
    await waitFor(() => {
      expect(screen.getAllByText(/#2/).length).toBeGreaterThan(0)
    })
  })
})
