// 变更单详情审批进度视图（FR-255）组件级断言：按 orderId 反查关联的统一审批申请，
// 展示状态 / 审批人 / 驳回理由，并给出去审批中心的深链。
// 真机约束：变更单表不落 approval_request_id，故按 keyword=单号 反查再按 resourceId 精确过滤。
import { screen, within } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import ApprovalProgress from '../../features/delivery/approval-progress'
import { fetchChangeOrders } from '../../api/delivery-changes'
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

async function orderIdByTitle(title: string): Promise<number> {
  const list = await fetchChangeOrders({ keyword: title, pageSize: 20 })
  const row = list.items.find((item) => item.title === title)
  if (!row) {
    throw new Error(`种子缺少变更单 ${title}`)
  }
  return row.id
}

// 审批申请行（真机字段形；只取本视图消费的键）
function approvalRow(overrides: Record<string, unknown>): Record<string, unknown> {
  return {
    id: 9101,
    requestId: 'apr_change_9101',
    operationKey: 'delivery.approve',
    operationKind: 'delivery.approve',
    resourceType: 'change-order',
    resourceId: '5002',
    riskLevel: 'high',
    status: 'pending',
    requestReason: '经济系统配置调优上线窗口',
    safeSummary: '提审变更单 #5002',
    frozenPayloadSha256: 'hash',
    requesterType: 'human',
    requesterId: 'ops-chen',
    deciderType: null,
    deciderId: null,
    rejectReason: null,
    decisionReason: null,
    expiresAt: null,
    version: 1,
    namespaceId: 1,
    createdAt: '2024-05-01T00:00:00.000Z',
    updatedAt: '2024-05-01T02:00:00.000Z',
    ...overrides,
  }
}

describe('ApprovalProgress 审批进度视图', () => {
  it('待审批单：展示申请号 / 状态 / 审批人待处理 / 申请原因与审批中心入口', async () => {
    useScenario('normal')
    const orderId = await orderIdByTitle('经济系统配置调优')

    renderPage(<ApprovalProgress orderId={orderId} />)

    expect(await screen.findByText('审批进度')).toBeInTheDocument()
    // 首行加载完成后再断言其余字段（标题与入口在加载态即已渲染）
    expect(await screen.findByText('提交审批')).toBeInTheDocument()
    expect(screen.getByText('apr_change_9101')).toBeInTheDocument()
    expect(screen.getByText('待审批')).toBeInTheDocument()
    expect(screen.getByText(/审批人：待处理/)).toBeInTheDocument()
    expect(screen.getByText(/按变更窗口执行，需统一审批留痕/)).toBeInTheDocument()
    // 深链：去本申请的审批详情 + 去审批中心列表
    expect(screen.getByRole('link', { name: /查看申请/ })).toHaveAttribute(
      'href',
      '/approvals/apr_change_9101',
    )
    expect(screen.getByRole('link', { name: '去审批中心' })).toHaveAttribute('href', '/approvals')
  })

  it('已驳回单：驳回理由与审批人一并可见（提审后能立刻看到为什么没过）', async () => {
    useScenario('normal')
    server.use(
      http.get('*/admin/v2/approval-requests', () =>
        HttpResponse.json({
          items: [
            approvalRow({
              requestId: 'apr_change_9001_7',
              resourceId: '9001',
              status: 'rejected',
              deciderType: 'human',
              deciderId: 'ops-li',
              rejectReason: '回滚计划缺失，补齐后重提',
              decisionReason: '风险过高，拒绝执行',
              updatedAt: '2024-05-02T08:30:00.000Z',
            }),
          ],
          total: 1,
        }),
      ),
    )

    renderPage(<ApprovalProgress orderId={9001} />)

    expect(await screen.findByText('已驳回')).toBeInTheDocument()
    expect(screen.getByText(/审批人：ops-li/)).toBeInTheDocument()
    expect(screen.getByText(/驳回理由：回滚计划缺失，补齐后重提/)).toBeInTheDocument()
  })

  it('资源号同号但资源类型不同（或同号其他单）的申请不被误挂到本单', async () => {
    useScenario('normal')
    server.use(
      http.get('*/admin/v2/approval-requests', () =>
        HttpResponse.json({
          items: [
            // 同号但是服务器资源（keyword 命中 resource_id，必须再按资源类型过滤）
            approvalRow({ requestId: 'apr_server_5002', resourceType: 'server', resourceId: '5002' }),
            // 变更单资源但号不同（前缀匹配命中的别的单）
            approvalRow({ requestId: 'apr_change_15002', resourceId: '15002' }),
          ],
          total: 2,
        }),
      ),
    )

    const orderId = await orderIdByTitle('经济系统配置调优')
    renderPage(<ApprovalProgress orderId={orderId} />)

    // 无本单的申请 → 不渲染空卡
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByText('审批进度')).not.toBeInTheDocument()
  })

  it('非提审申请（继续 / 批次放行 / 回滚）按操作键显示对应动作名，并列出多条历史申请', async () => {
    useScenario('normal')
    const rows = Array.from({ length: 6 }, (_, index) =>
      approvalRow({
        id: 9500 + index,
        requestId: `apr_change_9002_${String(index + 1)}`,
        resourceId: '9002',
        operationKey: index === 0 ? 'delivery.rollback' : index === 1 ? 'delivery.confirm_batch' : 'delivery.resume',
        status: index === 0 ? 'succeeded' : index === 1 ? 'executing' : 'pending',
        deciderId: index === 0 ? 'admin' : null,
        // 倒序：index 0（整单回滚）最新
        updatedAt: `2024-05-${String(10 - index)}T00:00:00.000Z`,
      }),
    )
    server.use(http.get('*/admin/v2/approval-requests', () => HttpResponse.json({ items: rows, total: rows.length })))

    renderPage(<ApprovalProgress orderId={9002} />)

    // 最新一条是整单回滚（按更新时间倒序），其余 5 条历史申请仍在展示上限内
    expect(await screen.findByText('整单回滚')).toBeInTheDocument()
    expect(screen.getByText('批次推进放行')).toBeInTheDocument()
    expect(screen.getAllByText('继续灰度').length).toBeGreaterThan(0)
    expect(screen.getByText('已通过')).toBeInTheDocument()
    expect(screen.getByText('执行中')).toBeInTheDocument()
    // 超出展示上限的条数只提示不铺开
    expect(screen.getByText('另有 1 条历史申请')).toBeInTheDocument()
    const list = screen.getByRole('list')
    expect(within(list).getAllByRole('listitem')).toHaveLength(5)
  })
})
