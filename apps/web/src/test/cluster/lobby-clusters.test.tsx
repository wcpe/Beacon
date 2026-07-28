// /lobby-clusters 大厅集群页测试：独立于区服树，覆盖入口就绪、空态、排水保护与迁移写闭环。
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import LobbyClustersPage from '../../pages/lobby-clusters'
import { transferServerPlacement } from '../../api/cluster'
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

describe('/lobby-clusters 大厅集群页', () => {
  it('常规态展示唯一大厅摘要与 Bukkit 成员表', async () => {
    useScenario('normal')
    renderPage(<LobbyClustersPage />)

    expect(await screen.findByText('大厅成员 2 · 可接入 1 · 排水中 1 · 无候选 否')).toBeInTheDocument()
    expect(await screen.findByText('lobby-1')).toBeInTheDocument()
    expect(screen.getByText('大厅成员（仅 Bukkit）')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '前往待确认身份' })).toHaveAttribute('href', '/servers')
  })

  it('空态明确说明 BC 首次连接会被拒绝', async () => {
    useScenario('empty')
    renderPage(<LobbyClustersPage />)

    expect(await screen.findByText('无大厅成员时，BC 首次连接拒绝；先确认 Bukkit 身份，再迁入。')).toBeInTheDocument()
  })

  it('在线且有玩家的大厅成员禁止迁出，并给出排水提示', async () => {
    useScenario('normal')
    renderPage(<LobbyClustersPage />)

    const row = (await screen.findByText('lobby-1')).closest('tr')
    expect(row).not.toBeNull()
    expect(within(row as HTMLElement).getByRole('button', { name: '迁出' })).toBeDisabled()
    expect(within(row as HTMLElement).getByText('在线且有玩家，需先排水')).toBeInTheDocument()
  })

  it('确认迁入大厅后，候选服务器进入成员表', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<LobbyClustersPage />)

    await user.click(await screen.findByRole('button', { name: '迁入大厅' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: '迁入 build-1' }))
    const confirm = within(dialog).getByRole('button', { name: '确认迁入' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByLabelText('迁移原因'), '设置生产大厅入口')
    await user.click(confirm)

    await waitFor(() => {
      const row = screen.getByText('build-1').closest('tr')
      expect(row).not.toBeNull()
      expect(within(row as HTMLElement).getByText('大厅成员')).toBeInTheDocument()
    })
  })

  it('统一迁移端点支持迁出到业务小区及未分配', async () => {
    useScenario('normal')

    const toZone = await transferServerPlacement('lobby-2', { kind: 'zone', id: 30 }, '恢复业务小区')
    expect(toZone.placementKind).toBe('zone')
    expect(toZone.zoneId).toBe(30)

    const toUnassigned = await transferServerPlacement('lobby-2', null, '暂不分配')
    expect(toUnassigned.placementKind).toBe('')
    expect(toUnassigned.lobbyClusterId).toBeNull()
  })
})
