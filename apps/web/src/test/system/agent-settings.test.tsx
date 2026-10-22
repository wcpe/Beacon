// /agent-settings Agent 接入与配置页测试（FR-279）：
// 渲染（生效值 + 来源标记）、server 作用域切换、编辑保存闭环、
// 高危命令白名单警告与审批分支、禁止下发键不可编辑。
//
// 来源列是本文件重点覆盖对象（FR-279 #2）：只允许出现「控制面能断言到的那几档」，
// 不得出现「代码默认」「本地配置」这类控制面其实分不出来的单独档位。
//
// 另有一组「与真后端同口径」的回归锁（C1/C2/C3/M1/M3）——这些缺陷的共同点是
// 只在真实后端上暴露（devmock 曾比后端宽松），所以断言都钉在请求体与票据状态上。
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { HttpResponse, http } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import type {
  AgentSettingItem,
  AgentSettingsResponse,
  AgentSettingsWriteResult,
  UpdateAgentSettingsBody,
} from '@beacon/contracts'
import { setAgentSettingsTicketStatus } from '@beacon/devmock'

import { i18next } from '../../i18n'
import AgentSettingsPage from '../../pages/agent-settings'
import {
  commandWhitelistIssue,
  isRevocableAgentSettingSource,
  splitAgentSettingStringList,
  validateCommandWhitelist,
} from '../../pages/agent-settings/keys'
import { createTestServer, renderPage, useScenario } from './harness'

const server = createTestServer()

/**
 * 故意放宽 source 为 string 的响应形：用于构造契约之外的来源取值，
 * 模拟「控制面比前端新」。若用 AgentSettingsResponse 锚定，类型系统会直接堵掉这条兼容性用例。
 */
