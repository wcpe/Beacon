// 单服级状态墙（FR-254）组件级断言：逐台展示正推状态、失败原因、回滚状态与备份标记，
// 让「执行中哪台到哪一步、哪台失败为什么」可见（此前 error / rollbackError 全站无渲染）。
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import TargetStatusWall from '../../features/delivery/target-status-wall'
import { fetchChangeOrders, fetchChangeOrder } from '../../api/delivery-changes'
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

/** 按标题取种子单号（避免测试硬编码种子顺序） */
async function orderIdByTitle(title: string): Promise<number> {
  const list = await fetchChangeOrders({ keyword: title, pageSize: 20 })
  const row = list.items.find((item) => item.title === title)
  if (!row) {
    throw new Error(`种子缺少变更单 ${title}`)
  }
  return row.id
}

describe('TargetStatusWall 单服级状态墙', () => {
  it('熔断暂停单：逐台可见失败状态与失败原因，并标出备份缺失', async () => {
    useScenario('normal')
    const orderId = await orderIdByTitle('PVP 平衡性补丁')
    const detail = await fetchChangeOrder(orderId)
    const failed = detail.batches.find((batch) => batch.status === 'failed')
    expect(failed).toBeDefined()

    renderPage(<TargetStatusWall orderId={orderId} orderStatus={detail.status} />)

    // 区块标题 + 汇总（总台数与失败计数）
    expect(await screen.findByText('单服状态')).toBeInTheDocument()
    expect(await screen.findByText(/共 \d+ 台目标/)).toBeInTheDocument()
    expect(screen.getByText(/台失败/)).toBeInTheDocument()
    // 状态徽标（失败）与失败原因（真因可见，不静默隐藏）
    const table = screen.getByRole('table')
    expect(within(table).getAllByText('失败').length).toBeGreaterThan(0)
    expect(within(table).getByText('生效超时：重启后 300 秒内心跳未回归')).toBeInTheDocument()
    // 备份标记两态之一可见（失败目标仍应显示是否留有备份）
    expect(within(table).getAllByText(/已备份|无备份/).length).toBeGreaterThan(0)
  })

  it('灰度中单：逐台展示正推状态与批次归属，未到的批次显示待推送', async () => {
    useScenario('normal')
    const orderId = await orderIdByTitle('Quests 插件灰度 v1.9')
    const detail = await fetchChangeOrder(orderId)

    renderPage(<TargetStatusWall orderId={orderId} orderStatus={detail.status} />)

    const table = await screen.findByRole('table')
    // 表头：正推状态 / 回滚状态 / 失败原因 / 回滚失败原因 / 备份
    expect(within(table).getByRole('columnheader', { name: '正推状态' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '失败原因' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '回滚失败原因' })).toBeInTheDocument()
    expect(within(table).getByRole('columnheader', { name: '备份' })).toBeInTheDocument()
    // 已生效 / 待推送两态并存（当前批已生效、后续批次待推送）
    expect(within(table).getAllByText('已生效').length).toBeGreaterThan(0)
    expect(within(table).getAllByText('待推送').length).toBeGreaterThan(0)
    // 未进入回滚的目标给「未回滚」占位，不显示空白
    expect(within(table).getAllByText('未回滚').length).toBeGreaterThan(0)
  })

  it('回滚中单：回滚失败目标的原因按脱敏真因展示，成功目标显示已回滚', async () => {
    useScenario('normal')
    const orderId = await orderIdByTitle('坏更新整单回滚示例')
    const detail = await fetchChangeOrder(orderId)
    // 注入一行回滚失败 + 一行回滚成功（回滚失败原因是运维定位关键，必须可见）
    server.use(
      http.get('*/admin/v2/change-orders/:id/targets', () =>
        HttpResponse.json({
          items: [
            {
              serverId: 'game-1',
              batchNo: 1,
              status: 'activated',
              pushedAt: null,
              activatedAt: null,
              changedFileCount: 5,
              skippedFileCount: 0,
              backupPresent: false,
              error: null,
              rollbackStatus: 'failed',
              rollbackError: '备份不存在（已被保留策略清理），无法文件回滚',
            },
            {
              serverId: 'game-2',
              batchNo: 2,
              status: 'activated',
              pushedAt: null,
              activatedAt: null,
              changedFileCount: 5,
              skippedFileCount: 0,
              backupPresent: true,
              error: null,
              rollbackStatus: 'rolled_back',
              rollbackError: null,
            },
          ],
          total: 2,
        }),
      ),
    )

    renderPage(<TargetStatusWall orderId={orderId} orderStatus={detail.status} />)

    const table = await screen.findByRole('table')
    expect(within(table).getByText('game-1')).toBeInTheDocument()
    expect(within(table).getByText('回滚失败')).toBeInTheDocument()
    expect(within(table).getByText('备份不存在（已被保留策略清理），无法文件回滚')).toBeInTheDocument()
    expect(within(table).getByText('已回滚')).toBeInTheDocument()
    // 汇总行：总台数 + 回滚失败计数（同一段落内，用正则匹配）
    expect(screen.getByText(/1 台回滚失败/)).toBeInTheDocument()
  })
})
