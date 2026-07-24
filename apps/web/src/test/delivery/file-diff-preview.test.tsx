// 变更项文件内容预览三形态测试（FileDiffPreview 组件级）：
// 文本内容 + 对比目标标签 / 二进制仅元数据 / 敏感 403 填原因放行 / agent 离线 504 可重试。
// 错误分流按 HTTP status（经 ApiClientError），不耦合错误码字符串。
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import FileDiffPreview from '../../features/delivery/file-diff-preview'
import { fetchChangeOrder, type ChangeOrderItem } from '../../api/delivery-changes'
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

// 种子单「Quests 插件灰度 v1.9」（rolling，目标 game-1..game-6，首个在线目标 game-1）
const ORDER_ID = 5004

/** 从种子单详情取指定路径后缀的文件差异项（保证 item 字段与 devmock 数据一致） */
async function itemByPath(suffix: string): Promise<ChangeOrderItem> {
  const detail = await fetchChangeOrder(ORDER_ID)
  const item = detail.items.find((row) => row.path?.endsWith(suffix) === true)
  if (!item) {
    throw new Error(`种子单缺少 ${suffix} 变更项`)
  }
  return item
}

describe('FileDiffPreview 三形态', () => {
  it('文本项展示内容与对比目标标签（before 侧默认取首个在线目标）', async () => {
    useScenario('normal')
    const item = await itemByPath('Essentials/config.yml')
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    expect((await screen.findAllByText(/max-players/)).length).toBeGreaterThan(0)
    expect(screen.getByText('对比目标：game-1')).toBeInTheDocument()
  })

  it('二进制项不出内容，仅展示元数据（路径 / 大小 / 哈希）', async () => {
    useScenario('normal')
    const item = await itemByPath('Essentials.jar')
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    expect(await screen.findByText('二进制文件不支持内容对比，仅展示元数据')).toBeInTheDocument()
    expect(screen.getByText('plugins/Essentials.jar')).toBeInTheDocument()
    expect(screen.getByText(/大小 .+ · 哈希 /)).toBeInTheDocument()
    expect(screen.queryByText(/max-players/)).not.toBeInTheDocument()
  })

  it('敏感路径 403：内联填写原因后带 reason 重试可见内容', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    const item = await itemByPath('database-password.yml')
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    // 无原因首取 → 403 → 内联原因表单（确认按钮空原因禁用）
    expect(await screen.findByText(/命中敏感规则/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '填写原因后查看' })).toBeDisabled()

    await user.type(screen.getByLabelText('查看原因'), '排查经济配置异常')
    await user.click(screen.getByRole('button', { name: '填写原因后查看' }))

    // 带原因重取成功 → 内容出现
    expect((await screen.findAllByText(/max-players/)).length).toBeGreaterThan(0)
  })

  it('agent 离线 504：展示脱敏真因并可手动重试', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    const item = await itemByPath('Essentials/config.yml')
    // 注入一次性 504（模拟 before 侧 agent 离线）
    server.use(
      http.get('*/admin/v2/change-orders/:id/items/:itemId/file-diff', () =>
        HttpResponse.json(
          { code: 'agent_offline', message: '对比目标 game-1 的 agent 离线，无法读取文件内容', traceId: 'trace-devmock' },
          { status: 504 },
        ),
      ),
    )
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    // 真因可见 + 重试按钮
    expect(await screen.findByText(/agent 离线/)).toBeInTheDocument()

    // 恢复正常 handler（模拟 agent 回归在线）后重试 → 内容出现
    server.resetHandlers()
    await user.click(screen.getByRole('button', { name: '重试' }))
    expect((await screen.findAllByText(/max-players/)).length).toBeGreaterThan(0)
  })
})