type LenientSettingsResponse = Omit<AgentSettingsResponse, 'items'> & {
  items: (Omit<AgentSettingItem, 'source'> & { source: string })[]
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(async () => {
  server.resetHandlers()
  // 票据状态注入随场景复位的，这里再显式兜一次：用例失败时不把状态漏给下一个文件/用例
  setAgentSettingsTicketStatus('pending')
  // i18n 是单例：语言切换必须复位，否则会污染同 worker 内的其它用例
  await i18next.changeLanguage('zh-CN')
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

/**
 * 走完「编辑白名单 → 填原因 → 提交」的完整闭环。
 * 白名单行的取值用 fireEvent 直接改：`[` `]` 等字符会被 userEvent.type 当成按键描述符。
 */
async function submitWhitelist(user: ReturnType<typeof userEvent.setup>, value: string): Promise<void> {
  const row = await rowOf('override.command-whitelist')
  await user.click(within(row).getByRole('button', { name: '编辑' }))
  fireEvent.change(within(row).getByRole('textbox'), { target: { value } })
  await user.type(screen.getByRole('textbox', { name: '下发原因' }), '值班需要热重载插件')
  await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))
}

describe('/agent-settings Agent 接入与配置页', () => {
  it('默认落在首个命名空间，逐项展示生效值与来源标记', async () => {
    useScenario('normal')
    renderPage(<AgentSettingsPage />)

    // namespace 默认层的项：展示生效值与「namespace 默认」来源
    const pollRow = await rowOf('timing.poll-timeout-ms')
    expect(within(pollRow).getByText('25000')).toBeInTheDocument()
    expect(within(pollRow).getByText('namespace 默认')).toBeInTheDocument()

    // 未下发项：不虚构值（不显示代码默认），来源标为「本地配置或代码默认」。
    // 关键点：这里**不能**出现「代码默认」单独一档——控制面看不到 agent 本地生效值。
    const backoffRow = await rowOf('backoff.initial-ms')
    expect(within(backoffRow).getByText('本地配置或代码默认')).toBeInTheDocument()
    expect(within(backoffRow).getByText('—')).toBeInTheDocument()
    expect(within(backoffRow).queryByText('未设置')).not.toBeInTheDocument()

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
    // 合法格式：与服务端 string-list 同口径（逗号或换行分隔的命令首 token）
    await user.type(input, 'reload, taboolib')
    expect(within(row).queryByText(/不要填 JSON 数组/)).not.toBeInTheDocument()

    await user.type(screen.getByRole('textbox', { name: '下发原因' }), '值班需要热重载插件')
    await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))

    // 202 票据：提示「已提交审批」，不能谎称已下发
    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent('已提交审批')
    expect(status).toHaveTextContent('批准后才会下发到 agent')
    expect(screen.getByRole('link', { name: '查看审批' })).toBeInTheDocument()
  }, 20_000)

  // —— C1：二次确认串必须原样回填 GET 的 scopeLabel ——

  it('保存时把 GET 的 scopeLabel 原样作为 confirmationScope 发回（缺了它后端每次必 400）', async () => {
    const bodies: UpdateAgentSettingsBody[] = []
    // 覆盖 PUT 只做「抓请求体」，返回契约内的 200 形状让页面正常收敛
    // （GET 仍走 devmock，scopeLabel 由它给出，前端不得自行拼装）
    server.use(
      http.put('*/admin/v2/agent-settings', async ({ request }) => {
        bodies.push((await request.json()) as UpdateAgentSettingsBody)
        return HttpResponse.json({
          scope: { items: [], updatedAt: '', scopeLabel: 'prod' },
          applied: ['delivery.config-reload-timeout-ms'],
          removed: [],
          unchanged: [],
        } satisfies AgentSettingsWriteResult)
      }),
    )
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('delivery.config-reload-timeout-ms')
    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const input = within(row).getByRole('textbox')
    await user.clear(input)
    await user.type(input, '45000')
    await user.type(screen.getByRole('textbox', { name: '下发原因' }), '压测期间放宽重载超时')
    await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))

    await waitFor(() => {
      expect(bodies).toHaveLength(1)
    })
    // 回归锁：必须等于 GET 返回的 scopeLabel（namespace 级 = 环境码 prod）
    expect(bodies[0].confirmationScope).toBe('prod')
    expect(bodies[0].namespaceId).toBe(1)
    expect(bodies[0].serverId).toBeUndefined()
    expect(bodies[0].values).toEqual({ 'delivery.config-reload-timeout-ms': '45000' })
  }, 20_000)

  it('GET 没有 scopeLabel 时禁止提交，并说明作用域未确认', async () => {
    // 老控制面 / 契约漂移：响应里没有二次确认串。此时不能发空串去换一个 400。
    server.use(
      http.get('*/admin/v2/agent-settings', () =>
        HttpResponse.json({
          items: [
            {
              key: 'timing.poll-timeout-ms',
              value: '25000',
              source: 'namespace',
              desc: '长轮询挂起上限（毫秒）',
            },
          ],
          updatedAt: '',
        } satisfies AgentSettingsResponse),
      ),
    )
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('timing.poll-timeout-ms')
    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const input = within(row).getByRole('textbox')
    await user.clear(input)
    await user.type(input, '20000')

    // 有改动但作用域未确认：按钮按不下去，且页面上说明了原因
    expect(screen.getByRole('button', { name: '保存 1 项改动' })).toBeDisabled()
    expect(await screen.findByText(/作用域尚未确认/)).toBeInTheDocument()
  }, 20_000)

  // —— C2：白名单口径与 agent / 服务端的 stringList 一致 ——

  it('白名单填 JSON 数组被判非法，并提示正确格式', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('override.command-whitelist')
    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const input = within(row).getByRole('textbox')
    // JSON 数组含 [ ]：userEvent.type 会把它们当按键描述符，直接改值更稳
    fireEvent.change(input, { target: { value: '["reload"]' } })

    // 行内给的是「能照做」的提示，而不是笼统的格式错误
    const issueText = within(row).getByText(/不要填 JSON 数组/)
    expect(issueText).toHaveTextContent('reload, taboolib')
    expect(input).toHaveAttribute('aria-invalid', 'true')
    expect(screen.getByRole('button', { name: '保存 1 项改动' })).toBeDisabled()
  }, 20_000)

  it('白名单含注入字符被判非法', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('override.command-whitelist')
    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const input = within(row).getByRole('textbox')
    fireEvent.change(input, { target: { value: 'reload; stop' } })

    expect(within(row).getByText(/不允许出现 ; & \| > < \$ 反引号/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保存 1 项改动' })).toBeDisabled()
  }, 20_000)

  // —— 撤销本层覆盖（null） ——

  it('点「撤销本层覆盖」后，提交体里该键为 null 而不是空串', async () => {
    const bodies: UpdateAgentSettingsBody[] = []
    server.use(
      http.put('*/admin/v2/agent-settings', async ({ request }) => {
        bodies.push((await request.json()) as UpdateAgentSettingsBody)
        return HttpResponse.json({
          scope: { items: [], updatedAt: '', scopeLabel: 'prod' },
          applied: [],
          removed: ['timing.poll-timeout-ms'],
          unchanged: [],
        } satisfies AgentSettingsWriteResult)
      }),
    )
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    // namespace 层就有该键的覆盖（25000），故「撤销本层覆盖」应可用
    const row = await rowOf('timing.poll-timeout-ms')
    await user.click(within(row).getByRole('button', { name: '撤销本层覆盖' }))

    // 标记态可见：告诉运维「提交后这个键会回退」
    expect(within(row).getByText(/待撤销/)).toBeInTheDocument()
    // 撤销不是「设为空」：不该冒出「清空不等于撤销」的就地报错
    expect(within(row).queryByText(/清空不等于撤销/)).not.toBeInTheDocument()

    await user.type(screen.getByRole('textbox', { name: '下发原因' }), '回退到上层')
    await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))

    await waitFor(() => {
      expect(bodies).toHaveLength(1)
    })
    // 核心断言：必须是 null。传空串会被服务端 400（空值不等于撤销）
    expect(bodies[0].values['timing.poll-timeout-ms']).toBeNull()
  }, 20_000)

  it('没有本层覆盖的项不提供撤销入口（撤销它是无操作，不该产生空下发）', async () => {
    useScenario('normal')
    renderPage(<AgentSettingsPage />)

    // prod 命名空间下 messaging.* 未下发 → 来源是「本地配置或代码默认」，
    // 控制面在这一层没有行可撤，故不该出现该按钮
    const row = await rowOf('assets.scan-interval-sec')
    expect(within(row).queryByRole('button', { name: '撤销本层覆盖' })).not.toBeInTheDocument()
  }, 20_000)

  it('点「取消撤销」后回到未改动，不产生任何下发', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('timing.poll-timeout-ms')
    await user.click(within(row).getByRole('button', { name: '撤销本层覆盖' }))
    expect(within(row).getByText(/待撤销/)).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: '取消撤销' }))
    expect(within(row).queryByText(/待撤销/)).not.toBeInTheDocument()
    // 回到零改动：保存按钮回到禁用态
    expect(screen.getByRole('button', { name: '保存 0 项改动' })).toBeDisabled()
  }, 20_000)

  // —— M3：清空不等于撤销 ——

  it('把值清空不会静默下发：就地提示「清空不等于撤销」并挡住提交', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('timing.poll-timeout-ms')
    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const input = within(row).getByRole('textbox')
    await user.clear(input)

    expect(within(row).getByText(/清空不等于撤销/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保存 1 项改动' })).toBeDisabled()
  }, 20_000)

  // —— C3：票据 status 必须被读取 ——

  it('票据状态 pending 时显示「已提交审批」', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    await submitWhitelist(user, 'reload')

    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent('已提交审批')
    expect(status).toHaveTextContent('批准后才会下发到 agent')
  }, 20_000)

  it('票据状态 rejected 时不显示「已提交审批」，改报已终结（旧票据不该谎报待批）', async () => {
    // 服务端命中幂等键时会回既有申请而不判状态：一张早已 rejected 的票据与新建的长得一样
    useScenario('normal')
    setAgentSettingsTicketStatus('rejected')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    await submitWhitelist(user, 'reload')

    const status = await screen.findByRole('status')
    expect(status).not.toHaveTextContent('已提交审批')
    expect(status).toHaveTextContent('审批票据已终结（rejected）')
    expect(status).toHaveTextContent('本次未下发')
    // 没有待审批的语义，就不该给出「查看审批」这条暗示还在等签发的入口
    expect(screen.queryByRole('link', { name: '查看审批' })).not.toBeInTheDocument()
  }, 20_000)

  it('票据状态未知时保守提示到审批中心确认，不谎称待审批', async () => {
    useScenario('normal')
    // executing 等未来新增状态：既不当作 pending，也不当作已终结
    setAgentSettingsTicketStatus('executing')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    await submitWhitelist(user, 'reload')

    const status = await screen.findByRole('status')
    expect(status).not.toHaveTextContent('已提交审批')
    expect(status).toHaveTextContent('票据状态未知')
  }, 20_000)

  it('202 返回整个写结果体（票据嵌在 approvalTicket）时仍判为待审批，不假报已下发', async () => {
    // M1 回归锁：只认顶层 approvalRequestId 时，这种形态会被当成「已下发」——
    // 运维以为配置生效了，实际还躺在审批中心等签发，是最坏的一种谎报。
    server.use(
      http.put('*/admin/v2/agent-settings', () =>
        HttpResponse.json(
          {
            scope: { items: [], updatedAt: '', scopeLabel: 'prod' },
            applied: [],
            removed: [],
            unchanged: [],
            approvalTicket: {
              approvalRequestId: 'apr_nested_1',
              status: 'pending',
              operationKey: 'agent_setting.dangerous',
            },
          } satisfies AgentSettingsWriteResult,
          { status: 202 },
        ),
      ),
    )
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    await submitWhitelist(user, 'reload')

    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent('已提交审批')
    expect(status).not.toHaveTextContent('已下发')
  }, 20_000)

  it('200 直执按 {scope, applied, removed, unchanged} 收敛为「已下发」', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('delivery.config-reload-timeout-ms')
    await user.click(within(row).getByRole('button', { name: '编辑' }))
    const input = within(row).getByRole('textbox')
    await user.clear(input)
    await user.type(input, '45000')
    await user.type(screen.getByRole('textbox', { name: '下发原因' }), '压测期间放宽重载超时')
    await user.click(screen.getByRole('button', { name: '保存 1 项改动' }))

    // 真实 200 形状（没有 ok 字段）也必须走「已下发」分支，而不是因为找不到票据就报错
    const status = await screen.findByRole('status')
    expect(status).toHaveTextContent('已下发，agent 将在约 30 秒内热生效')
  }, 20_000)


  // —— 来源三档（FR-279 #2）——

  it('来源列同时给出三档，并说明每档该去哪改', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    // namespace 档：环境默认，改在 Web
    expect(within(await rowOf('messaging.enabled')).getByText('namespace 默认')).toBeInTheDocument()
    // 不可区分档：控制面没下发，改在机器本地
    expect(within(await rowOf('backoff.max-ms')).getByText('本地配置或代码默认')).toBeInTheDocument()

    // 切到 game-1：server 级覆盖档出现（改在 Web，且优先于 namespace）
    const serverPicker = screen.getByRole('combobox', { name: '服务器（可选）' })
    await waitFor(() => {
      expect(serverPicker).toBeEnabled()
    })
    await user.click(serverPicker)
    await user.click(await screen.findByRole('option', { name: 'game-1' }))
    expect(within(await rowOf('timing.poll-timeout-ms')).getByText('server 级覆盖')).toBeInTheDocument()

    // 表下常驻说明：把「去哪改」写死在界面上，不靠文档
    expect(screen.getByText(/「server 级覆盖」与「namespace 默认」改在本页/)).toBeInTheDocument()
    expect(screen.getByText(/要到该机器的 config.yml/)).toBeInTheDocument()
  }, 20_000)

  it('英文文案同步覆盖三档来源与去向说明', async () => {
    await i18next.changeLanguage('en')
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AgentSettingsPage />)

    expect(within(await rowOf('messaging.enabled')).getByText('Namespace default')).toBeInTheDocument()
    expect(
      within(await rowOf('backoff.max-ms')).getByText('Local config or code default'),
    ).toBeInTheDocument()

    const serverPicker = screen.getByRole('combobox', { name: 'Server (optional)' })
    await waitFor(() => {
      expect(serverPicker).toBeEnabled()
    })
    await user.click(serverPicker)
    await user.click(await screen.findByRole('option', { name: 'game-1' }))
    expect(within(await rowOf('timing.poll-timeout-ms')).getByText('Server override')).toBeInTheDocument()

    expect(screen.getByText(/Edit "Server override" and "Namespace default" here/)).toBeInTheDocument()
  }, 20_000)

  it('旧服务端取值 unset 与旧契约取值 default 都按「本地配置或代码默认」呈现（版本错配不崩）', async () => {
    // 覆盖 GET 端点：把三档来源换成旧值 + 一个本版本不认识的新值，
    // 验证来源列不会露 i18n 原始键，也不会把未知值硬套成某个已知档。
    server.use(
      http.get('*/admin/v2/agent-settings', () =>
        HttpResponse.json({
          items: [
            { key: 'timing.poll-timeout-ms', value: '15000', source: 'unset', desc: '旧服务端取值' },
            { key: 'backoff.max-ms', value: '', source: 'default', desc: '旧契约取值' },
            { key: 'assets.enabled', value: '', source: 'brand-new-source', desc: '本版本不认识的来源' },
          ],
          updatedAt: '',
        } satisfies LenientSettingsResponse),
      ),
    )
    useScenario('normal')
    renderPage(<AgentSettingsPage />)

    expect(within(await rowOf('timing.poll-timeout-ms')).getByText('本地配置或代码默认')).toBeInTheDocument()
    expect(within(await rowOf('backoff.max-ms')).getByText('本地配置或代码默认')).toBeInTheDocument()
    // 未识别来源明说「来源未知」，不冒充任何已知档
    expect(within(await rowOf('assets.enabled')).getByText('来源未知')).toBeInTheDocument()
    expect(screen.queryByText(/system\.agentSettings\.source/)).not.toBeInTheDocument()
  }, 20_000)

  it('未下发项不虚构生效值：value 为空串，只显示占位符，并明示该作用域从未下发', async () => {
    // 不用 useScenario('empty')：那是**全局**场景，会把命名空间域一起清空，
    // 页面就只剩「请先选择命名空间」，压根没有配置表可断言。
    // 这里只替换本页的 GET 响应，构造「该作用域一项都没下发过」的最小态。
    server.use(
      http.get('*/admin/v2/agent-settings', () =>
        HttpResponse.json({
          items: [
            { key: 'timing.poll-timeout-ms', value: '', source: 'local-or-default', desc: '未下发' },
            { key: 'assets.enabled', value: '', source: 'local-or-default', desc: '未下发' },
          ],
          updatedAt: '',
        } satisfies AgentSettingsResponse),
      ),
    )
    useScenario('normal')
    renderPage(<AgentSettingsPage />)

    const row = await rowOf('timing.poll-timeout-ms')
    expect(within(row).getByText('本地配置或代码默认')).toBeInTheDocument()
    // 控制面不知道本地生效值：只显示占位符，不回填任何「代码默认值」
    expect(within(row).getByText('—')).toBeInTheDocument()
    // updatedAt 空串 → 页脚明示该作用域从未下发过
    expect(await screen.findByText('该作用域尚未下发过任何配置')).toBeInTheDocument()
  }, 20_000)
})

