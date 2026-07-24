// /alert-events 告警事件页测试：KPI + 列表渲染、空态、处理写闭环（确认 / 标记已处理 / 403 错误展示）。
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'

import AlertEventsPage from '../../pages/alert-events'
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

// 打开首条「待处理」告警的详情面板（写闭环用例共用步骤）
async function openFirstOpenAlert(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  let row: HTMLTableRowElement | null = null
  await waitFor(() => {
    const badges = screen.getAllByText('待处理')
    const tr = badges.map((el) => el.closest('tr')).find((r): r is HTMLTableRowElement => r !== null) ?? null
    expect(tr).not.toBeNull()
    row = tr
  })
  await user.click(row as unknown as HTMLElement)
  await waitFor(() => {
    expect(screen.getByText('告警详情')).toBeInTheDocument()
  })
}

describe('/alert-events 告警事件页', () => {
  it('常规态渲染 KPI 与告警列表', async () => {
    useScenario('normal')
    renderPage(<AlertEventsPage />)

    expect(await screen.findByText('告警总数')).toBeInTheDocument()
    // 健康流转摘要已 i18n：亚健康/失联/在线 等中文态 + 箭头
    expect(
      (await screen.findAllByText(/亚健康\s*→\s*失联|在线\s*→\s*亚健康|失联\s*→\s*离线|在线\s*→\s*失联/)).length,
    ).toBeGreaterThan(0)
  })

  it('空态给出无记录提示', async () => {
    useScenario('empty')
    renderPage(<AlertEventsPage />)

    expect(await screen.findByText('当前筛选条件下无告警事件')).toBeInTheDocument()
  })

  it('点行开右侧非模态详情面板并确认待处理告警（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)

    // 等待列表首屏，找到第一条「待处理」告警行
    let row: HTMLTableRowElement | null = null
    await waitFor(() => {
      const badges = screen.getAllByText('待处理')
      const tr = badges.map((el) => el.closest('tr')).find((r): r is HTMLTableRowElement => r !== null) ?? null
      expect(tr).not.toBeNull()
      row = tr
    })

    // 点行 → 固定层详情出现（非 dialog，主表不 reflow）
    await user.click(row as unknown as HTMLElement)
    await waitFor(() => {
      expect(screen.getByText('告警详情')).toBeInTheDocument()
    })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(screen.getByRole('table')).toBeInTheDocument()

    // 面板内点「确认」完成写闭环
    await user.click(screen.getByRole('button', { name: '确认' }))

    // 详情面板内状态徽标更新为「已确认」（选中行从最新数据派生；排除筛选下拉的同名 option）
    await waitFor(() => {
      const acknowledged = screen
        .getAllByText('已确认')
        .filter((el) => el.tagName !== 'OPTION')
      expect(acknowledged.length).toBeGreaterThan(0)
    })
  })

  it('填写备注标记已处理：处理人与备注即时可见（写闭环）', async () => {
    useScenario('normal')
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)
    await openFirstOpenAlert(user)

    // 备注未填时「标记已处理」禁用（resolved 备注必填约束在面板）
    const resolveBtn = screen.getByRole('button', { name: '标记已处理' })
    expect(resolveBtn).toBeDisabled()

    await user.type(screen.getByLabelText('处理备注'), '已重启 agent')
    await user.click(resolveBtn)

    // 写成功 → 列表 invalidate → 面板从最新数据派生出处理人与备注
    expect(await screen.findByText('处理人')).toBeInTheDocument()
    expect(screen.getByText('已重启 agent')).toBeInTheDocument()
  })

  it('处理失败（readonly 403）时面板展示后端脱敏错误文案（不静默）', async () => {
    useScenario('normal')
    // 覆写 handle 端点模拟 readonly 守卫 403（真后端 readonlyWriteGuard 行为）
    server.use(
      http.post('*/admin/v1/alert-events/:id/handle', () =>
        HttpResponse.json(
          { code: 'forbidden', message: '只读模式禁止写操作', traceId: 'trace-test' },
          { status: 403 },
        ),
      ),
    )
    const user = userEvent.setup()
    renderPage(<AlertEventsPage />)
    await openFirstOpenAlert(user)

    await user.click(screen.getByRole('button', { name: '确认' }))

    // 后端脱敏 message 原样展示在面板内，错误不被静默（ADR-0057）
    expect(await screen.findByText('只读模式禁止写操作')).toBeInTheDocument()
    // 告警仍为待处理，处理表单未消失（可重试）
    expect(screen.getByRole('button', { name: '确认' })).toBeInTheDocument()
  })
})
