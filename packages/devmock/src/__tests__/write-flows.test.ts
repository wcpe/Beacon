// 写操作闭环与场景重置：UI 操作后列表状态真的变化；切换场景 / 重置后回到初始数据；
// 非法状态迁移按规格回 409。
import { afterAll, afterEach, beforeAll, describe, expect, it } from 'vitest'
import { resetMockData, setMockScenario } from '../index'
import { callJson, resetMockWorld, server } from './msw-server'

interface IdentityItem {
  identityId: string
  serverId: string
  status: string
}

async function findIdentity(serverId: string): Promise<IdentityItem | undefined> {
  const { json } = await callJson('GET', `/admin/v2/agent-identities?keyword=${serverId}&pageSize=100`)
  const paged = json as { items: IdentityItem[] }
  return paged.items.find((item) => item.serverId === serverId)
}

beforeAll(() => {
  server.listen({ onUnhandledRequest: 'error' })
})
afterEach(() => {
  resetMockWorld()
})
afterAll(() => {
  server.close()
})

describe('身份确认闭环（approve）', () => {
  it('approve 待确认身份后列表状态变化，且未分配篮出现新 server', async () => {
    const pending = await findIdentity('game-new-1')
    expect(pending?.status).toBe('pending')

    const before = (await callJson('GET', '/admin/v2/servers?assigned=false&namespaceId=1&pageSize=100')).json as {
      items: { serverId: string }[]
      total: number
    }
    expect(before.items.some((s) => s.serverId === 'game-new-1')).toBe(false)

    const approve = await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/approve`, {})
    expect(approve.status).toBe(200)

    const after = await findIdentity('game-new-1')
    expect(after?.status).toBe('active')

    const basket = (await callJson('GET', '/admin/v2/servers?assigned=false&namespaceId=1&pageSize=100')).json as {
      items: { serverId: string }[]
    }
    expect(basket.items.some((s) => s.serverId === 'game-new-1')).toBe(true)
  })

  it('非法迁移返回 409：对 active 身份再 approve', async () => {
    const active = await findIdentity('game-1')
    expect(active?.status).toBe('active')
    const { status, json } = await callJson('POST', `/admin/v2/agent-identities/${active?.identityId ?? ''}/approve`, {})
    expect(status).toBe(409)
    expect((json as { code: string }).code).toBe('illegal_state')
  })

  it('原因必填的写操作缺原因返回 400', async () => {
    const pending = await findIdentity('game-new-1')
    const { status, json } = await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/reject`, {})
    expect(status).toBe(400)
    expect((json as { code: string }).code).toBe('missing_reason')
  })

  it('占用冲突（Q3）未勾选强制解绑的 approve 被 409 拒绝', async () => {
    const { json } = await callJson('GET', '/admin/v2/agent-identities?status=pending&pageSize=100')
    const paged = json as { items: (IdentityItem & { conflictReason: string | null })[] }
    const occupied = paged.items.find((item) => item.conflictReason === 'server-id-occupied')
    expect(occupied).toBeDefined()
    const denied = await callJson('POST', `/admin/v2/agent-identities/${occupied?.identityId ?? ''}/approve`, {})
    expect(denied.status).toBe(409)
    expect((denied.json as { code: string }).code).toBe('server_id_occupied')
    const forced = await callJson('POST', `/admin/v2/agent-identities/${occupied?.identityId ?? ''}/approve`, {
      forceUnbindOccupier: true,
    })
    expect(forced.status).toBe(200)
    // 旧占用身份被解绑
    const old = await findIdentity('lobby-2')
    expect(old?.status).toBe('unbound')
  })
})