// 只有控制面在某层真有覆盖时，「撤销」才是一个真实改动。
//
// 这条判断同时决定两件事：行内是否出现「撤销本层覆盖」按钮，以及提交体里是否要出现该键。
// 后者是防御性冗余（正常情况下按钮已经挡住了），但它是「撤销无操作也会产生一条空下发」
// 的唯一防线——单点靠 UI 断言测不到，故在此直接测纯函数。
describe('撤销本层覆盖的可撤销性判定', () => {
  it('server / namespace 两档代表本层真有覆盖，可撤销', () => {
    expect(isRevocableAgentSettingSource('server')).toBe(true)
    expect(isRevocableAgentSettingSource('namespace')).toBe(true)
  })

  it('未下发（含旧服务端取值）不可撤销：那一层根本没有行可删', () => {
    expect(isRevocableAgentSettingSource('local-or-default')).toBe(false)
    // 旧服务端 / 旧契约取值语义等同 local-or-default
    expect(isRevocableAgentSettingSource('unset')).toBe(false)
    expect(isRevocableAgentSettingSource('default')).toBe(false)
  })

  it('未识别的来源一律不可撤销（版本错配时不冒险删数据）', () => {
    expect(isRevocableAgentSettingSource('brand-new-source')).toBe(false)
    expect(isRevocableAgentSettingSource('')).toBe(false)
  })
})

