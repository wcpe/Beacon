// 变更项文件内容预览（FileDiffPreview 组件级）：真机对读源服文件内容恒回 409
// operation_requires_approval，故组件只展示「需审批」引导 + 审批中心入口；
// 审批放行后的内容形态（文本 diff / 二进制元数据）在 200 响应下仍按变更类型渲染。
import { screen } from '@testing-library/react'
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

// 种子单「Quests 插件灰度 v1.9」（rolling，目标 game-1..game-6）
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

describe('FileDiffPreview 需审批形态', () => {
  it('409 operation_requires_approval：展示需审批引导与审批中心入口（不静默失败）', async () => {
    useScenario('normal')
    const item = await itemByPath('Essentials/config.yml')
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    expect(await screen.findByText('该文件内容需审批后查看')).toBeInTheDocument()
    expect(screen.getByText(/读取源服文件内容属于敏感操作/)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '去审批中心' })).toHaveAttribute('href', '/approvals')
    // 不给「重试」假出口：重试不会绕过审批
    expect(screen.queryByRole('button', { name: '重试' })).not.toBeInTheDocument()
  })

  it('409 但错误码不是 operation_requires_approval：走通用错误展示，不误报「需审批」', async () => {
    useScenario('normal')
    const item = await itemByPath('Essentials/config.yml')
    server.use(
      http.get('*/admin/v2/change-orders/:id/items/:itemId/file-diff', () =>
        HttpResponse.json({ code: 'asset_conflict', message: '该文件资产存在冲突，请先解决' }, { status: 409 }),
      ),
    )
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    // 真实原因照原样展示（不静默），且不给出与成因不符的审批引导
    expect(await screen.findByText(/该文件资产存在冲突/)).toBeInTheDocument()
    expect(screen.queryByText('该文件内容需审批后查看')).not.toBeInTheDocument()
  })

  it('审批放行后的文本项：渲染内容与对比目标标签（200 形态仍可用）', async () => {
    useScenario('normal')
    const item = await itemByPath('Essentials/config.yml')
    server.use(
      http.get('*/admin/v2/change-orders/:id/items/:itemId/file-diff', () =>
        HttpResponse.json({
          path: item.path,
          changeType: 'modified',
          before: 'max-players: 20\n',
          after: 'max-players: 40\n',
          truncated: false,
          binary: false,
          serverId: 'game-1',
        }),
      ),
    )
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    expect((await screen.findAllByText(/max-players/)).length).toBeGreaterThan(0)
    expect(screen.getByText('对比目标：game-1')).toBeInTheDocument()
  })

  it('审批放行后的二进制项：不出内容，仅展示元数据（路径 / 大小 / 哈希）', async () => {
    useScenario('normal')
    const item = await itemByPath('Essentials.jar')
    server.use(
      http.get('*/admin/v2/change-orders/:id/items/:itemId/file-diff', () =>
        HttpResponse.json({
          path: item.path,
          changeType: 'modified',
          before: null,
          after: null,
          truncated: false,
          binary: true,
          serverId: 'game-1',
        }),
      ),
    )
    renderPage(<FileDiffPreview orderId={ORDER_ID} item={item} />)

    expect(await screen.findByText('二进制文件不支持内容对比，仅展示元数据')).toBeInTheDocument()
    expect(screen.getByText('plugins/Essentials.jar')).toBeInTheDocument()
    expect(screen.getByText(/大小 .+ · 哈希 /)).toBeInTheDocument()
    expect(screen.queryByText(/max-players/)).not.toBeInTheDocument()
  })
})
