// /agent-settings Agent 接入与配置页测试（FR-279）：
// 渲染（生效值 + 来源标记）、server 作用域切换、编辑保存闭环、
// 高危命令白名单警告与审批分支、禁止下发键不可编辑。
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AgentSettingsPage from '../../pages/agent-settings'
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

/**
 * 可编辑配置表本身。
 * 不能直接 `getByRole('table')`：加载骨架（TableSkeleton）同样是一个 <table>，会先命中它。
 * 故以「表内出现已知配置键」定位真实数据表。
 */
async function dataTable(): Promise<HTMLElement> {
  const anchor = await screen.findByText('timing.poll-timeout-ms')
  const table = anchor.closest('table')
  expect(table).not.toBeNull()
  return table as HTMLElement
}

/**
 * 取某个配置项所在的表格行。
 * 不用 `findByText(key).closest('tr')`：高危警告卡里也会出现同一个键名，
 * 故先定位数据表、再按「行内文本命中且唯一」找行；表可能在作用域切换后重建，每次重新解析。
 */
async function rowOf(key: string): Promise<HTMLElement> {
  const matched = await waitFor(() => {
    const table = screen.getByText('timing.poll-timeout-ms').closest('table')
    if (table === null) {
      throw new Error('配置表尚未渲染')
    }
    const rows = within(table)
      .getAllByRole('row')
      .filter((row) => within(row).queryByText(key) !== null)
    expect(rows).toHaveLength(1)
    return rows
  })
  return matched[0]
}

describe('/agent-settings Agent 接入与配置页', () => {
  it('默认落在首个命名空间，逐项展示生效值与来源标记', async () => {
    useScenario('normal')
    renderPage(<AgentSettingsPage />)

    // namespace 默认层的项：展示生效值与「namespace 默认」来源
    const pollRow = await rowOf('timing.poll-timeout-ms')
    expect(within(pollRow).getByText('25000')).toBeInTheDocument()
    expect(within(pollRow).getByText('namespace 默认')).toBeInTheDocument()

    // 未下发项：不虚构值，来源标记为「未设置」
    const backoffRow = await rowOf('backoff.initial-ms')
    expect(within(backoffRow).getByText('未设置')).toBeInTheDocument()

    // 最近下发时间来自 updatedAt（mock 种子有值，不应落到「从未下发」）
    expect(screen.queryByText('该作用域尚未下发过任何配置')).not.toBeInTheDocument()
  }, 20_000)

  it('选定服务器后展示 server 级覆盖来源（优先级高于 namespace 默认）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    // 先确认 namespace 默认层是 25000
    expect(within(await rowOf('timing.poll-timeout-ms')).getByText('25000')).toBeInTheDocument()

    // 服务器候选异步加载：等选择器可用再点，否则点到的是禁用控件
    const serverPicker = screen.getByRole('combobox', { name: '服务器（可选）' })
    await waitFor(() => {
      expect(serverPicker).toBeEnabled()
    })
    await user.click(serverPicker)
    await user.click(await screen.findByRole('option', { name: 'game-1' }))

    // 覆盖层返回后来源标记变为「server 级覆盖」（命中 game-1 的两项覆盖）
    expect((await screen.findAllByText('server 级覆盖')).length).toBeGreaterThan(0)
    const overridden = await rowOf('timing.poll-timeout-ms')
    expect(within(overridden).getByText('15000')).toBeInTheDocument()
  }, 20_000)

  it('编辑并保存后提示已下发与热生效时限（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('delivery.config-reload-timeout-ms')
    await user.click(within(row).getByRole('button', { name: '编辑' }))

    const input = within(row).getByRole('textbox')
    await user.clear(input)
    await user.type(input, '45000')

    // 原因必填：不填就下发会被挡下并给出提示
    await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('请填写下发原因')

    await user.type(screen.getByRole('textbox', { name: '下发原因' }), '压测期间放宽重载超时')
    await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))

    // 直执成功：明示热生效时限（agent 轮询下发配置的周期约 30 秒）
    expect(await screen.findByRole('status')).toHaveTextContent('已下发，agent 将在约 30 秒内热生效')

    // 保存后重新取数：新值已在该作用域生效
    const refreshed = await rowOf('delivery.config-reload-timeout-ms')
    await waitFor(() => {
      expect(within(refreshed).getByText('45000')).toBeInTheDocument()
    })
  }, 20_000)

  it('禁止下发的键不出现在可编辑列表，只读区注明只能本地配置', async () => {
    useScenario('normal')
    renderPage(<AgentSettingsPage />)

    // 禁区键在页面上可见（有解释），但没有任何编辑入口
    const endpoint = await screen.findByText('beacon.endpoints')
    const endpointRow = endpoint.closest('li')
    expect(endpointRow).not.toBeNull()
    expect(within(endpointRow as HTMLElement).getByText('只能本地配置')).toBeInTheDocument()
    expect(within(endpointRow as HTMLElement).queryByRole('button')).not.toBeInTheDocument()

    // 可编辑表里不应出现这些键
    const editableTable = await dataTable()
    expect(within(editableTable).queryByText('beacon.endpoints')).not.toBeInTheDocument()
    expect(within(editableTable).queryByText('beacon.bootstrap-token')).not.toBeInTheDocument()
    expect(screen.getByText('identity.server-id')).toBeInTheDocument()
    expect(screen.getByText('identity.identity-id')).toBeInTheDocument()
  }, 20_000)

  it('命令白名单显示高危警告，下发走审批而非直接生效', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    // 高危警告常驻可见（ADR-0011）
    expect(await screen.findByText('高危配置：命令白名单')).toBeInTheDocument()

    const row = await rowOf('override.command-whitelist')
    expect(within(row).getByText('高危')).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const input = within(row).getByRole('textbox')
    // JSON 数组含 [ ]：userEvent.type 会把它们当按键描述符，直接改值更稳
    fireEvent.change(input, { target: { value: '["reload"]' } })
    await user.type(screen.getByRole('textbox', { name: '下发原因' }), '值班需要热重载插件')
    await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))

    // 202 票据：提示「已提交审批」，不能谎称已下发
    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent('已提交审批')
    expect(status).toHaveTextContent('批准后才会下发到 agent')
    expect(screen.getByRole('link', { name: '查看审批' })).toBeInTheDocument()
  }, 20_000)
})