// 白名单取值校验的纯函数用例：口径必须与服务端 agentSettingTypeStringList / agent 侧
// ConfigReader.stringList 一致（逗号或换行分隔，**没有** JSON 解析）。
describe('命令白名单取值校验（与服务端 stringList 同口径）', () => {
  it('逗号或换行分隔的命令首 token 合法', () => {
    expect(validateCommandWhitelist('reload')).toBe(true)
    expect(validateCommandWhitelist('a, b')).toBe(true)
    expect(validateCommandWhitelist('a\nb')).toBe(true)
    expect(validateCommandWhitelist('taboolib, reload')).toBe(true)
    // 切分口径：逗号与换行都算分隔符，逐项 trim，丢弃空项
    expect(splitAgentSettingStringList('a, b\nc')).toEqual(['a', 'b', 'c'])
    expect(splitAgentSettingStringList(' a ,, b \n ')).toEqual(['a', 'b'])
  })

  it('JSON 数组形态被明确拒绝（它就是白名单静默失效的根源）', () => {
    expect(commandWhitelistIssue('["reload"]')).toBe('json-array')
    expect(validateCommandWhitelist('["reload"]')).toBe(false)
  })

  it('含注入字符的项被拒绝', () => {
    expect(commandWhitelistIssue('reload; stop')).toBe('unsafe')
    expect(commandWhitelistIssue('a && b')).toBe('unsafe')
    expect(commandWhitelistIssue('a | b')).toBe('unsafe')
    expect(commandWhitelistIssue('a > out')).toBe('unsafe')
    expect(commandWhitelistIssue('$(id)')).toBe('unsafe')
    expect(commandWhitelistIssue('a `id`')).toBe('unsafe')
  })

  it('空值与纯空白被拒绝（清空不等于撤销）', () => {
    expect(commandWhitelistIssue('')).toBe('empty')
    expect(commandWhitelistIssue('   ')).toBe('empty')
    expect(commandWhitelistIssue('\n')).toBe('empty')
  })

  it('含空格的一项不是「单个命令首 token」', () => {
    expect(commandWhitelistIssue('taboolib reload')).toBe('format')
  })
})
