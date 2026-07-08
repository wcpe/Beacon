// /zones 区服分配页测试（主从：树 + 抽屉）：结构树常规渲染（含代理角色标注）、空态引导、
// 未分配抽屉批量首次分配写闭环。未分配收敛为抽屉入口，故分配用例先开抽屉。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ZonesPage from '../../pages/zones'
import { createTestServer, renderPage, useScenario } from './harness'

// 本文件独享 mock 服务端实例
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

describe('/zones 区服分配页', () => {
  it('常规态渲染结构树（集群 + 大区 + 小区）', async () => {
    useScenario('normal')
    renderPage(<ZonesPage />)

    // 结构树出现已知集群、大区与小区（集群 + 大区默认展开）
    expect(await screen.findByText('bc-main')).toBeInTheDocument()
    expect(await screen.findByText('华东大区')).toBeInTheDocument()
    expect(await screen.findByText('area-1')).toBeInTheDocument()
  })

  it('集群节点标注代理角色计数', async () => {
    useScenario('normal')
    renderPage(<ZonesPage />)

    // 集群头带「代理 · N」角色徽标（代理服明确标注）
    expect(await screen.findByText(/代理 · \d/)).toBeInTheDocument()
  })

  it('空态给出建集群引导', async () => {
    useScenario('empty')
    renderPage(<ZonesPage />)

    expect(
      await screen.findByText('尚未建立任何 BC 集群，先新建一个集群开始规划区服'),
    ).toBeInTheDocument()
  })

  it('打开未分配抽屉批量首次分配后该 server 从抽屉消失（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<ZonesPage />)

    // 等结构树加载，再从顶部入口打开未分配抽屉
    await screen.findByText('bc-main')
    await user.click(await screen.findByRole('button', { name: /未分配/ }))

    // 抽屉内勾选 build-1
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

    // build-1 从未分配抽屉消失
    await waitFor(() => {
      expect(screen.queryByText('build-1')).not.toBeInTheDocument()
    })
  })
})
