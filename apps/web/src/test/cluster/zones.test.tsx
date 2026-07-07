// /zones 页测试：结构树渲染、空态引导、未分配篮批量首次分配写闭环、筛选。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ZonesPage from '../../pages/zones'
import { renderPage, server, useScenario } from './harness'

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  server.resetHandlers()
})
afterAll(() => {
  server.close()
})

describe('/zones 区服分配页', () => {
  it('常规态渲染结构树与未分配篮', async () => {
    useScenario('normal')
    renderPage(<ZonesPage />)

    // 结构树出现已知集群与小区
    expect(await screen.findByText('bc-main')).toBeInTheDocument()
    expect(await screen.findByText('area-1')).toBeInTheDocument()
    // 未分配篮出现未分配 server
    expect(await screen.findByText('build-1')).toBeInTheDocument()
  })

  it('空态给出建集群引导', async () => {
    useScenario('empty')
    renderPage(<ZonesPage />)

    expect(
      await screen.findByText('尚未建立任何 BC 集群，先新建一个集群开始规划区服'),
    ).toBeInTheDocument()
  })

  it('未分配篮批量首次分配后该 server 从篮中消失（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ZonesPage />)

    // 勾选 build-1
    const basketRow = (await screen.findByText('build-1')).closest('tr')
    expect(basketRow).not.toBeNull()
    await user.click(within(basketRow as HTMLElement).getByRole('checkbox'))

    // 打开批量分配
    await user.click(screen.getByRole('button', { name: '批量分配' }))

    // 选目标小区（area-1，id=30）→ 影响预览 → 确认
    const dialog = await screen.findByRole('dialog')
    const targetSelect = within(dialog).getByLabelText('目标小区')
    await user.selectOptions(targetSelect, '30')
    await user.click(within(dialog).getByRole('button', { name: '确认分配' }))

    // build-1 从未分配篮消失
    await waitFor(() => {
      expect(screen.queryByText('build-1')).not.toBeInTheDocument()
    })
  })
})