describe('场景切换与重置', () => {
  it('切换场景后数据集重置：approve 的变更不残留', async () => {
    const pending = await findIdentity('game-new-1')
    await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/approve`, {})
    expect((await findIdentity('game-new-1'))?.status).toBe('active')

    setMockScenario('huge')
    setMockScenario('normal')
    expect((await findIdentity('game-new-1'))?.status).toBe('pending')
  })

  it('resetMockData 直接重建当前场景数据', async () => {
    const pending = await findIdentity('game-new-1')
    await callJson('POST', `/admin/v2/agent-identities/${pending?.identityId ?? ''}/approve`, {})
    resetMockData()
    expect((await findIdentity('game-new-1'))?.status).toBe('pending')
  })
})

describe('变更单生命周期闭环', () => {
  it('创建 → 提审 → 审批 → 启动 → 批次放行 → 完成 → 整单回滚', async () => {
    const created = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '测试专用小流量变更',
      selector: { servers: ['pvp-1'] },
    })
    expect(created.status).toBe(201)
    const orderId = (created.json as { id: number }).id

    expect((await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/submit`)).status).toBe(200)
    expect((await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/approve`)).status).toBe(200)

    const started = await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/start`)
    expect(started.status).toBe(200)
    const startedOrder = started.json as { status: string; batches: { batchNo: number; status: string }[] }
    expect(startedOrder.status).toBe('rolling')
    const awaiting = startedOrder.batches.find((b) => b.status === 'awaiting_confirm')
    expect(awaiting).toBeDefined()

    const confirmed = await callJson(
      'POST',
      `/admin/v2/change-orders/${String(orderId)}/batches/${String(awaiting?.batchNo ?? 0)}/confirm`,
    )
    expect(confirmed.status).toBe(200)
    expect((confirmed.json as { status: string }).status).toBe('completed')

    const rolledBack = await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/rollback`, {
      reason: '演示回滚',
    })
    expect(rolledBack.status).toBe(200)
    expect((rolledBack.json as { status: string }).status).toBe('rolled_back')
  })

  it('目标集与活动单交叠时启动被 409 拒绝（冲突守卫）', async () => {
    // game-1 属于常规态 rolling 单的目标集
    const created = await callJson('POST', '/admin/v2/change-orders', {
      namespaceId: 1,
      title: '冲突守卫演示',
      selector: { servers: ['game-1'] },
    })
    const orderId = (created.json as { id: number }).id
    await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/submit`)
    await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/approve`)
    const started = await callJson('POST', `/admin/v2/change-orders/${String(orderId)}/start`)
    expect(started.status).toBe(409)
    expect((started.json as { code: string }).code).toBe('target_conflict')
  })

  it('draft 以外状态编辑 / 删除按状态机拒绝', async () => {
    const rollingList = (await callJson('GET', '/admin/v2/change-orders?status=rolling')).json as {
      items: { id: number }[]
    }
    const rollingId = rollingList.items[0].id
    const del = await callJson('DELETE', `/admin/v2/change-orders/${String(rollingId)}`)
    expect(del.status).toBe(409)
  })
})

describe('配置版本链写闭环', () => {
  async function essentialsFileId(): Promise<number> {
    const files = (await callJson('GET', '/admin/v2/config-files?namespaceId=1&keyword=Essentials')).json as {
      items: { id: number; name: string }[]
    }
    const file = files.items.find((f) => f.name === 'plugins/Essentials/config.yml')
    if (!file) {
      throw new Error('fixture 缺失 Essentials 配置文件')
    }
    return file.id
  }

  it('保存新版本：过期基线 409，正确基线 201 并推进版本号', async () => {
    const fileId = await essentialsFileId()
    const versions = (
      await callJson('GET', `/admin/v2/config-files/${String(fileId)}/versions?scopeLevel=namespace&scopeRefId=1`)
    ).json as { items: { versionId: number; versionNo: number }[] }
    const head = versions.items[0]

    const stale = await callJson('POST', `/admin/v2/config-files/${String(fileId)}/versions`, {
      scopeLevel: 'namespace',
      scopeRefId: 1,
      content: 'teleport-cooldown: 9',
      basedOnVersionId: 12345,
    })
    expect(stale.status).toBe(409)
    expect((stale.json as { code: string }).code).toBe('CONFIG_VERSION_CONFLICT')

    const saved = await callJson('POST', `/admin/v2/config-files/${String(fileId)}/versions`, {
      scopeLevel: 'namespace',
      scopeRefId: 1,
      content: 'teleport-cooldown: 9\nspawn-on-join: false\neconomy-enabled: true\nmotd: 欢迎来到 Beacon 集群',
      remark: '压测调参',
      basedOnVersionId: head.versionId,
    })
    expect(saved.status).toBe(201)
    expect((saved.json as { versionNo: number }).versionNo).toBe(head.versionNo + 1)
  })

  it('回收站闭环：删除 → 常规列表消失 → 恢复', async () => {
    const fileId = await essentialsFileId()
    expect((await callJson('DELETE', `/admin/v2/config-files/${String(fileId)}`)).status).toBe(204)
    const list = (await callJson('GET', '/admin/v2/config-files?namespaceId=1&keyword=Essentials')).json as {
      items: { id: number }[]
    }
    expect(list.items.some((f) => f.id === fileId)).toBe(false)
    const trash = (await callJson('GET', '/admin/v2/config-files/trash?namespaceId=1')).json as {
      items: { id: number }[]
    }
    expect(trash.items.some((f) => f.id === fileId)).toBe(true)
    expect((await callJson('POST', `/admin/v2/config-files/${String(fileId)}/restore`)).status).toBe(200)
  })
})

describe('健康权重热更', () => {
  it('非法阈值 400，合法配置产生新 rev', async () => {
    const current = (await callJson('GET', '/admin/v2/settings/health-weights')).json as {
      current: { rev: number; config: { weights: Record<string, number>; normalize: Record<string, number>; levels: { healthyMin: number; degradedMin: number } } }
    }
    const bad = await callJson('PUT', '/admin/v2/settings/health-weights', {
      ...current.current.config,
      levels: { healthyMin: 40, degradedMin: 50 },
    })
    expect(bad.status).toBe(400)

    const good = await callJson('PUT', '/admin/v2/settings/health-weights', current.current.config)
    expect(good.status).toBe(200)
    expect((good.json as { current: { rev: number } }).current.rev).toBe(current.current.rev + 1)
  })
})
